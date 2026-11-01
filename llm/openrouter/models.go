package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/govengers/aikido/llm"
)

// Model is one entry of OpenRouter's model catalogue (GET /models).
type Model struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	Description         string       `json:"description"`
	Created             int64        `json:"created"`
	ContextLength       int          `json:"context_length"`
	Architecture        Architecture `json:"architecture"`
	Pricing             Pricing      `json:"pricing"`
	TopProvider         TopProvider  `json:"top_provider"`
	SupportedParameters []string     `json:"supported_parameters"`
}

// Architecture lists a model's modalities and tokenizer.
type Architecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer"`
}

// TopProvider is the limits of the provider OpenRouter routes to by default.
type TopProvider struct {
	ContextLength           int  `json:"context_length"`
	MaximumCompletionTokens int  `json:"max_completion_tokens"`
	IsModerated             bool `json:"is_moderated"`
}

// Pricing is in USD per token (per request / per image for those fields).
type Pricing struct {
	Prompt          USD `json:"prompt"`
	Completion      USD `json:"completion"`
	Request         USD `json:"request"`
	Image           USD `json:"image"`
	InputCacheRead  USD `json:"input_cache_read"`
	InputCacheWrite USD `json:"input_cache_write"`
}

// USD is a price OpenRouter sends as a decimal string ("0.000001"); it also
// accepts a JSON number or null.
type USD float64

// UnmarshalJSON accepts "0.000001", 0.000001, "" and null (both zero). Any
// other text is an error wrapping the strconv.ParseFloat error.
func (u *USD) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*u = 0
		return nil
	}
	price, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("openrouter: price %s: %w", data, err)
	}
	*u = USD(price)
	return nil
}

// PerMillion is the price per million tokens, the unit OpenRouter's UI and
// ProviderPreferences.MaximumPrice use.
func (u USD) PerMillion() float64 { return float64(u) * 1e6 }

// ModelEndpoints is a model with the provider endpoints serving it
// (GET /models/{id}/endpoints).
type ModelEndpoints struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Architecture Architecture `json:"architecture"`
	Endpoints    []Endpoint   `json:"endpoints"`
}

// Endpoint is one provider serving a model, with its live performance.
type Endpoint struct {
	Name         string `json:"name"`
	ProviderName string `json:"provider_name"`
	// Tag is the provider slug for ProviderPreferences ("azure/global").
	Tag                     string   `json:"tag"`
	Quantization            string   `json:"quantization"`
	ContextLength           int      `json:"context_length"`
	MaximumCompletionTokens int      `json:"max_completion_tokens"`
	MaximumPromptTokens     int      `json:"max_prompt_tokens"`
	Pricing                 Pricing  `json:"pricing"`
	SupportedParameters     []string `json:"supported_parameters"`
	// Status is 0 when the endpoint is healthy; negative values mean degraded
	// or down.
	Status int `json:"status"`
	// UptimeLast30Minutes is the success percentage over the last 30 minutes; nil
	// without traffic.
	UptimeLast30Minutes *float64 `json:"uptime_last_30m"`
	// LatencyLast30Minutes is the time to first token in milliseconds; nil without
	// traffic.
	LatencyLast30Minutes *Percentiles `json:"latency_last_30m"`
	// ThroughputLast30Minutes is in tokens per second; nil without traffic. Its
	// percentiles rank from fastest (P50) to slowest (P99).
	ThroughputLast30Minutes *Percentiles `json:"throughput_last_30m"`
}

// Percentiles is a p50/p75/p90/p99 distribution.
type Percentiles struct {
	P50 float64 `json:"p50"`
	P75 float64 `json:"p75"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
}

// Models lists OpenRouter's model catalogue. Errors wrap the llm sentinels
// like Complete's.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var out struct {
		Data []Model `json:"data"`
	}
	if err := c.getJSON(ctx, "/models", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ModelEndpoints lists the providers serving a model ("anthropic/claude-haiku-4.5")
// with their pricing, limits, uptime, latency and throughput. An id that is
// not author/slug fails with llm.ErrInvalidRequest before any request.
func (c *Client) ModelEndpoints(ctx context.Context, id string) (ModelEndpoints, error) {
	author, slug, ok := strings.Cut(id, "/")
	if !ok || author == "" || slug == "" {
		return ModelEndpoints{}, fmt.Errorf("openrouter: model id %q: want author/slug: %w", id, llm.ErrInvalidRequest)
	}
	var out struct {
		Data ModelEndpoints `json:"data"`
	}
	if err := c.getJSON(ctx, "/models/"+author+"/"+slug+"/endpoints", &out); err != nil {
		return ModelEndpoints{}, err
	}
	return out.Data, nil
}

// getJSON GETs path and decodes the JSON body into target.
func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	raw, _, err := c.do(ctx, httpCall{
		method:     http.MethodGet,
		url:        c.baseURL + path,
		accept:     "application/json",
		wantStatus: http.StatusOK,
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("openrouter: decode %s: %w", path, err)
	}
	return nil
}
