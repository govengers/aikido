package llmtest_test

import (
	"context"
	"fmt"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func ExampleStubClient() {
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "call-1", Name: "weather", Arguments: `{"city":"Stuttgart"}`}},
			{Kind: llm.EventEnd, FinishReason: "tool_calls"},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "It is sunny in Stuttgart."},
			{Kind: llm.EventEnd, FinishReason: "stop"},
		}},
	)

	first, _ := llm.Collect(context.Background(), stub, llm.Request{Model: "m"})
	second, _ := llm.Collect(context.Background(), stub, llm.Request{Model: "m"})
	_, err := llm.Collect(context.Background(), stub, llm.Request{Model: "m"})

	fmt.Println(first.ToolCalls[0].Name, first.FinishReason)
	fmt.Println(second.Text)
	fmt.Println(err)
	fmt.Println(len(stub.Requests()), "requests recorded")
	// Output:
	// weather tool_calls
	// It is sunny in Stuttgart.
	// aikido/llmtest: stub client: script exhausted
	// 2 requests recorded
}
