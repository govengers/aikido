package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

// fakeJWT builds an unsigned access token with the claims the package reads.
func fakeJWT(t *testing.T, expiresAt time.Time, account string) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"exp":                         expiresAt.Unix(),
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_plan_type": "plus"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(claims) + ".sig"
}

func TestTokenSource_RefreshRotatesOnceAndPersists(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body) // a broken body fails the check below
		if request.URL.Path != "/oauth/token" || body["grant_type"] != "refresh_token" || body["refresh_token"] != "rt.old" || body["client_id"] != ClientID {
			t.Errorf("unexpected refresh %s %v", request.URL.Path, body)
		}
		calls.Add(1)
		time.Sleep(20 * time.Millisecond) // simulated latency, so the other callers pile up on the mutex
		_ = json.NewEncoder(writer).Encode(map[string]string{"access_token": fakeJWT(t, time.Now().Add(240*time.Hour), "acct"), "refresh_token": "rt.new"})
	}))
	defer testServer.Close()

	var persisted []Tokens
	var mutex sync.Mutex
	source := newTokenSource(t, &TokenSourceOptions{
		Tokens: Tokens{AccessToken: "old", RefreshToken: "rt.old", ExpiresAt: time.Now().Add(time.Minute)},
		OnRefresh: func(_ context.Context, tokens Tokens) error {
			mutex.Lock()
			persisted = append(persisted, tokens)
			mutex.Unlock()
			return nil
		},
		Login: LoginOptions{Issuer: testServer.URL},
	})

	var waitGroup sync.WaitGroup
	for range 5 {
		waitGroup.Go(func() {
			tokens, err := source.Token(context.Background())
			if err != nil || tokens.RefreshToken != "rt.new" || tokens.AccountID != "acct" || tokens.PlanType != "plus" {
				t.Errorf("Token = %+v, %v", tokens, err)
			}
		})
	}
	waitGroup.Wait()
	if calls.Load() != 1 || len(persisted) != 1 || persisted[0].RefreshToken != "rt.new" {
		t.Errorf("refreshes = %d, persisted = %d, want 1 each", calls.Load(), len(persisted))
	}
}

func TestTokenSource_PermanentErrorsNeedReauthentication(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		status             int
		body               string
		wantReauthenticate bool
	}{
		{400, `{"error":{"code":"refresh_token_reused"}}`, true},
		{400, `{"error":"invalid_grant"}`, true},
		{401, `{}`, true},
		{503, `refresh_token_reused`, false}, // a 5xx is never taken as permanent
	} {
		testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
			_, _ = io.WriteString(writer, testCase.body)
		}))
		source := newTokenSource(t, &TokenSourceOptions{Tokens: Tokens{RefreshToken: "rt.x"}, Login: LoginOptions{Issuer: testServer.URL}})
		_, err := source.Token(context.Background())
		if errors.Is(err, ErrReauthenticationRequired) != testCase.wantReauthenticate {
			t.Errorf("%d %s: err = %v, reauthenticate want %v", testCase.status, testCase.body, err, testCase.wantReauthenticate)
		}
		testServer.Close()
	}
}

func TestRefreshError(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name               string
		status             int
		body               string
		wantReauthenticate bool
		wantSentinel       error
	}{
		{"unauthorized", http.StatusUnauthorized, "", true, nil},
		{"reused token", http.StatusBadRequest, `{"error":{"code":"refresh_token_reused"}}`, true, nil},
		{"expired token", http.StatusBadRequest, `refresh_token_expired`, true, nil},
		{"invalidated token", http.StatusForbidden, `refresh_token_invalidated`, true, nil},
		{"other 4xx", http.StatusBadRequest, `{"error":"unsupported"}`, false, llm.ErrInvalidRequest},
		{"rate limited with a permanent code", http.StatusTooManyRequests, `invalid_grant`, false, llm.ErrRateLimited},
		{"server error with a permanent code", http.StatusBadGateway, `invalid_grant`, false, llm.ErrServerError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := refreshError(testCase.status, []byte(testCase.body))
			if errors.Is(err, ErrReauthenticationRequired) != testCase.wantReauthenticate {
				t.Errorf("err = %v, reauthenticate want %v", err, testCase.wantReauthenticate)
			}
			if testCase.wantSentinel != nil && !errors.Is(err, testCase.wantSentinel) {
				t.Errorf("err = %v, want %v", err, testCase.wantSentinel)
			}
		})
	}
}

