package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/govengers/aikido/llm"
)

// TestMaximumAttempts pins Options.MaximumAttempts on every call path: a server that
// always answers 503 must see exactly the configured number of requests.
func TestMaximumAttempts(t *testing.T) {
	t.Parallel()
	paths := map[string]func(context.Context, *Client) error{
		"stream": func(ctx context.Context, client *Client) error {
			_, err := client.Stream(ctx, llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
			return err
		},
		"complete": func(ctx context.Context, client *Client) error {
			_, err := client.Complete(ctx, llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
			return err
		},
		"transcribe": func(ctx context.Context, client *Client) error {
			_, _, err := llm.Transcribe(ctx, client, llm.TranscribeRequest{Audio: []byte("x"), Format: "wav"})
			return err
		},
		"images": func(ctx context.Context, client *Client) error {
			_, err := client.GenerateImage(ctx, llm.ImageRequest{Model: "m", Prompt: "p"})
			return err
		},
		"speech": func(ctx context.Context, client *Client) error {
			_, err := client.Speak(ctx, llm.SpeechRequest{Model: "m", Input: "hi", Voice: "v"})
			return err
		},
	}
	for name, call := range paths {
		for _, testCase := range []struct{ maximumAttempts, want int }{{1, 1}, {2, 2}, {0, 3}} {
			t.Run(fmt.Sprintf("%s/maximum_attempts_%d", name, testCase.maximumAttempts), func(t *testing.T) {
				t.Parallel()
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					http.Error(writer, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
				}))
				defer server.Close()
				client, err := NewClient(&Options{APIKey: "k", BaseURL: server.URL, MaximumAttempts: testCase.maximumAttempts})
				if err != nil {
					t.Fatal(err)
				}
				if err := call(context.Background(), client); !errors.Is(err, llm.ErrServerError) {
					t.Fatalf("err = %v, want ErrServerError", err)
				}
				if got := int(hits.Load()); got != testCase.want {
					t.Fatalf("MaximumAttempts=%d: %d requests, want %d", testCase.maximumAttempts, got, testCase.want)
				}
			})
		}
	}
}
