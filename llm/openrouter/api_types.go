package openrouter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/govengers/aikido/llm"
)

// chatRequest is the OpenAI-compatible chat-completions request body.
//
// `omitempty` is load-bearing: zero-value fields the caller did not set must
// not appear on the wire. Pointer types are used where zero is a valid
// user-set value (notably Temperature: 0 for deterministic decoding).
type chatRequest struct {
	Model              string                 `json:"model"`
	Messages           []apiMessage           `json:"messages"`
	Tools              []apiTool              `json:"tools,omitempty"`
	Stream             bool                   `json:"stream,omitempty"`
	MaximumTokens      int                    `json:"max_tokens,omitempty"`
	Temperature        *float32               `json:"temperature,omitempty"`
	Stop               []string               `json:"stop,omitempty"`
	Reasoning          *apiReasoning          `json:"reasoning,omitempty"`
	Provider           *ProviderPreferences   `json:"provider,omitempty"`
	Modalities         []string               `json:"modalities,omitempty"`
	ImageConfiguration *apiImageConfiguration `json:"image_config,omitempty"`
	Usage              *apiUsageOptions       `json:"usage,omitempty"`
	// ResponseFormat carries structured-output requests (llm.JSONSchema).
	ResponseFormat *apiResponseFormat `json:"response_format,omitempty"`
}

// apiResponseFormat is the OpenAI-compatible structured-output block.
type apiResponseFormat struct {
	Type       string        `json:"type"` // "json_schema"
	JSONSchema apiJSONSchema `json:"json_schema"`
}

type apiJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// apiUsageOptions is OpenRouter's usage-accounting block. With Include set the
// provider returns the credit cost of the call in usage.cost, on the
// non-streaming response and on the final SSE chunk alike. Without it the
// token counts arrive but cost is always absent, which leaves a caller that
// meters money with nothing to meter.
type apiUsageOptions struct {
	Include bool `json:"include"`
}

// apiImageConfiguration mirrors llm.ImageConfiguration on the OpenRouter
// wire. Both fields use omitempty so an unset value leaves the provider
// default per-field.
type apiImageConfiguration struct {
	AspectRatio string `json:"aspect_ratio,omitempty"`
	ImageSize   string `json:"image_size,omitempty"`
}

// apiReasoning is the OpenRouter-normalized thinking-control block.
// OpenRouter accepts `effort` ("low"|"medium"|"high"|...) and forwards an
// equivalent directive to the routed provider. aikido maps
// `*ThinkingConfiguration` onto this; ThinkingByEffort sets `effort`, ThinkingByBudget is forwarded
// here as the OpenAI-style `effort` derived from a coarse budget bucket
// (per ADR-016).
type apiReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// apiMessage is the OpenAI-compatible message shape. Content is RawMessage so
// it can carry either a JSON string (text-only) or a JSON array (multimodal /
// cache_control-tagged content blocks).
type apiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []apiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type apiToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"` // "function"
	Function apiToolCallFunction `json:"function"`
}

type apiToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type apiTool struct {
	Type     string      `json:"type"` // "function"
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type streamChunk struct {
	ID     string `json:"id,omitempty"`
	Object string `json:"object,omitempty"`
	// Provider names the upstream provider that served the generation.
	Provider string         `json:"provider,omitempty"`
	Choices  []streamChoice `json:"choices,omitempty"`
	Usage    *apiUsage      `json:"usage,omitempty"`
	// Top-level error envelope on mid-stream errors.
	Error *apiError `json:"error,omitempty"`
}

type streamChoice struct {
	Index int `json:"index"`
	// Delta carries fragment-style updates for streaming responses.
	Delta streamDelta `json:"delta"`
	// Message carries the fully-assembled assistant message. Image-capable
	// OpenRouter models (e.g. gemini-2.5-flash-image-preview) deliver the
	// generated image in a single SSE chunk under `choices[0].message`
	// instead of `choices[0].delta`.
	Message      *streamDelta `json:"message,omitempty"`
	FinishReason *string      `json:"finish_reason,omitempty"`
}

type streamDelta struct {
	Role      string             `json:"role,omitempty"`
	Content   json.RawMessage    `json:"content,omitempty"`
	ToolCalls []toolCallFragment `json:"tool_calls,omitempty"`
	Reasoning string             `json:"reasoning,omitempty"`
	Images    []apiImagePart     `json:"images,omitempty"`
}

// hasOutput reports whether the delta carries generated output, as opposed
// to a role-only or empty opening chunk. It marks the call's first token.
func (d streamDelta) hasOutput() bool {
	content := string(d.Content)
	hasContent := content != "" && content != `""` && content != "null"
	return hasContent || d.Reasoning != "" || len(d.ToolCalls) > 0 || len(d.Images) > 0
}

// hasOutput reports whether either payload shape of the choice carries
// generated output.
func (c streamChoice) hasOutput() bool {
	return c.Delta.hasOutput() || (c.Message != nil && c.Message.hasOutput())
}

// apiImagePart is the OpenRouter assistant image envelope, present on either
// streaming `delta.images[]` or non-streaming `message.images[]`. Wire shape
// per OpenRouter image-capable models (gemini-2.5-flash-image-preview, gpt-image-1).
type apiImagePart struct {
	Type     string       `json:"type"`
	ImageURL *apiImageURL `json:"image_url,omitempty"`
}

type apiImageURL struct {
	URL string `json:"url"`
}

// apiContentPart is a typed content block. Some image-capable models embed
// generated images inside `message.content` as a typed-parts array rather
// than (or in addition to) the dedicated `images` field.
type apiContentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *apiImageURL `json:"image_url,omitempty"`
}

