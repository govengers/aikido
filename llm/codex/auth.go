package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/govengers/aikido/llm"
)

// Protocol constants of the ChatGPT login, as used by the Codex CLI.
const (
	DefaultIssuer = "https://auth.openai.com"
	ClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	// deviceRedirectURI is fixed by OpenAI for the device-code exchange.
	deviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
	// refreshSkew refreshes an access token this long before it expires.
	refreshSkew = 5 * time.Minute
	// deviceCodeTTL is how long a device code stays valid.
	deviceCodeTTL = 15 * time.Minute
	// minimumPollInterval keeps polling polite when the issuer sends no or a
	// too small interval.
	minimumPollInterval = 3 * time.Second
	defaultLoginTimeout = 30 * time.Second
	// refreshTimeout bounds a refresh that runs detached from its caller.
	refreshTimeout = time.Minute
	// maximumLoginResponseBytes caps what is read from a login endpoint.
	maximumLoginResponseBytes = 1 << 20
)

// ErrReauthenticationRequired means the login is gone for good (refresh token reused,
// expired or revoked): the user has to log in again. Retrying cannot help.
var ErrReauthenticationRequired = errors.New("aikido/codex: ChatGPT login expired or revoked, log in again")

// ErrRefreshInterrupted means a refresh got no complete answer (transport
// failure, cut response, a 200 without tokens): the issuer may have rotated
// the refresh token, and the rotated one is lost. The error wraps the cause
// too: llm.ErrServerError, or context.DeadlineExceeded when the refresh ran
// out of time. The next Token call refreshes with the previous token
// again; if that fails with ErrReauthenticationRequired, the user has to sign
// in again. No client can do better: the issuer offers no way to recover a
// rotated token whose answer did not arrive.
var ErrRefreshInterrupted = errors.New("aikido/codex: refresh interrupted, the refresh token may have rotated; " +
	"if the next refresh fails with refresh token reuse, sign in again")

// ErrDeviceLoginDisabled means the issuer does not offer device-code login
// (it answered 404 to the user-code request).
var ErrDeviceLoginDisabled = errors.New("aikido/codex: device-code login is not enabled")

// Tokens is one ChatGPT login. RefreshToken rotates on every refresh and is
// single-use: the caller must persist what OnRefresh hands over, and must not
// share it with another process (a replayed refresh token revokes the login).
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	// AccountID and PlanType come from the access token's claims.
	AccountID string
	PlanType  string
	// ExpiresAt is the access token's exp claim.
	ExpiresAt time.Time
}

// LoginOptions configure the login endpoints. A nil *LoginOptions and zero
// fields use the defaults.
type LoginOptions struct {
	// Issuer overrides DefaultIssuer (tests).
	Issuer string
	// HTTPClient overrides a client with a 30 s timeout.
	HTTPClient *http.Client
}

func (o *LoginOptions) issuer() string {
	if o != nil && o.Issuer != "" {
		return strings.TrimRight(o.Issuer, "/")
	}
	return DefaultIssuer
}

func (o *LoginOptions) client() *http.Client {
	if o != nil && o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: defaultLoginTimeout}
}

// TokensFromJWT builds Tokens from a token response, reading account, plan and
// expiry from the access token. The signature is not verified: the token only
// travels between OpenAI and the caller. It fails when accessToken is not a
// JWT with a JSON payload.
func TokensFromJWT(accessToken, refreshToken, idToken string) (Tokens, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return Tokens{}, errors.New("aikido/codex: access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return Tokens{}, fmt.Errorf("aikido/codex: access token payload: %w", err)
	}
	var claims struct {
		Expiry        float64 `json:"exp"`
		Authorization struct {
			AccountID string `json:"chatgpt_account_id"`
			PlanType  string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Tokens{}, fmt.Errorf("aikido/codex: access token claims: %w", err)
	}
	return Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      idToken,
		AccountID:    claims.Authorization.AccountID,
		PlanType:     claims.Authorization.PlanType,
		ExpiresAt:    time.Unix(int64(claims.Expiry), 0),
	}, nil
}

