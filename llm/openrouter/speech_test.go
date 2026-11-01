package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/govengers/aikido/llm"
)

func TestSpeak_RequestAndResponse(t *testing.T) {
	t.Parallel()
	audio := []byte("ID3\x04fake-mp3")
	var (
		gotPath, gotAccept string
		gotBody            map[string]any
		hits               int32
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		// First attempt fails retryably; the second must carry the same body.
		if atomic.AddInt32(&hits, 1) == 1 {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`{"error":{"code":502,"message":"upstream failed"}}`))
			return
		}
		gotPath = request.URL.Path
		gotAccept = request.Header.Get("Accept")
		rawBody, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(rawBody, &gotBody)
		writer.Header().Set("Content-Type", "audio/mpeg")
		writer.Header().Set("X-Generation-Id", "gen-tts-1")
		_, _ = writer.Write(audio)
	})
	client := newTestClient(t, server)

	response, err := client.Speak(context.Background(), llm.SpeechRequest{
		Model:  "elevenlabs/eleven-v4-turbo",
		Input:  "Hallo Welt.",
		Voice:  "sarah",
		Format: "mp3",
		Speed:  new(float32(1.1)),
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if hits != 2 {
		t.Errorf("attempts = %d, want 2", hits)
	}
	if gotPath != "/audio/speech" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAccept == "text/event-stream" {
		t.Errorf("Accept = %q, want audio", gotAccept)
	}
	want := map[string]any{"model": "elevenlabs/eleven-v4-turbo", "input": "Hallo Welt.", "voice": "sarah", "response_format": "mp3"}
	for field, value := range want {
		if gotBody[field] != value {
			t.Errorf("body[%s] = %v, want %v", field, gotBody[field], value)
		}
	}
	if speed, _ := gotBody["speed"].(float64); speed < 1.09 || speed > 1.11 {
		t.Errorf("body[speed] = %v", gotBody["speed"])
	}
	if !bytes.Equal(response.Audio, audio) || response.ContentType != "audio/mpeg" || response.GenerationID != "gen-tts-1" {
		t.Errorf("response = %q %q %q", response.Audio, response.ContentType, response.GenerationID)
	}
}

func TestSpeak_OptionalFieldsOmitted(t *testing.T) {
	t.Parallel()
	var gotBody map[string]any
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(rawBody, &gotBody)
		writer.Header().Set("Content-Type", "audio/pcm")
		_, _ = writer.Write([]byte{0, 1})
	})
	client := newTestClient(t, server)
	if _, err := client.Speak(context.Background(), llm.SpeechRequest{Model: "m", Input: "x"}); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(gotBody) != 2 {
		t.Errorf("body = %v, want only model and input", gotBody)
	}
}

func TestSpeak_Errors(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		request     llm.SpeechRequest
		status      int
		contentType string
		body        string
		want        error
	}{
		{"no input", llm.SpeechRequest{Model: "m"}, 0, "", "", llm.ErrInvalidRequest},
		{"no model", llm.SpeechRequest{Input: "x"}, 0, "", "", llm.ErrInvalidRequest},
		{"bad voice", llm.SpeechRequest{Model: "m", Input: "x", Voice: "nope"}, 400, "application/json", `{"error":{"code":400,"message":"unknown voice"}}`, llm.ErrInvalidRequest},
		{"no credits", llm.SpeechRequest{Model: "m", Input: "x"}, 402, "application/json", `{"error":{"code":402,"message":"insufficient credits"}}`, llm.ErrAuthentication},
		{"json on 200", llm.SpeechRequest{Model: "m", Input: "x"}, 200, "application/json", `{"error":{"code":500,"message":"boom"}}`, llm.ErrServerError},
		{"empty audio", llm.SpeechRequest{Model: "m", Input: "x"}, 200, "audio/mpeg", "", llm.ErrServerError},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var hits int32
			server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				writer.Header().Set("Content-Type", testCase.contentType)
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			})
			client := newTestClient(t, server)
			_, err := client.Speak(context.Background(), testCase.request)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if testCase.status == 0 && hits != 0 {
				t.Errorf("invalid request reached the server")
			}
			if testCase.status >= 400 && hits != 1 {
				t.Errorf("attempts = %d, want 1 (no retry)", hits)
			}
		})
	}
}