func TestPollInterval(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		raw  string
		want time.Duration
	}{
		{`"5"`, 5 * time.Second},
		{`7`, 7 * time.Second},
		{`"1"`, minimumPollInterval},
		{`"-4"`, minimumPollInterval},
		{`"soon"`, minimumPollInterval},
		{``, minimumPollInterval},
	} {
		if got := pollInterval(json.RawMessage(testCase.raw)); got != testCase.want {
			t.Errorf("pollInterval(%s) = %v, want %v", testCase.raw, got, testCase.want)
		}
	}
}

func TestKeepMissingFields(t *testing.T) {
	t.Parallel()
	previous := Tokens{AccessToken: "old", RefreshToken: "rt.old", IDToken: "id.old", AccountID: "acct.old"}
	if got := keepMissingFields(Tokens{AccessToken: "new"}, previous); got != (Tokens{AccessToken: "new", RefreshToken: "rt.old", IDToken: "id.old", AccountID: "acct.old"}) {
		t.Errorf("empty fields = %+v", got)
	}
	rotated := Tokens{AccessToken: "new", RefreshToken: "rt.new", IDToken: "id.new", AccountID: "acct.new"}
	if got := keepMissingFields(rotated, previous); got != rotated {
		t.Errorf("rotated fields = %+v", got)
	}
}

func TestDeviceLogin_PendingThenSuccess(t *testing.T) {
	t.Parallel()
	var polls atomic.Int32
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_, _ = io.WriteString(writer, `{"device_auth_id":"dev1","user_code":"K7QM-4XPD","interval":"5"}`)
		case "/api/accounts/deviceauth/token":
			if polls.Add(1) < 3 {
				writer.WriteHeader(http.StatusForbidden) // not confirmed yet
				return
			}
			_, _ = io.WriteString(writer, `{"authorization_code":"code1","code_challenge":"challenge","code_verifier":"ver1"}`)
		case "/oauth/token":
			raw, _ := io.ReadAll(request.Body) // a broken body fails the form check below
			form, _ := url.ParseQuery(string(raw))
			if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || form.Get("grant_type") != "authorization_code" ||
				form.Get("code") != "code1" || form.Get("code_verifier") != "ver1" || form.Get("redirect_uri") != deviceRedirectURI {
				t.Errorf("exchange form = %v", form)
			}
			_, _ = fmt.Fprintf(writer, `{"access_token":%q,"refresh_token":"rt.1","id_token":"id"}`, fakeJWT(t, time.Now().Add(time.Hour), "acct"))
		}
	}))
	defer testServer.Close()
	options := &LoginOptions{Issuer: testServer.URL}

	deviceCode, err := StartDeviceLogin(context.Background(), options)
	if err != nil || deviceCode.UserCode != "K7QM-4XPD" || deviceCode.Interval != 5*time.Second || deviceCode.VerificationURL != testServer.URL+"/codex/device" {
		t.Fatalf("StartDeviceLogin = %+v, %v", deviceCode, err)
	}
	deviceCode.Interval = 10 * time.Millisecond
	tokens, err := PollDeviceLogin(context.Background(), options, deviceCode)
	if err != nil || tokens.RefreshToken != "rt.1" || tokens.AccountID != "acct" || polls.Load() != 3 {
		t.Fatalf("PollDeviceLogin = %+v, %v after %d polls", tokens, err, polls.Load())
	}
}

