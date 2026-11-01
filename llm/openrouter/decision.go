package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
)

// Compile-time conformance for the optional decisions capability.
var _ llm.Decider = (*Client)(nil)

// Decide asks a decision model typed questions through OpenRouter's Decisions
// API (POST /api/alpha/decisions; alpha, outside /api/v1). Decision models
// (typesafe/jev-1.14, perplexity/pplx-decider-v1.2, ...) answer only here;
// chat completions rejects them. Retry, error classification and provider
// routing (Options.Provider, WithProvider) are Complete's.
//
// A request without model, state or questions, or with a question its kind
// does not allow, fails with llm.ErrInvalidRequest before any request. A
// question the model left unanswered fails with ErrServerError, returned
// together with the answers that did arrive.
func (c *Client) Decide(ctx context.Context, request llm.DecisionRequest) (llm.DecisionResponse, error) {
	body, err := c.buildDecisionsBody(ctx, request)
	if err != nil {
		return llm.DecisionResponse{}, fmt.Errorf("openrouter: decisions: %w", err)
	}
	start := time.Now()
	raw, _, err := c.do(ctx, httpCall{
		method:     http.MethodPost,
		url:        c.apiRoot() + "/alpha/decisions",
		body:       body,
		accept:     "application/json",
		wantStatus: http.StatusOK,
	})
	if err != nil {
		return llm.DecisionResponse{}, err
	}
	var decoded decisionsResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return llm.DecisionResponse{}, fmt.Errorf("openrouter: decisions: decode response: %w", llm.ErrServerError)
	}
	response := toDecisionResponse(decoded)
	response.Usage.SetTiming(start, time.Time{}, time.Now())
	// A handful of answer tokens over the whole call is no throughput.
	response.Usage.TokensPerSecond = 0
	for name := range request.Questions {
		if _, ok := response.Answers[name]; !ok {
			return response, fmt.Errorf("openrouter: decisions: no answer for question %q: %w", name, llm.ErrServerError)
		}
	}
	return response, nil
}

// apiRoot is the API root above /v1: the Decisions API lives at
// /api/alpha/decisions. A BaseURL without /v1 (tests) is the root itself.
func (c *Client) apiRoot() string {
	return strings.TrimSuffix(c.baseURL, "/v1")
}

// buildDecisionsBody validates the questions per kind and maps them and the
// state's images onto the wire.
func (c *Client) buildDecisionsBody(ctx context.Context, request llm.DecisionRequest) ([]byte, error) {
	if strings.TrimSpace(request.Model) == "" {
		return nil, fmt.Errorf("model is required: %w", llm.ErrInvalidRequest)
	}
	if request.State == nil {
		return nil, fmt.Errorf("state is required: %w", llm.ErrInvalidRequest)
	}
	if len(request.Questions) == 0 {
		return nil, fmt.Errorf("at least one question is required: %w", llm.ErrInvalidRequest)
	}
	body := decisionsRequest{
		Model:     request.Model,
		Questions: make(map[string]apiDecisionQuestion, len(request.Questions)),
		Provider:  c.providerPreferences(ctx),
	}
	for name, question := range request.Questions {
		wireQuestion, err := apiQuestion(question)
		if err != nil {
			return nil, fmt.Errorf("question %q: %w", name, err)
		}
		body.Questions[name] = wireQuestion
	}
	state, err := apiState(request.State)
	if err != nil {
		return nil, err
	}
	body.State = state
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode decisions request: %w", err)
	}
	return encoded, nil
}

