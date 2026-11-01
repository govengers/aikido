package llm_test

import (
	"context"
	"fmt"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/retry"
)

func ExampleCollect() {
	client := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventThinking, Text: "The user greets me."},
		{Kind: llm.EventTextDelta, Text: "Hello, "},
		{Kind: llm.EventTextDelta, Text: "world."},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 12, CompletionTokens: 3}},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}})

	response, err := llm.Collect(context.Background(), client, llm.Request{
		Model:    "anthropic/claude-sonnet-4.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Say hello."}},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(response.Text)
	fmt.Println(response.FinishReason, response.Usage.CompletionTokens)
	// Output:
	// Hello, world.
	// stop 3
}

func ExampleCollectWithRetry() {
	client := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrServerError}}},
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventTextDelta, Text: "second time lucky"}, {Kind: llm.EventEnd}}},
	)
	policy := llm.DefaultStreamingRetryPolicy()
	policy.BaseDelay = time.Millisecond

	response, err := llm.CollectWithRetry(context.Background(), client, llm.Request{Model: "m"}, policy)
	fmt.Println(response.Text, err, len(client.Requests()))
	// Output:
	// second time lucky <nil> 2
}

func ExampleCompleteWithRetry() {
	client := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrContentFiltered}}},
	)
	policy := retry.Policy{MaximumAttempts: 3, BaseDelay: time.Millisecond}

	// ShouldRetry is nil, so only transient server errors are retried: a content filter is final.
	_, err := llm.CompleteWithRetry(context.Background(), client, llm.Request{Model: "m"}, policy)
	fmt.Println(llm.IsContentFilter(err), llm.IsTransientServerError(err), len(client.Requests()))
	// Output:
	// true false 1
}

func ExampleTranscribe() {
	client := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "Ich nehme den wasch tisch."},
		{Kind: llm.EventEnd},
	}})

	text, _, err := llm.Transcribe(context.Background(), client, llm.TranscribeRequest{
		Audio:      []byte("RIFF...."),
		Format:     "wav",
		Hint:       "The speaker speaks German.",
		Vocabulary: &llm.Vocabulary{Terms: []string{"Waschtisch"}},
	})
	fmt.Println(text, err)
	// Output:
	// Ich nehme den Waschtisch. <nil>
}

func ExampleVocabulary_Correct() {
	vocabulary := &llm.Vocabulary{
		Terms:        []string{"LIV60G", "Duschtasse"},
		Replacements: map[string]string{"pfeifer und mai": "PFEIFFER & MAY"},
		Fuzzy:        true,
	}
	fmt.Println(vocabulary.Correct("Die Duschtase LIV 60 g von Pfeifer und Mai."))
	// Output:
	// Die Duschtasse LIV60G von PFEIFFER & MAY.
}

func ExampleDecider() {
	client := llmtest.NewStubClient()
	client.ScriptDecisions(llmtest.DecisionScript{Response: llm.DecisionResponse{
		Answers: map[string]llm.DecisionAnswer{"spam": {Kind: llm.DecisionNoul, Noul: 0.97}},
	}})

	var model llm.Client = client
	decider, ok := model.(llm.Decider)
	if !ok {
		fmt.Println("no decisions endpoint")
		return
	}
	response, err := decider.Decide(context.Background(), llm.DecisionRequest{
		Model:     "typesafe/jev-1.14",
		State:     "WIN A FREE CRUISE!!! Click here.",
		Questions: map[string]llm.DecisionQuestion{"spam": llm.NoulQuestion("Is this message spam?")},
	})
	fmt.Println(response.Answers["spam"].Noul > 0.5, err)
	// Output:
	// true <nil>
}
