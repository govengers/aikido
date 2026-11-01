package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/govengers/aikido/llm"
)

func TestAudioInputOnWire(t *testing.T) {
	t.Parallel()
	clip := []byte("\x1aE\xdf\xa3 webm bytes")
	chatRequest := llm.Request{
		Model: "google/gemini-3.1-flash-lite",
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "transcribe",
			Audio:   []llm.AudioPart{{Data: clip, Format: "webm"}},
		}},
	}
	for _, stream := range []bool{true, false} {
		var captured []byte
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			captured, _ = io.ReadAll(request.Body)
			if stream {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			_, _ = io.WriteString(writer, `{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
		}))
		client := newTestClient(t, server)
		var text string
		var err error
		if stream {
			var response llm.Response
			response, err = llm.Collect(context.Background(), client, chatRequest)
			text = response.Text
		} else {
			var response llm.Response
			response, err = client.Complete(context.Background(), chatRequest)
			text = response.Text
		}
		server.Close()
		if err != nil || text != "hi" {
			t.Fatalf("stream=%v: text %q err %v", stream, text, err)
		}

		var body struct {
			Messages []struct {
				Content []struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					InputAudio *struct {
						Data   string `json:"data"`
						Format string `json:"format"`
					} `json:"input_audio"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(captured, &body); err != nil {
			t.Fatalf("stream=%v: %v: %s", stream, err, captured)
		}
		parts := body.Messages[0].Content
		if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "transcribe" {
			t.Fatalf("stream=%v: parts = %+v", stream, parts)
		}
		audioPart := parts[1]
		if audioPart.Type != "input_audio" || audioPart.InputAudio == nil || audioPart.InputAudio.Format != "webm" || audioPart.InputAudio.Data != base64.StdEncoding.EncodeToString(clip) {
			t.Errorf("stream=%v: audio part = %+v", stream, audioPart)
		}
	}
}
