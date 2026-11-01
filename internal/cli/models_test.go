package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/llm/openrouter"
)

// fakeCatalog is an llm.Client with a fixed model catalogue.
type fakeCatalog struct {
	*llmtest.StubClient
}

func (fakeCatalog) Models(context.Context) ([]openrouter.Model, error) {
	return []openrouter.Model{
		{ID: "anthropic/claude-haiku-4.5", ContextLength: 200000, Pricing: openrouter.Pricing{Prompt: 0.000001, Completion: 0.000005}},
		{ID: "openai/gpt-5", ContextLength: 400000},
	}, nil
}

func (fakeCatalog) DecisionModels(context.Context) ([]openrouter.Model, error) {
	return []openrouter.Model{{ID: "typesafe/jev-1.14"}}, nil
}

func (fakeCatalog) VideoModels(context.Context) ([]openrouter.VideoModel, error) {
	return []openrouter.VideoModel{
		{ID: "google/veo-3.1-lite", SupportedDurations: []int{4, 6, 8}, SupportedResolutions: []string{"720p", "1080p"}, GenerateAudio: new(true)},
		{ID: "bytedance/seedance", SupportedDurations: []int{3, 4, 5, 6}, SupportedSizes: []string{"1280x720"}},
	}, nil
}

func (fakeCatalog) ModelEndpoints(_ context.Context, id string) (openrouter.ModelEndpoints, error) {
	return openrouter.ModelEndpoints{ID: id, Name: "Claude Haiku", Endpoints: []openrouter.Endpoint{
		{ProviderName: "Anthropic", Tag: "anthropic", ContextLength: 200000, UptimeLast30Minutes: new(99.5), LatencyLast30Minutes: &openrouter.Percentiles{P50: 420}, ThroughputLast30Minutes: &openrouter.Percentiles{P50: 88}},
		{ProviderName: "Bedrock", Tag: "amazon-bedrock", Status: -1},
	}}, nil
}

func runModels(t *testing.T, client llm.Client, arguments ...string) string {
	t.Helper()
	app := NewApp(func() (llm.Client, error) { return client, nil })
	var out bytes.Buffer
	app.Writer = &out
	if err := app.Run(context.Background(), append([]string{"aikido", "models"}, arguments...)); err != nil {
		t.Fatalf("models %v: %v", arguments, err)
	}
	return out.String()
}

func TestModelsCommand(t *testing.T) {
	catalog := fakeCatalog{llmtest.NewStubClient()}
	testCases := []struct {
		name      string
		arguments []string
		want      []string
		notWant   []string
	}{
		{"every model", nil, []string{"anthropic/claude-haiku-4.5  200000   1.00    5.00", "openai/gpt-5"}, nil},
		{"filtered", []string{"HAIKU"}, []string{"anthropic/claude-haiku-4.5"}, []string{"openai/gpt-5"}},
		{"decision models", []string{"--decisions"}, []string{"typesafe/jev-1.14"}, []string{"openai/gpt-5"}},
		{"video models", []string{"--video"}, []string{"google/veo-3.1-lite", "4,6,8", "720p,1080p", "true", "bytedance/seedance", "3-6", "1280x720"}, nil},
		{"endpoints", []string{"anthropic/claude-haiku-4.5"}, []string{"anthropic/claude-haiku-4.5 (Claude Haiku)", "99.5%", "420 ms", "88 tok/s", "amazon-bedrock"}, nil},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output := runModels(t, catalog, testCase.arguments...)
			for _, want := range testCase.want {
				if !strings.Contains(output, want) {
					t.Errorf("output lacks %q:\n%s", want, output)
				}
			}
			for _, notWant := range testCase.notWant {
				if strings.Contains(output, notWant) {
					t.Errorf("output has %q:\n%s", notWant, output)
				}
			}
		})
	}
}

func TestModelsCommand_RequiresACatalog(t *testing.T) {
	app := NewApp(func() (llm.Client, error) { return llmtest.NewStubClient(), nil })
	app.Writer = &bytes.Buffer{}
	if err := app.Run(context.Background(), []string{"aikido", "models"}); err == nil || !strings.Contains(err.Error(), "catalogue") {
		t.Errorf("err = %v, want the missing-catalogue error", err)
	}
}

func TestFormatUsage(t *testing.T) {
	usage := &llm.Usage{PromptTokens: 3, CompletionTokens: 4, CostUSD: 0.5, Provider: "Anthropic"}
	if got, want := formatUsage(usage), "[usage] prompt=3 completion=4 cost=$0.500000 provider=Anthropic"; got != want {
		t.Errorf("formatUsage = %q, want %q", got, want)
	}
}