// ParseLogin reads a Codex CLI ~/.codex/auth.json. Only ChatGPT logins with
// an unexpired access token are accepted. Note the refresh token is then
// shared with that CLI: whichever side refreshes first logs the other out.
func ParseLogin(raw []byte) (Tokens, error) {
	var file struct {
		AuthenticationMode string `json:"auth_mode"`
		Tokens             *struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return Tokens{}, fmt.Errorf("aikido/codex: auth.json: %w", err)
	}
	if file.AuthenticationMode != "chatgpt" || file.Tokens == nil || file.Tokens.AccessToken == "" || file.Tokens.RefreshToken == "" {
		return Tokens{}, errors.New("aikido/codex: auth.json holds no ChatGPT login")
	}
	tokens, err := TokensFromJWT(file.Tokens.AccessToken, file.Tokens.RefreshToken, file.Tokens.IDToken)
	if err != nil {
		return Tokens{}, err
	}
	if tokens.AccountID == "" {
		tokens.AccountID = file.Tokens.AccountID
	}
	if !tokens.ExpiresAt.After(time.Now()) {
		return Tokens{}, errors.New("aikido/codex: auth.json access token has expired")
	}
	return tokens, nil
}

// DeviceCode is a started device login: show VerificationURL and UserCode to
// the user, then call PollDeviceLogin.
type DeviceCode struct {
	DeviceAuthorizationID string
	UserCode              string
	VerificationURL       string
	// Interval is the pause between two polls, at least three seconds.
	Interval  time.Duration
	ExpiresAt time.Time
}

// StartDeviceLogin asks the issuer for a device code. A nil options uses the
// defaults. It returns ErrDeviceLoginDisabled if the issuer has no device
// login, and an error wrapping an llm sentinel for other failed statuses.
func StartDeviceLogin(ctx context.Context, options *LoginOptions) (DeviceCode, error) {
	status, body, err := postJSON(ctx, options, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": ClientID})
	if err != nil {
		return DeviceCode{}, err
	}
	switch {
	case status == http.StatusNotFound:
		return DeviceCode{}, ErrDeviceLoginDisabled
	case status != http.StatusOK:
		return DeviceCode{}, fmt.Errorf("aikido/codex: device code: status %d: %w", status, statusSentinel(status))
	}
	var response struct {
		DeviceAuthorizationID string          `json:"device_auth_id"`
		UserCode              string          `json:"user_code"`
		AlternativeUserCode   string          `json:"usercode"`
		Interval              json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return DeviceCode{}, fmt.Errorf("aikido/codex: device code response: %w", err)
	}
	if response.UserCode == "" {
		response.UserCode = response.AlternativeUserCode
	}
	if response.DeviceAuthorizationID == "" || response.UserCode == "" {
		return DeviceCode{}, errors.New("aikido/codex: device code response incomplete")
	}
	return DeviceCode{
		DeviceAuthorizationID: response.DeviceAuthorizationID,
		UserCode:              response.UserCode,
		VerificationURL:       options.issuer() + "/codex/device",
		Interval:              pollInterval(response.Interval),
		ExpiresAt:             time.Now().Add(deviceCodeTTL),
	}, nil
}

// pollInterval reads the issuer's interval in seconds, a string on the wire
// with a number tolerated. Anything unreadable or below the minimum polls at
// minimumPollInterval, which is why the parse error carries no information.
func pollInterval(raw json.RawMessage) time.Duration {
	seconds, err := strconv.Atoi(strings.Trim(string(raw), `" `))
	if err != nil {
		return minimumPollInterval
	}
	return max(time.Duration(seconds)*time.Second, minimumPollInterval)
}

// PollDeviceLogin waits until the user confirmed the code (or it expired, or
// ctx ended), then exchanges it for tokens. A nil options uses the defaults.
// It returns ctx.Err() when ctx ends first.
func PollDeviceLogin(ctx context.Context, options *LoginOptions, deviceCode DeviceCode) (Tokens, error) {
	for {
		status, body, err := postJSON(ctx, options, "/api/accounts/deviceauth/token",
			map[string]string{"device_auth_id": deviceCode.DeviceAuthorizationID, "user_code": deviceCode.UserCode})
		if err != nil {
			return Tokens{}, err
		}
		switch status {
		case http.StatusOK:
			return exchangeDeviceGrant(ctx, options, body)
		case http.StatusForbidden, http.StatusNotFound:
			// not confirmed yet
		default:
			return Tokens{}, fmt.Errorf("aikido/codex: device login: status %d: %w", status, statusSentinel(status))
		}
		if time.Now().Add(deviceCode.Interval).After(deviceCode.ExpiresAt) {
			return Tokens{}, errors.New("aikido/codex: device code expired before it was confirmed")
		}
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		case <-time.After(deviceCode.Interval):
		}
	}
}

