package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/govengers/aikido/llm"
)

// Compile-time conformance for the optional speech capability.
var _ llm.SpeechGenerator = (*Client)(nil)

// Speak synthesizes request.Input through OpenRouter's text-to-speech endpoint
// (POST /api/v1/audio/speech, OpenAI audio/speech compatible). The 200 body
// is the raw audio; it is read in full, there is no chunked variant.
//
// An empty Model or Input fails with llm.ErrInvalidRequest before any
// request. Retry and error classification are Complete's: 429 and 5xx retry
// per Options.MaximumAttempts, auth and bad-request short-circuit. A 200 that
// carries a JSON error envelope instead of audio, or no audio at all,
// surfaces as ErrServerError.
func (c *Client) Speak(ctx context.Context, request llm.SpeechRequest) (llm.SpeechResponse, error) {
	if strings.TrimSpace(request.Model) == "" {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: model is required: %w", llm.ErrInvalidRequest)
	}
	if strings.TrimSpace(request.Input) == "" {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: input is required: %w", llm.ErrInvalidRequest)
	}
	body, err := json.Marshal(speechRequest{
		Model:          request.Model,
		Input:          request.Input,
		Voice:          request.Voice,
		ResponseFormat: request.Format,
		Speed:          request.Speed,
	})
	if err != nil {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: %w", err)
	}
	audio, header, err := c.do(ctx, httpCall{
		method:     http.MethodPost,
		url:        c.baseURL + "/audio/speech",
		body:       body,
		accept:     "audio/*, application/json",
		wantStatus: http.StatusOK,
	})
	if err != nil {
		return llm.SpeechResponse{}, err
	}
	contentType := header.Get("Content-Type")
	if mediaType, _, _ := mime.ParseMediaType(contentType); mediaType == "application/json" {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: %s: %w", jsonInsteadOfAudioMessage(audio), llm.ErrServerError)
	}
	if len(audio) == 0 {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: empty audio: %w", llm.ErrServerError)
	}
	return llm.SpeechResponse{
		Audio:        audio,
		ContentType:  contentType,
		GenerationID: header.Get("X-Generation-Id"),
	}, nil
}

func jsonInsteadOfAudioMessage(body []byte) string {
	if envelope := decodeErrorEnvelope(body); envelope != nil {
		return envelope.Error.Message
	}
	return "response is JSON, not audio"
}