func TestStartDeviceLogin_Failures(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		status    int
		body      string
		wantError error
	}{
		{"disabled", http.StatusNotFound, "", ErrDeviceLoginDisabled},
		{"rate limited", http.StatusTooManyRequests, "", llm.ErrRateLimited},
		{"not json", http.StatusOK, "nope", nil},
		{"incomplete", http.StatusOK, `{"device_auth_id":"dev1"}`, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = io.WriteString(writer, testCase.body)
			}))
			defer testServer.Close()
			_, err := StartDeviceLogin(context.Background(), &LoginOptions{Issuer: testServer.URL})
			if err == nil || (testCase.wantError != nil && !errors.Is(err, testCase.wantError)) {
				t.Errorf("err = %v, want %v", err, testCase.wantError)
			}
		})
	}
}

func TestPollDeviceLogin_StopsOnExpiry(t *testing.T) {
	t.Parallel()
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer testServer.Close()
	expired := DeviceCode{Interval: time.Millisecond, ExpiresAt: time.Now()}
	if _, err := PollDeviceLogin(context.Background(), &LoginOptions{Issuer: testServer.URL}, expired); err == nil {
		t.Error("expired device code: no error")
	}
}

func TestPollDeviceLogin_StopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		cancel() // the caller gives up while the login is pending
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer testServer.Close()
	pending := DeviceCode{Interval: time.Hour, ExpiresAt: time.Now().Add(2 * time.Hour)}
	if _, err := PollDeviceLogin(ctx, &LoginOptions{Issuer: testServer.URL}, pending); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestPost_TransportFailureIsServerError(t *testing.T) {
	t.Parallel()
	testServer := httptest.NewServer(http.NotFoundHandler())
	issuer := testServer.URL
	testServer.Close()
	if _, err := StartDeviceLogin(context.Background(), &LoginOptions{Issuer: issuer}); !errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v, want llm.ErrServerError", err)
	}
}