// exchangeDeviceGrant trades the authorization code of a confirmed device
// login for tokens.
func exchangeDeviceGrant(ctx context.Context, options *LoginOptions, body []byte) (Tokens, error) {
	var grant struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.Unmarshal(body, &grant); err != nil || grant.AuthorizationCode == "" {
		return Tokens{}, errors.New("aikido/codex: device token response incomplete")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {grant.AuthorizationCode},
		"redirect_uri":  {deviceRedirectURI},
		"code_verifier": {grant.CodeVerifier},
	}
	status, body, err := post(ctx, options, "/oauth/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	if status != http.StatusOK {
		return Tokens{}, tokenEndpointError(status)
	}
	return tokensFromResponse(body)
}

// TokenSource hands out a valid access token, refreshing it when it is about
// to expire. It is safe for concurrent use. Use one TokenSource per login and
// process: the mutex keeps exactly one refresh in flight, since a second
// refresh with the same (rotated) refresh token would revoke the login.
//
// Known limit: a refresh whose answer does not arrive (ErrRefreshInterrupted)
// cannot be repaired client-side. One Token call never retries a refresh; the
// next call tries the previous refresh token once more, which either works
// (the issuer did not rotate) or fails with ErrReauthenticationRequired, and
// then only a new sign-in helps.
type TokenSource struct {
	mutex     sync.Mutex
	tokens    Tokens
	login     *LoginOptions
	onRefresh func(context.Context, Tokens) error
	// interrupted records that the last refresh got no complete answer, so a
	// following reuse error can name the cause.
	interrupted bool
}

// TokenSourceOptions configure a TokenSource.
type TokenSourceOptions struct {
	// Tokens is the login to keep valid, required to carry a RefreshToken.
	Tokens Tokens
	// OnRefresh, optional, receives every rotated login and should persist it;
	// aikido never writes tokens anywhere.
	OnRefresh func(ctx context.Context, tokens Tokens) error
	// Login configures the refresh endpoint; the zero value uses the defaults.
	Login LoginOptions
}

// NewTokenSource wraps a login. It returns an error if options is nil or the
// login has no RefreshToken, since such a login cannot be kept valid.
func NewTokenSource(options *TokenSourceOptions) (*TokenSource, error) {
	if options == nil || options.Tokens.RefreshToken == "" {
		return nil, errors.New("aikido/codex: token source: Tokens.RefreshToken is required")
	}
	login := options.Login
	return &TokenSource{tokens: options.Tokens, login: &login, onRefresh: options.OnRefresh}, nil
}

// Token returns a login whose access token is valid for at least a few more
// minutes. A refresh that fails for good returns an error wrapping
// ErrReauthenticationRequired; one without a complete answer an error
// wrapping ErrRefreshInterrupted (see TokenSource). If persisting a refreshed login fails the error
// is returned, but the new login is kept in memory, so the next call succeeds
// without refreshing.
func (s *TokenSource) Token(ctx context.Context) (Tokens, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if time.Until(s.tokens.ExpiresAt) > refreshSkew {
		return s.tokens, nil
	}
	// The refresh and its persisting outlive the caller: once OpenAI rotated the
	// refresh token, dropping the answer (a cancelled request) would leave only the
	// used one, and replaying that revokes the login.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	fresh, err := refresh(ctx, s.login, s.tokens.RefreshToken)
	if err != nil {
		return Tokens{}, s.refreshFailed(err)
	}
	s.interrupted = false
	fresh = keepMissingFields(fresh, s.tokens)
	s.tokens = fresh
	if s.onRefresh != nil {
		if err := s.onRefresh(ctx, fresh); err != nil {
			return Tokens{}, fmt.Errorf("aikido/codex: persist refreshed login: %w", err)
		}
	}
	return fresh, nil
}

