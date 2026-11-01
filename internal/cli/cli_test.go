package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func stubFactory(client llm.Client) ClientFactory {
	return func() (llm.Client, error) { return client, nil }
}

func TestNewApp_VersionFlag(t *testing.T) {
	app := NewApp(func() (llm.Client, error) { return nil, errors.New("factory must not run for --version") })
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	if err := app.Run(context.Background(), []string{"aikido", "--version"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), version()) {
		t.Errorf("version output missing %q: %q", version(), out.String())
	}
}

func TestVersionFromBuildInfo(t *testing.T) {
	revision := []debug.BuildSetting{{Key: "vcs.revision", Value: "b25d1832e0c5f6a7b8c9d0e1f2a3b4c5d6e7f8a9"}}
	testCases := []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"go install at a tag", debug.BuildInfo{Main: debug.Module{Version: "v0.6.1"}}, "v0.6.1"},
		{"checkout pseudo-version", debug.BuildInfo{Main: debug.Module{Version: "v0.6.1-0.20261011120000-b25d1832e0c5+dirty"}}, "v0.6.1-0.20261011120000-b25d1832e0c5+dirty"},
		{"devel with revision", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: revision}, "(devel) b25d1832e0c5"},
		{"devel dirty", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: append(revision, debug.BuildSetting{Key: "vcs.modified", Value: "true"})}, "(devel) b25d1832e0c5+dirty"},
		{"devel without vcs", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "(devel)"},
		{"empty", debug.BuildInfo{}, "(devel)"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := versionFromBuildInfo(&testCase.info); got != testCase.want {
				t.Errorf("got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNewApp_RoutesChat(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "ok"},
		{Kind: llm.EventEnd},
	}})
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	if err := app.Run(context.Background(), []string{"aikido", "chat", "--model", "m1", "hello"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("chat output missing reply: %q", out.String())
	}
	if requests := stub.Requests(); len(requests) != 1 || requests[0].Model != "m1" {
		t.Errorf("model flag not propagated: %+v", requests)
	}
}

func TestNewApp_ChatWithoutPromptFails(t *testing.T) {
	stub := llmtest.NewStubClient()
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	err := app.Run(context.Background(), []string{"aikido", "chat"})
	if err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestNewApp_RoutesAgent(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "fine"},
		{Kind: llm.EventEnd},
	}})
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	if err := app.Run(context.Background(), []string{"aikido", "agent", "--max-turns", "3", "do thing"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "[end] stop") {
		t.Errorf("agent output missing end: %q", out.String())
	}
}

func TestNewApp_FactoryError(t *testing.T) {
	sentinel := errors.New("no key")
	app := NewApp(func() (llm.Client, error) { return nil, sentinel })
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	err := app.Run(context.Background(), []string{"aikido", "chat", "hi"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want %v", err, sentinel)
	}
}

func TestNewOpenRouterClient(t *testing.T) {
	unsetForTest(t, "OPENROUTER_API_KEY")
	if _, err := NewOpenRouterClient(); err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("err = %v, want the missing-key error", err)
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	if client, err := NewOpenRouterClient(); err != nil || client == nil {
		t.Errorf("NewOpenRouterClient = %v, %v; want a client", client, err)
	}
}

func TestNewOpenRouterClient_BaseURLFromEnvironment(t *testing.T) {
	requestedPaths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestedPaths <- request.URL.Path
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("OPENROUTER_BASE_URL", server.URL+"/api/v1")

	client, err := NewOpenRouterClient()
	if err != nil {
		t.Fatalf("NewOpenRouterClient: %v", err)
	}
	_, err = client.Complete(t.Context(), llm.Request{Model: "test/model", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})

	if !errors.Is(err, llm.ErrAuthentication) {
		t.Errorf("err = %v; want the test server's 401", err)
	}
	select {
	case requestedPath := <-requestedPaths:
		if requestedPath != "/api/v1/chat/completions" {
			t.Errorf("request path = %q; want /api/v1/chat/completions", requestedPath)
		}
	default:
		t.Error("the test server got no request")
	}
}

func TestResolveStandardStreams(t *testing.T) {
	if resolveWriter(nil) != os.Stdout || resolveErrorWriter(nil) != os.Stderr || resolveReader(nil) != os.Stdin {
		t.Error("a nil command must fall back to the process's standard streams")
	}
}
