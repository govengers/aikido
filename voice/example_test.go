package voice_test

import (
	"context"
	"fmt"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/voice"
)

// echoSpeech stands in for a text-to-speech provider such as OpenRouter.
type echoSpeech struct{}

func (echoSpeech) Speak(_ context.Context, request llm.SpeechRequest) (llm.SpeechResponse, error) {
	return llm.SpeechResponse{Audio: []byte(request.Input), ContentType: "audio/mpeg"}, nil
}

func ExamplePipeline_Run() {
	// A scripted model stands in for a real llm.Client.
	model := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "The shower fits by the window. "},
		{Kind: llm.EventTextDelta, Text: "The basin goes opposite."},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}})
	session, err := agent.NewSession(&agent.SessionOptions{
		ID: "kitchen-42", Client: model, History: memory.NewHistory(), Locker: agent.NewLocalLocker(), Model: "any/model",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	pipeline, err := voice.New(&voice.Options{Speech: echoSpeech{}, SpokenStyle: voice.DefaultSpokenStyle})
	if err != nil {
		fmt.Println(err)
		return
	}
	events, err := pipeline.Run(context.Background(), session, voice.Input{Text: "Where does the shower go?", Output: voice.OutputSpeech})
	if err != nil {
		fmt.Println(err)
		return
	}
	for event := range events {
		switch event.Kind {
		case voice.EventSpeech:
			fmt.Printf("chunk %d: %s\n", event.Speech.Sequence, event.Speech.Text)
		case voice.EventDone:
			fmt.Println("done:", event.EndReason)
		default:
			// text deltas, transcript, tool and error events
		}
	}
	// Output:
	// chunk 0: The shower fits by the window.
	// chunk 1: The basin goes opposite.
	// done: stop
}