// refreshFailed records whether err left the refresh outcome unknown and
// names an earlier interruption as the likely cause of a permanent failure.
func (s *TokenSource) refreshFailed(err error) error {
	wasInterrupted := s.interrupted
	s.interrupted = errors.Is(err, ErrRefreshInterrupted)
	if wasInterrupted && errors.Is(err, ErrReauthenticationRequired) {
		return fmt.Errorf("aikido/codex: refresh after an interrupted one: %w", err)
	}
	return err
}

// keepMissingFields fills what a refresh response left out from the previous
// login: the issuer may skip rotating the refresh or ID token.
func keepMissingFields(fresh, previous Tokens) Tokens {
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = previous.RefreshToken
	}
	if fresh.IDToken == "" {
		fresh.IDToken = previous.IDToken
	}
	if fresh.AccountID == "" {
		fresh.AccountID = previous.AccountID
	}
	return fresh
}

// permanentRefreshErrors are the token-endpoint codes after which only a new
// login helps.
var permanentRefreshErrors = []string{"refresh_token_reused", "refresh_token_expired", "refresh_token_invalidated", "invalid_grant"}

// refresh trades refreshToken for a new login at /oauth/token. A failure
// without a complete answer wraps ErrRefreshInterrupted.
func refresh(ctx context.Context, options *LoginOptions, refreshToken string) (Tokens, error) {
	status, body, err := postJSON(ctx, options, "/oauth/token", map[string]string{
		"client_id": ClientID, "grant_type": "refresh_token", "refresh_token": refreshToken,
	})
	if err != nil {
		return Tokens{}, fmt.Errorf("%w: %w", ErrRefreshInterrupted, err)
	}
	if status != http.StatusOK {
		return Tokens{}, refreshError(status, body)
	}
	tokens, err := tokensFromResponse(body)
	if err != nil {
		return Tokens{}, fmt.Errorf("%w: %w: %w", ErrRefreshInterrupted, err, llm.ErrServerError)
	}
	return tokens, nil
}

// refreshError classifies a failed refresh. A 401 or a 4xx naming a permanent
// code wraps ErrReauthenticationRequired; 429 and 5xx never do, since they
// say nothing about the refresh token.
func refreshError(status int, body []byte) error {
	if status >= http.StatusInternalServerError || status == http.StatusTooManyRequests {
		return tokenEndpointError(status)
	}
	if status == http.StatusUnauthorized {
		return ErrReauthenticationRequired
	}
	for _, code := range permanentRefreshErrors {
		if bytes.Contains(body, []byte(code)) {
			return fmt.Errorf("%s: %w", code, ErrReauthenticationRequired)
		}
	}
	return tokenEndpointError(status)
}

func tokenEndpointError(status int) error {
	return fmt.Errorf("aikido/codex: token endpoint: status %d: %w", status, statusSentinel(status))
}

func tokensFromResponse(body []byte) (Tokens, error) {
	var response struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.AccessToken == "" {
		return Tokens{}, errors.New("aikido/codex: token response without access token")
	}
	return TokensFromJWT(response.AccessToken, response.RefreshToken, response.IDToken)
}

func postJSON(ctx context.Context, options *LoginOptions, path string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("aikido/codex: %s: encode request: %w", path, err)
	}
	return post(ctx, options, path, "application/json", raw)
}

// post sends body to the issuer and returns status and response body. A
// transport failure wraps ctx.Err() once ctx ended, llm.ErrServerError
// otherwise.
func post(ctx context.Context, options *LoginOptions, path, contentType string, body []byte) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, options.issuer()+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("aikido/codex: %s: build request: %w", path, err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	response, err := options.client().Do(request)
	if err != nil {
		return 0, nil, transportError(ctx, "aikido/codex: "+path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumLoginResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("aikido/codex: %s: read response: %s: %w", path, err.Error(), llm.ErrServerError)
	}
	return response.StatusCode, raw, nil
}