type toolCallFragment struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function functionFragment `json:"function,omitempty"`
}

type functionFragment struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type apiUsage struct {
	PromptTokens            int               `json:"prompt_tokens"`
	CompletionTokens        int               `json:"completion_tokens"`
	TotalTokens             int               `json:"total_tokens"`
	PromptTokensDetails     promptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails completionDetails `json:"completion_tokens_details,omitempty"`
	Cost                    float64           `json:"cost,omitempty"`
	// Legacy field shapes (pre-2026 cache renaming) — accepted for resilience.
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type promptDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	AudioTokens      int `json:"audio_tokens,omitempty"`
	// Legacy alternates.
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type completionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type apiError struct {
	// `code` may be either a JSON string (provider-defined symbol) or an int
	// (HTTP status mirror). Decode permissively.
	Code     json.RawMessage `json:"code,omitempty"`
	Message  string          `json:"message,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
}

// errorEnvelope is the JSON envelope OpenRouter wraps non-200 responses in.
type errorEnvelope struct {
	Error apiError `json:"error"`
}

// message is the envelope's message, or a generic text when the provider
// sent none.
func (e *apiError) message() string {
	if e.Message == "" {
		return "provider error"
	}
	return e.Message
}

// cause is the sentinel an error envelope on a 200 maps onto: a content
// filter is ErrContentFiltered, anything else ErrServerError.
func (e *apiError) cause() error {
	if isContentFilterErrorEnvelope(e) {
		return llm.ErrContentFiltered
	}
	return llm.ErrServerError
}

// chunkUsage is toLLMUsage plus the provider that served the chunk.
func chunkUsage(chunk *streamChunk) *llm.Usage {
	usage := toLLMUsage(chunk.Usage)
	if usage != nil {
		usage.Provider = chunk.Provider
	}
	return usage
}

// toLLMUsage maps the wire usage onto the locked llm.Usage shape. The cache
// counts take the current field names first and fall back to the legacy ones.
func toLLMUsage(usage *apiUsage) *llm.Usage {
	if usage == nil {
		return nil
	}
	details := usage.PromptTokensDetails
	return &llm.Usage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CacheReadTokens:  firstNonZero(details.CachedTokens, details.CacheReadInputTokens, usage.CacheReadInputTokens),
		CacheWriteTokens: firstNonZero(details.CacheWriteTokens, details.CacheCreationInputTokens, usage.CacheCreationInputTokens),
		CostUSD:          usage.Cost,
	}
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// toAPIImageConfiguration is nil unless the configuration sets a field, so an
// empty configuration leaves image_config off the wire.
func toAPIImageConfiguration(configuration *llm.ImageConfiguration) *apiImageConfiguration {
	if configuration == nil || (configuration.AspectRatio == "" && configuration.ImageSize == "") {
		return nil
	}
	return &apiImageConfiguration{
		AspectRatio: configuration.AspectRatio,
		ImageSize:   configuration.ImageSize,
	}
}

func toAPIResponseFormat(schema *llm.JSONSchema) *apiResponseFormat {
	if schema == nil {
		return nil
	}
	return &apiResponseFormat{
		Type:       "json_schema",
		JSONSchema: apiJSONSchema{Name: schema.Name, Strict: schema.Strict, Schema: schema.Schema},
	}
}

// toAPIReasoning is nil when thinking is off, which omits the reasoning block.
func toAPIReasoning(thinking *llm.ThinkingConfiguration) *apiReasoning {
	effort := effortFromConfiguration(thinking)
	if effort == "" {
		return nil
	}
	return &apiReasoning{Effort: effort}
}

// Thinking budgets (tokens) at or above these bucket onto medium and high
// effort (ADR-016).
const (
	mediumEffortBudget = 2048
	highEffortBudget   = 16384
)

// effortFromConfiguration collapses a *ThinkingConfiguration onto the
// OpenAI-shape effort string OpenRouter accepts. Empty string means "no
// thinking".
//
// For ThinkingByEffort the value passes through verbatim.
//
// For ThinkingByBudget the budget is bucketed onto coarse effort (per
// ADR-016): <2048 low, <16384 medium, >=16384 high. Direct Anthropic
// (v2) honors budget verbatim; here we lose precision, which is fine because
// OpenRouter maps "effort" to an internal budget anyway.
func effortFromConfiguration(thinking *llm.ThinkingConfiguration) string {
	if thinking == nil {
		return ""
	}
	if effort := thinking.Effort(); effort != "" {
		return string(effort)
	}
	budget := thinking.Budget()
	switch {
	case budget <= 0:
		return ""
	case budget < mediumEffortBudget:
		return string(llm.ThinkingEffortLow)
	case budget < highEffortBudget:
		return string(llm.ThinkingEffortMedium)
	default:
		return string(llm.ThinkingEffortHigh)
	}
}

// buildAPIMessages converts public llm.Messages onto the wire shape.
func buildAPIMessages(messages []llm.Message) ([]apiMessage, error) {
	out := make([]apiMessage, 0, len(messages))
	for index, message := range messages {
		content, err := messageContent(message)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		wireMessage := apiMessage{
			Role:      string(message.Role),
			Content:   content,
			ToolCalls: buildAPIToolCalls(message.ToolCalls),
		}
		if message.Role == llm.RoleTool {
			wireMessage.ToolCallID = message.ToolCallID
		}
		out = append(out, wireMessage)
	}
	return out, nil
}

func buildAPIToolCalls(calls []llm.ToolCall) []apiToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]apiToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, apiToolCall{
			ID:       call.ID,
			Type:     "function",
			Function: apiToolCallFunction{Name: call.Name, Arguments: call.Arguments},
		})
	}
	return out
}

// messageContent encodes a message's content in one of three shapes:
//  1. an array of typed parts, for images, audio or a cache breakpoint;
//  2. the empty string "", for an assistant message with only tool calls
//     (per OPENROUTER-DETAILS: more stable than null on the wire);
//  3. a plain string otherwise.
func messageContent(message llm.Message) (json.RawMessage, error) {
	needsParts := len(message.Images) > 0 || len(message.Audio) > 0 || message.Cache != nil
	if needsParts {
		parts, err := buildContentParts(message)
		if err != nil {
			return nil, err
		}
		return marshalContent(parts)
	}
	if message.Content == "" && len(message.ToolCalls) > 0 {
		return json.RawMessage(`""`), nil
	}
	return marshalContent(message.Content)
}

func marshalContent(content any) (json.RawMessage, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("encode content: %w", err)
	}
	return raw, nil
}

// buildContentParts builds the typed-parts array for a message with images,
// audio or a cache breakpoint. The cache_control directive lands on the last part.
// An image without URL is sent inline as a data URI (a generated image kept in
// History has only Data); one with neither fails with llm.ErrInvalidRequest.
func buildContentParts(message llm.Message) ([]map[string]any, error) {
	parts := make([]map[string]any, 0, 1+len(message.Images)+len(message.Audio))
	if message.Content != "" {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": message.Content,
		})
	}
	for index, image := range message.Images {
		url, err := imageURL(image)
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", index, err)
		}
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": url,
			},
		})
	}
	for _, audio := range message.Audio {
		parts = append(parts, map[string]any{
			"type": "input_audio",
			"input_audio": map[string]any{
				"data":   base64.StdEncoding.EncodeToString(audio.Data),
				"format": audio.Format,
			},
		})
	}
	if len(parts) == 0 {
		// A Cache breakpoint on an otherwise-empty message needs a stub text
		// part to live on.
		parts = append(parts, map[string]any{
			"type": "text",
			"text": "",
		})
	}
	if message.Cache != nil {
		cacheControl := map[string]any{"type": "ephemeral"}
		if message.Cache.TTL == llm.CacheTTLOneHour {
			cacheControl["ttl"] = "1h"
		}
		parts[len(parts)-1]["cache_control"] = cacheControl
	}
	return parts, nil
}

// buildAPITools converts tool definitions onto the wire shape.
func buildAPITools(definitions []llm.ToolDefinition) []apiTool {
	if len(definitions) == 0 {
		return nil
	}
	out := make([]apiTool, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, apiTool{
			Type: "function",
			Function: apiFunction{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		})
	}
	return out
}

// imagesRequest is the body of OpenRouter's dedicated images endpoint. It is
// deliberately not chatRequest: the endpoint takes a bare prompt, has no
// message list and no system role, and names the resolution tier
// `resolution` (its `size` field wants WxH pixels and rejects "1K").
type imagesRequest struct {
	Model           string         `json:"model"`
	Prompt          string         `json:"prompt"`
	AspectRatio     string         `json:"aspect_ratio,omitempty"`
	Resolution      string         `json:"resolution,omitempty"`
	Size            string         `json:"size,omitempty"`
	Quality         string         `json:"quality,omitempty"`
	InputReferences []apiImagePart `json:"input_references,omitempty"`
}

// imagesResponse is the images-endpoint success body. A 200 may still carry a
// top-level error envelope, exactly as on chat completions.
type imagesResponse struct {
	Created int64         `json:"created,omitempty"`
	Data    []imagesDatum `json:"data,omitempty"`
	Usage   *apiUsage     `json:"usage,omitempty"`
	Error   *apiError     `json:"error,omitempty"`
}

// imagesDatum is one generated image. Either B64JSON or URL is set;
// MediaType may be absent, in which case the content type is sniffed.
type imagesDatum struct {
	B64JSON   string `json:"b64_json,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// speechRequest is the POST /audio/speech body. The 200 response is raw audio,
// not JSON.
type speechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	Speed          *float32 `json:"speed,omitempty"`
}

// videoRequest is the POST /videos body.
type videoRequest struct {
	Model           string          `json:"model"`
	Prompt          string          `json:"prompt"`
	AspectRatio     string          `json:"aspect_ratio,omitempty"`
	Resolution      string          `json:"resolution,omitempty"`
	Size            string          `json:"size,omitempty"`
	Duration        int             `json:"duration,omitempty"`
	GenerateAudio   *bool           `json:"generate_audio,omitempty"`
	Seed            *int            `json:"seed,omitempty"`
	FrameImages     []apiFrameImage `json:"frame_images,omitempty"`
	InputReferences []apiImagePart  `json:"input_references,omitempty"`
}

// apiFrameImage anchors a clip; FrameType is "first_frame" or "last_frame".
type apiFrameImage struct {
	Type      string       `json:"type"`
	ImageURL  *apiImageURL `json:"image_url"`
	FrameType string       `json:"frame_type"`
}

// videoJob is both the 202 submit answer and GET /videos/{id}. Status walks
// pending, in_progress, completed or failed. Error is a string on some
// providers and an object on others.
type videoJob struct {
	ID           string          `json:"id"`
	Status       string          `json:"status"`
	UnsignedURLs []string        `json:"unsigned_urls"`
	URLs         []string        `json:"urls"`
	Error        json.RawMessage `json:"error"`
	Usage        *struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

// decisionsRequest is the POST /api/alpha/decisions body.
type decisionsRequest struct {
	Model     string                         `json:"model"`
	State     any                            `json:"state"`
	Questions map[string]apiDecisionQuestion `json:"questions"`
	Provider  *ProviderPreferences           `json:"provider,omitempty"`
}

// apiDecisionQuestion's criteria is a map for choice and noul, a list for
// score and absent for box.
type apiDecisionQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// apiStateImage is an image inside a decisions state array.
type apiStateImage struct {
	ImageURL apiImageURL `json:"image_url"`
}

type decisionsResponse struct {
	ID       string                       `json:"id"`
	Model    string                       `json:"model"`
	Provider string                       `json:"provider"`
	Answers  map[string]apiDecisionAnswer `json:"answers"`
	Usage    struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		Cost         float64 `json:"cost"`
	} `json:"usage"`
}

type apiDecisionAnswer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
	Legend        map[string]any     `json:"legend"`
	Confidence    float64            `json:"confidence"`
	Box           *struct {
		Top    float64 `json:"top"`
		Bottom float64 `json:"bottom"`
		Left   float64 `json:"left"`
		Right  float64 `json:"right"`
	} `json:"box"`
}
