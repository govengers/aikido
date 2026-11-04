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

// A client with a dedicated images endpoint implements llm.ImageGenerator;
// other image models answer on Complete with image output modalities (see
// ExampleRequest_imageOutput).
func ExampleImageGenerator() {
	client := llmtest.NewStubClient()
	client.ScriptImages(llmtest.ImageScript{Response: llm.ImageResponse{
		Images: []llm.ImagePart{{ContentType: "image/png", Data: []byte("\x89PNG...")}},
	}})

	var model llm.Client = client
	generator, ok := model.(llm.ImageGenerator)
	if !ok {
		fmt.Println("no images endpoint")
		return
	}
	response, err := generator.GenerateImage(context.Background(), llm.ImageRequest{
		Model:       "openai/gpt-image-2.5-flare",
		Prompt:      "A pixel-art fox in tall grass.",
		AspectRatio: "16:9",
		ImageSize:   "2K",
		Quality:     "high",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(response.Images), response.Images[0].ContentType)
	// Output:
	// 1 image/png
}

// Image output on chat completions, for the models that answer there (the
// Gemini image family): Complete, not Stream, since one base64 image exceeds
// the SSE line cap.
func ExampleRequest_imageOutput() {
	client := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: []byte("\x89PNG...")}},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}})

	response, err := client.Complete(context.Background(), llm.Request{
		Model:              "google/gemini-3.1-flash-image-preview",
		Messages:           []llm.Message{{Role: llm.RoleUser, Content: "A pixel-art fox in tall grass."}},
		Modalities:         []string{"image", "text"},
		ImageConfiguration: &llm.ImageConfiguration{AspectRatio: "16:9", ImageSize: "2K"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(response.Images), response.Images[0].ContentType)
	// Output:
	// 1 image/png
}

func ExampleSpeechGenerator() {
	client := llmtest.NewStubClient()
	client.ScriptSpeech(llmtest.SpeechScript{Response: llm.SpeechResponse{
		Audio: []byte("ID3..."), ContentType: "audio/mpeg", GenerationID: "gen-1",
	}})

	var model llm.Client = client
	speaker, ok := model.(llm.SpeechGenerator)
	if !ok {
		fmt.Println("no speech endpoint")
		return
	}
	response, err := speaker.Speak(context.Background(), llm.SpeechRequest{
		Model:  "elevenlabs/eleven-v4-turbo",
		Input:  "The shower goes by the window.",
		Voice:  "sarah",
		Format: "mp3",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(response.ContentType, response.GenerationID)
	// Output:
	// audio/mpeg gen-1
}

func ExampleVideoGenerator() {
	client := llmtest.NewStubClient()
	client.ScriptVideos(llmtest.VideoScript{Response: llm.VideoResponse{
		Video: []byte("....ftypmp42"), ContentType: "video/mp4", JobID: "job-1",
		Usage: &llm.Usage{CostUSD: 0.12},
	}})

	var model llm.Client = client
	generator, ok := model.(llm.VideoGenerator)
	if !ok {
		fmt.Println("no video endpoint")
		return
	}
	// The call blocks until the clip is downloaded, minutes on a real
	// provider: bound it with the context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	withoutAudio := false
	response, err := generator.GenerateVideo(ctx, llm.VideoRequest{
		Model:           "google/veo-3.1-lite",
		Prompt:          "A paper boat drifts across a calm pond at sunrise, static camera.",
		AspectRatio:     "16:9",
		Resolution:      "720p",
		DurationSeconds: 4,
		GenerateAudio:   &withoutAudio,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(response.ContentType, response.JobID, response.Usage.CostUSD)
	// Output:
	// video/mp4 job-1 0.12
}

// countingObserver is the smallest Observer: it counts the calls that finished.
type countingObserver struct{ finished int }

func (o *countingObserver) CallStarted(ctx context.Context, _ *llm.Call) context.Context { return ctx }

func (o *countingObserver) CallFinished(context.Context, *llm.Call, *llm.CallOutcome) { o.finished++ }

// Wrap the chat client once in main, with the adapters you need (llm/prometheusadapter,
// llm/oteladapter, llm/wideeventadapter), and hand the wrapped client to agent.Session.
func ExampleNewObservedClient() {
	observer := &countingObserver{}
	client, err := llm.NewObservedClient(&llm.ObservedClientOptions{
		Client:    llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventTextDelta, Text: "hi"}, {Kind: llm.EventEnd}}}),
		Name:      "openrouter",
		Observers: []llm.Observer{observer},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	if _, err := llm.Collect(context.Background(), client, llm.Request{Model: "openai/gpt-5.4"}); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(observer.finished)
	// Output: 1
}