// Regression: an over-long login answer was cut at the cap and then failed
// as an unrelated decode error.
func TestPost_OversizedAnswerIsAClearError(t *testing.T) {
	t.Parallel()
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"user_code":"ABCD","padding":"`+strings.Repeat("x", maximumLoginResponseBytes)+`"}`)
	}))
	defer testServer.Close()
	_, err := StartDeviceLogin(context.Background(), &LoginOptions{Issuer: testServer.URL})
	if !errors.Is(err, llm.ErrServerError) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want an over-cap llm.ErrServerError", err)
	}
}

func TestParseLogin(t *testing.T) {
	t.Parallel()
	valid, expired := fakeJWT(t, time.Now().Add(time.Hour), ""), fakeJWT(t, time.Now().Add(-time.Hour), "")
	file := func(mode, access string) []byte {
		return fmt.Appendf(nil, `{"auth_mode":%q,"tokens":{"access_token":%q,"refresh_token":"rt.1","account_id":"acct-file"}}`, mode, access)
	}
	for name, testCase := range map[string]struct {
		raw   []byte
		valid bool
	}{
		"chatgpt login":   {file("chatgpt", valid), true},
		"api key mode":    {file("apikey", valid), false},
		"expired":         {file("chatgpt", expired), false},
		"not json":        {[]byte("nope"), false},
		"access not jwt":  {file("chatgpt", "opaque"), false},
		"payload not b64": {file("chatgpt", "a.!!!.c"), false},
	} {
		tokens, err := ParseLogin(testCase.raw)
		if (err == nil) != testCase.valid {
			t.Errorf("%s: err = %v", name, err)
		}
		if testCase.valid && tokens.AccountID != "acct-file" {
			t.Errorf("%s: account id %q, want the file's when the claim is empty", name, tokens.AccountID)
		}
	}
}

// A caller that gives up mid-refresh must not lose the rotated login.
func TestTokenSourceRefreshSurvivesCancel(t *testing.T) {
	t.Parallel()
	arrived, release := make(chan struct{}), make(chan struct{})
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(arrived)
		<-release
		_ = json.NewEncoder(writer).Encode(map[string]string{"access_token": fakeJWT(t, time.Now().Add(time.Hour), "acct"), "refresh_token": "rt.new"})
	}))
	defer testServer.Close()
	var saved Tokens
	persisted := make(chan struct{})
	source := newTokenSource(t, &TokenSourceOptions{
		Tokens: Tokens{AccessToken: "old", RefreshToken: "rt.old", ExpiresAt: time.Now()},
		OnRefresh: func(_ context.Context, tokens Tokens) error {
			saved = tokens
			close(persisted)
			return nil
		},
		Login: LoginOptions{Issuer: testServer.URL},
	})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		_, _ = source.Token(ctx) // the outcome is checked through OnRefresh
	}()
	<-arrived
	cancel()
	close(release)
	select {
	case <-persisted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh was dropped with the cancelled caller")
	}
	<-returned
	if saved.RefreshToken != "rt.new" {
		t.Fatalf("persisted %q, want rt.new", saved.RefreshToken)
	}
}

func TestTokenSource_PersistFailureKeepsTheNewLogin(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(writer).Encode(map[string]string{"access_token": fakeJWT(t, time.Now().Add(time.Hour), "acct")})
	}))
	defer testServer.Close()
	persistError := errors.New("disk full")
	source := newTokenSource(t, &TokenSourceOptions{
		Tokens:    Tokens{AccessToken: "old", RefreshToken: "rt.old", ExpiresAt: time.Now()},
		OnRefresh: func(context.Context, Tokens) error { return persistError },
		Login:     LoginOptions{Issuer: testServer.URL},
	})
	if _, err := source.Token(context.Background()); !errors.Is(err, persistError) {
		t.Fatalf("first Token: err = %v, want the persist error", err)
	}
	tokens, err := source.Token(context.Background())
	if err != nil || tokens.RefreshToken != "rt.old" || calls.Load() != 1 {
		t.Errorf("second Token = %+v, %v after %d refreshes; want the kept login without a refresh", tokens, err, calls.Load())
	}
}

// A refresh whose answer is lost may have rotated the refresh token: the
// client must not replay it inside one call, must say so, and a following
// reuse failure must name the interruption.
func TestTokenSource_InterruptedRefreshIsNotReplayed(t *testing.T) {
	t.Parallel()
	for name, interrupt := range map[string]func(writer http.ResponseWriter){
		"cut response": func(writer http.ResponseWriter) {
			writer.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(writer, `{"access_`) // the server closes the short body
		},
		"200 without tokens": func(writer http.ResponseWriter) {
			_, _ = io.WriteString(writer, `{}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var refreshes atomic.Int32
			tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if refreshes.Add(1) == 1 {
					interrupt(writer)
					return
				}
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":{"code":"refresh_token_reused"}}`)
			}))
			defer tokenServer.Close()
			source := newTokenSource(t, &TokenSourceOptions{
				Tokens: Tokens{AccessToken: "old", RefreshToken: "rt.old", ExpiresAt: time.Now()},
				Login:  LoginOptions{Issuer: tokenServer.URL},
			})
			client, err := NewClient(&Options{Tokens: source, Originator: "casa-mapa", BaseURL: tokenServer.URL})
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.Stream(context.Background(), llm.Request{Model: "gpt-5"})
			if !errors.Is(err, ErrRefreshInterrupted) || !errors.Is(err, llm.ErrServerError) || refreshes.Load() != 1 {
				t.Fatalf("Stream: err = %v after %d refreshes; want ErrRefreshInterrupted after exactly one", err, refreshes.Load())
			}
			_, err = source.Token(context.Background())
			if !errors.Is(err, ErrReauthenticationRequired) || !strings.Contains(err.Error(), "after an interrupted one") {
				t.Fatalf("next Token: err = %v; want ErrReauthenticationRequired naming the interruption", err)
			}
		})
	}
}

func newTokenSource(t *testing.T, options *TokenSourceOptions) *TokenSource {
	t.Helper()
	source, err := NewTokenSource(options)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	return source
}

func TestNewTokenSourceRequiresRefreshToken(t *testing.T) {
	t.Parallel()
	if _, err := NewTokenSource(nil); err == nil {
		t.Error("NewTokenSource(nil) returned no error")
	}
	if _, err := NewTokenSource(&TokenSourceOptions{Tokens: Tokens{AccessToken: "at"}}); err == nil {
		t.Error("NewTokenSource without RefreshToken returned no error")
	}
}
