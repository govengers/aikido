package openrouter

import (
	"context"
	"slices"
)

// ProviderPreferences is OpenRouter's provider-routing block
// (https://openrouter.ai/docs/features/provider-routing), sent as `provider`
// on chat completions. Zero fields stay off the wire and leave OpenRouter's
// default (load-balanced across providers, fallbacks on).
type ProviderPreferences struct {
	// Order lists provider slugs to try in order ("anthropic", "azure", ...).
	Order []string `json:"order,omitempty"`
	// AllowFallbacks false pins the request to Order/Only: no other provider
	// is tried when they fail. Nil keeps OpenRouter's default (true).
	AllowFallbacks *bool `json:"allow_fallbacks,omitempty"`
	// Only restricts routing to these provider slugs.
	Only []string `json:"only,omitempty"`
	// Ignore excludes these provider slugs.
	Ignore []string `json:"ignore,omitempty"`
	// Sort ranks the candidates: "price", "throughput" or "latency".
	Sort string `json:"sort,omitempty"`
	// Quantizations restricts to endpoints with these quantizations
	// ("fp8", "bf16", ...).
	Quantizations []string `json:"quantizations,omitempty"`
	// RequireParameters routes only to providers that support every
	// parameter in the request (tools, response_format, ...).
	RequireParameters bool `json:"require_parameters,omitempty"`
	// DataCollection "deny" excludes providers that may store or train on
	// prompts.
	DataCollection string `json:"data_collection,omitempty"`
	// ZeroDataRetention routes only to endpoints with zero data retention:
	// providers that keep neither prompts nor outputs. Stricter than
	// DataCollection "deny".
	ZeroDataRetention bool `json:"zdr,omitempty"`
	// MaximumPrice caps the price in USD per million tokens.
	MaximumPrice *MaximumPrice `json:"max_price,omitempty"`
}

// MaximumPrice is the per-million-token price cap of ProviderPreferences.
// A zero field leaves that side uncapped.
type MaximumPrice struct {
	Prompt     float64 `json:"prompt,omitempty"`
	Completion float64 `json:"completion,omitempty"`
}

type providerKey struct{}

// WithProvider returns a context whose chat calls (Stream, Complete and
// everything built on them: agent runs, voice turns, Transcribe) and Decide
// calls use preferences instead of the client's Options.Provider. The
// preferences replace the client's, they are not merged. Nil preferences fall
// back to the client's. The context keeps a copy: changing preferences
// afterwards does not affect it.
func WithProvider(ctx context.Context, preferences *ProviderPreferences) context.Context {
	return context.WithValue(ctx, providerKey{}, preferences.clone())
}

// clone deep-copies the preferences, so neither a Client nor a context shares
// mutable state with the caller (DES-001). Nil stays nil.
func (p *ProviderPreferences) clone() *ProviderPreferences {
	if p == nil {
		return nil
	}
	clone := *p
	clone.Order = slices.Clone(p.Order)
	clone.Only = slices.Clone(p.Only)
	clone.Ignore = slices.Clone(p.Ignore)
	clone.Quantizations = slices.Clone(p.Quantizations)
	if p.AllowFallbacks != nil {
		clone.AllowFallbacks = new(*p.AllowFallbacks)
	}
	if p.MaximumPrice != nil {
		clone.MaximumPrice = new(*p.MaximumPrice)
	}
	return &clone
}

// providerPreferences resolves the routing block for one call: the context
// override, else the client default.
func (c *Client) providerPreferences(ctx context.Context) *ProviderPreferences {
	if preferences, _ := ctx.Value(providerKey{}).(*ProviderPreferences); preferences != nil {
		return preferences
	}
	return c.provider
}

// preferencesFromOrder is the ProviderOrder shorthand: one entry pins the
// provider (no fallbacks), several form a fallback chain.
func preferencesFromOrder(order []string) *ProviderPreferences {
	if len(order) == 0 {
		return nil
	}
	allowFallbacks := len(order) > 1
	return &ProviderPreferences{Order: append([]string(nil), order...), AllowFallbacks: &allowFallbacks}
}
