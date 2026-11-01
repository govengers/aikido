package openrouter_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
)

// fakeOpenRouter stands in for https://openrouter.ai/api/v1 so the examples
// run offline.
func fakeOpenRouter() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		switch request.Header.Get("Accept") {
		case "text/event-stream":
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\", world.\"},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n")
		default:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello, world."},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":12,"completion_tokens":4,"cost":0.00002}}`)
		}
	}))
}

func ExampleClient_Complete() {
	server := fakeOpenRouter()
	defer server.Close()

	// Production code leaves BaseURL empty for DefaultBaseURL.
	client, err := openrouter.NewClient(&openrouter.Options{APIKey: "sk-or-...", BaseURL: server.URL})
	if err != nil {
		fmt.Println(err)
		return
	}
	response, err := client.Complete(context.Background(), llm.Request{
		Model:    "anthropic/claude-sonnet-4.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Say hello."}},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(response.Text, response.FinishReason, response.Usage.CostUSD)
	// Output: Hello, world. stop 2e-05
}

func ExampleClient_Stream() {
	server := fakeOpenRouter()
	defer server.Close()

	client, err := openrouter.NewClient(&openrouter.Options{APIKey: "sk-or-...", BaseURL: server.URL})
	if err != nil {
		fmt.Println(err)
		return
	}
	events, err := client.Stream(context.Background(), llm.Request{
		Model:    "anthropic/claude-sonnet-4.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Say hello."}},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Drain until the channel closes; EventEnd is always the last event.
	for event := range events {
		switch event.Kind {
		case llm.EventTextDelta:
			fmt.Printf("%q\n", event.Text)
		case llm.EventEnd:
			fmt.Println("end:", event.FinishReason)
		default:
		}
	}
	// Output:
	// "Hello"
	// ", world."
	// end: stop
}

func ExampleAPIError() {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(writer, `{"error":{"code":402,"message":"Insufficient credits"}}`)
	}))
	defer server.Close()

	client, err := openrouter.NewClient(&openrouter.Options{APIKey: "sk-or-...", BaseURL: server.URL})
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = client.Complete(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4.6"})

	var apiError *openrouter.APIError
	if errors.As(err, &apiError) {
		fmt.Println(apiError.StatusCode, apiError.Message)
	}
	fmt.Println(errors.Is(err, llm.ErrAuthentication))
	// Output:
	// 402 Insufficient credits
	// true
}
