package codex_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/codex"
)

func ExampleNewClient() {
	// A stand-in for the Codex backend; leave BaseURL empty in real use.
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello!\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer backend.Close()

	// The login comes from StartDeviceLogin + PollDeviceLogin or ParseLogin.
	tokens, err := codex.NewTokenSource(&codex.TokenSourceOptions{
		Tokens: codex.Tokens{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)},
		OnRefresh: func(context.Context, codex.Tokens) error {
			return nil // persist the rotated login here
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	client, err := codex.NewClient(&codex.Options{Tokens: tokens, Originator: "my-app", BaseURL: backend.URL})
	if err != nil {
		fmt.Println(err)
		return
	}
	response, err := client.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Say hello."}},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(response.Text, response.FinishReason)
	// Output: Hello! stop
}