// apiQuestion validates a question against its kind: a choice needs two
// options, a noul's options (optional) are exactly "true" and "false", a
// score needs levels, a box takes none.
func apiQuestion(question llm.DecisionQuestion) (apiDecisionQuestion, error) {
	if strings.TrimSpace(question.Instructions) == "" {
		return apiDecisionQuestion{}, fmt.Errorf("instructions are required: %w", llm.ErrInvalidRequest)
	}
	wireQuestion := apiDecisionQuestion{Type: string(question.Kind), Instructions: question.Instructions}
	switch question.Kind {
	case llm.DecisionChoice:
		if len(question.Options) < 2 {
			return wireQuestion, fmt.Errorf("a choice needs at least two options: %w", llm.ErrInvalidRequest)
		}
		wireQuestion.Criteria = question.Options
	case llm.DecisionNoul:
		if len(question.Options) == 0 {
			break
		}
		if !isTrueFalseOptions(question.Options) {
			return wireQuestion, fmt.Errorf(`noul options must be exactly "true" and "false": %w`, llm.ErrInvalidRequest)
		}
		wireQuestion.Criteria = question.Options
	case llm.DecisionScore:
		if len(question.Levels) == 0 {
			return wireQuestion, fmt.Errorf("a score needs levels: %w", llm.ErrInvalidRequest)
		}
		wireQuestion.Criteria = question.Levels
	case llm.DecisionBox:
	default:
		return wireQuestion, fmt.Errorf("unknown kind %q: %w", question.Kind, llm.ErrInvalidRequest)
	}
	return wireQuestion, nil
}

func isTrueFalseOptions(options map[string]string) bool {
	_, hasTrue := options["true"]
	_, hasFalse := options["false"]
	return hasTrue && hasFalse && len(options) == 2
}

// apiState replaces ImagePart elements of a slice state with the wire image
// object; any other state passes through for json.Marshal.
func apiState(state any) (any, error) {
	items, ok := state.([]any)
	if !ok {
		return state, nil
	}
	out := make([]any, len(items))
	for index, item := range items {
		var part *llm.ImagePart
		switch value := item.(type) {
		case llm.ImagePart:
			part = &value
		case *llm.ImagePart:
			part = value
		default:
			out[index] = item
			continue
		}
		partURL, err := imageURL(*part)
		if err != nil {
			return nil, fmt.Errorf("state image %d: %w", index, err)
		}
		out[index] = apiStateImage{ImageURL: apiImageURL{URL: partURL}}
	}
	return out, nil
}

// toDecisionResponse maps the wire answer; its Usage is never nil.
func toDecisionResponse(decoded decisionsResponse) llm.DecisionResponse {
	response := llm.DecisionResponse{
		Answers: make(map[string]llm.DecisionAnswer, len(decoded.Answers)),
		Model:   decoded.Model,
		ID:      decoded.ID,
		Usage: &llm.Usage{
			PromptTokens:     decoded.Usage.InputTokens,
			CompletionTokens: decoded.Usage.OutputTokens,
			CostUSD:          decoded.Usage.Cost,
			Provider:         decoded.Provider,
		},
	}
	for name, answer := range decoded.Answers {
		response.Answers[name] = toDecisionAnswer(answer)
	}
	return response
}

func toDecisionAnswer(answer apiDecisionAnswer) llm.DecisionAnswer {
	result := llm.DecisionAnswer{
		Kind:          llm.DecisionKind(answer.Type),
		Noul:          answer.Noul,
		Choice:        answer.Choice,
		Score:         answer.Score,
		Probabilities: answer.Probabilities,
		Confidence:    answer.Confidence,
	}
	if len(answer.Legend) > 0 {
		result.Legend = make(map[string]string, len(answer.Legend))
		for level, description := range answer.Legend {
			result.Legend[level] = legendText(description)
		}
	}
	if box := answer.Box; box != nil {
		result.Box = &llm.DecisionBoxArea{Top: box.Top, Bottom: box.Bottom, Left: box.Left, Right: box.Right}
	}
	return result
}

// legendText is a legend entry as text: the level string as sent, or the JSON
// of a structured one.
func legendText(description any) string {
	if text, ok := description.(string); ok {
		return text
	}
	// description came out of json.Unmarshal into an any, so marshaling it
	// back cannot fail.
	encoded, _ := json.Marshal(description)
	return string(encoded)
}

// DecisionModels lists the models served by the Decisions API. They are not
// part of the plain GET /models list.
func (c *Client) DecisionModels(ctx context.Context) ([]Model, error) {
	var out struct {
		Data []Model `json:"data"`
	}
	if err := c.getJSON(ctx, "/models?output_modalities=decisions", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
