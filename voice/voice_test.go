package voice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

// fakeSpeech synthesizes "audio:<text>" and can delay or fail per input.
type fakeSpeech struct {
	mutex    sync.Mutex
	requests []llm.SpeechRequest
	delay    func(text string) time.Duration
	fail     func(text string) bool
}

func (f *fakeSpeech) Speak(ctx context.Context, request llm.SpeechRequest) (llm.SpeechResponse, error) {
	f.mutex.Lock()
	f.requests = append(f.requests, request)
	f.mutex.Unlock()
	if f.delay != nil {
		select {
		case <-time.After(f.delay(request.Input)):
		case <-ctx.Done():
			return llm.SpeechResponse{}, ctx.Err()
		}
	}
	if f.fail != nil && f.fail(request.Input) {
		return llm.SpeechResponse{}, fmt.Errorf("tts down: %w", llm.ErrServerError)
	}
	return llm.SpeechResponse{Audio: []byte("audio:" + request.Input), ContentType: "audio/mpeg", GenerationID: "gen"}, nil
}

// replyTurn streams text in the given deltas and reports a priced usage.
func replyTurn(cost float64, deltas ...string) llmtest.TurnScript {
	var events []llm.Event
	for _, delta := range deltas {
		events = append(events, llm.Event{Kind: llm.EventTextDelta, Text: delta})
	}
	events = append(events,
		llm.Event{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, CostUSD: cost}},
		llm.Event{Kind: llm.EventEnd, FinishReason: "stop"})
	return llmtest.TurnScript{Events: events}
}

func transcriptTurn(text string) llmtest.TurnScript {
	return llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: text},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 100, CostUSD: 0.0001}},
		{Kind: llm.EventEnd},
	}}
}

func newSession(t *testing.T, client llm.Client) (*agent.Session, *memory.History) {
	t.Helper()
	history := memory.New()
	session, err := agent.NewSession(&agent.SessionOptions{
		ID: "voice-test", Client: client, History: history, Locker: agent.NewLocalLocker(),
		Model: "test/model", SystemPrompt: "You are a bathroom planner.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return session, history
}

func collect(t *testing.T, stream <-chan Event) []Event {
	t.Helper()
	var events []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, open := <-stream:
			if !open {
				return events
			}
			events = append(events, event)
		case <-timeout:
			t.Fatal("event stream did not close")
		}
	}
}

func kinds(events []Event) string {
	var names []string
	for _, event := range events {
		names = append(names, string(event.Kind))
	}
	return strings.Join(names, ",")
}

func only(events []Event, kind EventKind) []Event {
	var matching []Event
	for _, event := range events {
		if event.Kind == kind {
			matching = append(matching, event)
		}
	}
	return matching
}

const firstAnswer, secondAnswer = "Die Dusche passt an die Fensterwand. ", "Der Waschtisch kommt gegenüber."

func newPipeline(t *testing.T, options *Options) *Pipeline {
	t.Helper()
	pipeline, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline
}

// runTurn runs input to the end under t.Context().
func runTurn(t *testing.T, pipeline *Pipeline, runner Runner, input Input) []Event {
	t.Helper()
	stream, err := pipeline.Run(t.Context(), runner, input)
	if err != nil {
		t.Fatal(err)
	}
	return collect(t, stream)
}

func lastDone(t *testing.T, events []Event) Event {
	t.Helper()
	if count := len(only(events, EventDone)); count != 1 {
		t.Fatalf("done events = %d (%s)", count, kinds(events))
	}
	last := events[len(events)-1]
	if last.Kind != EventDone || last.Usage == nil {
		t.Fatalf("last event = %+v (%s)", last, kinds(events))
	}
	return last
}

type modeCase struct {
	name       string
	audio      bool
	output     Output
	transcript bool
	speech     int
}

func TestRun_FourModes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []modeCase{
		{"text in, text out", false, OutputText, false, 0},
		{"text in, voice out", false, OutputSpeech, false, 2},
		{"voice in, text out", true, OutputText, true, 0},
		{"voice in, voice out", true, OutputSpeech, true, 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transcriber := llmtest.NewStubClient(transcriptTurn("Wo kommt die Dusche hin?"))
			chat := llmtest.NewStubClient(replyTurn(0.002, "Die Dusche passt ", "an die Fensterwand. Der Wasch", "tisch kommt gegenüber."))
			pipeline := newPipeline(t, &Options{Transcriber: transcriber, Speech: &fakeSpeech{}, SpokenStyle: DefaultSpokenStyle, SpeechUSDPerCharacter: 0.00002})
			session, history := newSession(t, chat)
			input := Input{Text: "Wo kommt die Dusche hin?", Output: testCase.output}
			if testCase.audio {
				input = Input{Audio: []byte("webm"), ContentType: "audio/webm;codecs=opus", Language: "de", Output: testCase.output}
			}
			events := runTurn(t, pipeline, session, input)

			checkTranscriptFirst(t, testCase, events)
			checkReply(t, testCase, events)
			checkPrompts(t, testCase, chat, transcriber)
			messages, err := history.Read(context.Background(), "voice-test")
			if err != nil || len(messages) != 2 || messages[0].Content != "Wo kommt die Dusche hin?" {
				t.Errorf("history = %+v, %v", messages, err)
			}
		})
	}
}

func checkTranscriptFirst(t *testing.T, testCase modeCase, events []Event) {
	t.Helper()
	if got := len(only(events, EventTranscript)) == 1; got != testCase.transcript {
		t.Errorf("transcript event = %v, want %v (%s)", got, testCase.transcript, kinds(events))
	}
	if testCase.transcript && events[0].Kind != EventTranscript {
		t.Errorf("transcript must come first: %s", kinds(events))
	}
}

func checkReply(t *testing.T, testCase modeCase, events []Event) {
	t.Helper()
	var text strings.Builder
	for _, event := range only(events, EventText) {
		text.WriteString(event.Text)
	}
	if text.String() != firstAnswer+secondAnswer {
		t.Errorf("text = %q", text.String())
	}
	speech := only(events, EventSpeech)
	if len(speech) != testCase.speech {
		t.Fatalf("speech chunks = %d, want %d (%s)", len(speech), testCase.speech, kinds(events))
	}
	if testCase.speech > 0 && (speech[0].Speech.Text != strings.TrimSpace(firstAnswer) || speech[1].Speech.Text != secondAnswer) {
		t.Errorf("chunks = %q, %q", speech[0].Speech.Text, speech[1].Speech.Text)
	}
	if last := events[len(events)-1]; last.Kind != EventDone || last.EndReason != agent.EndReasonStop {
		t.Fatalf("last = %+v", last)
	}
}

func checkPrompts(t *testing.T, testCase modeCase, chat, transcriber *llmtest.StubClient) {
	t.Helper()
	system := chat.Requests()[0].Messages[0].Content
	if spoken := strings.Contains(system, "read aloud"); spoken != (testCase.output == OutputSpeech) {
		t.Errorf("spoken style in system prompt = %v for %s", spoken, testCase.output)
	}
	if !testCase.audio {
		return
	}
	if prompt := transcriber.Requests()[0].Messages[0].Content; !strings.Contains(prompt, "Expected language: de.") {
		t.Errorf("language hint missing: %s", prompt)
	}
}

func TestRun_UsageAggregatesAllStages(t *testing.T) {
	t.Parallel()
	transcriber := llmtest.NewStubClient(transcriptTurn("Hallo."))
	chat := llmtest.NewStubClient(replyTurn(0.002, firstAnswer+secondAnswer))
	pipeline := newPipeline(t, &Options{Transcriber: transcriber, Speech: &fakeSpeech{}, SpeechUSDPerCharacter: 0.00002})
	session, _ := newSession(t, chat)
	events := runTurn(t, pipeline, session, Input{Audio: []byte("x"), ContentType: "audio/wav", Output: OutputSpeech})
	usage := events[len(events)-1].Usage
	characters := len([]rune(strings.TrimSpace(firstAnswer))) + len([]rune(secondAnswer))
	if usage == nil || usage.SpeechCharacters != characters || usage.LLM.PromptTokens != 10 || usage.Transcription == nil || usage.Transcription.PromptTokens != 100 {
		t.Fatalf("usage = %+v", usage)
	}
	want := 0.0001 + 0.002 + float64(characters)*0.00002
	if math.Abs(usage.CostUSD-want) > 1e-12 || math.Abs(usage.SpeechCostUSD-float64(characters)*0.00002) > 1e-12 {
		t.Errorf("cost = %v (speech %v), want %v", usage.CostUSD, usage.SpeechCostUSD, want)
	}
}

func TestRun_SpeechChunksKeepOrderWhenSynthesisIsNot(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(replyTurn(0, "Erster Satz ist lang. ", "Zweiter Satz ist kurz. ", "Dritter Satz ganz am Ende."))
	speech := &fakeSpeech{delay: func(text string) time.Duration {
		if strings.HasPrefix(text, "Erster") {
			return 150 * time.Millisecond
		}
		return 0
	}}
	pipeline := newPipeline(t, &Options{Speech: speech, SpeechConcurrency: 3})
	session, _ := newSession(t, chat)
	chunks := only(runTurn(t, pipeline, session, Input{Text: "Hallo", Output: OutputSpeech}), EventSpeech)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for index, event := range chunks {
		if event.Speech.Sequence != index || string(event.Speech.Audio) != "audio:"+event.Speech.Text {
			t.Errorf("chunk %d = %+v", index, event.Speech)
		}
	}
	if !strings.HasPrefix(chunks[0].Speech.Text, "Erster") {
		t.Errorf("first chunk = %q", chunks[0].Speech.Text)
	}
}

func TestRun_SpeechStartsBeforeTheReplyEnds(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	// The agent blocks after the first sentence until the test has seen its audio.
	chat := &gatedClient{first: "Die Dusche passt an die Fensterwand. ", rest: "Fertig ist der Plan.", gate: release}
	pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
	session, _ := newSession(t, chat)
	stream, err := pipeline.Run(context.Background(), session, Input{Text: "Hallo", Output: OutputSpeech})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for event := range stream {
		events = append(events, event)
		if event.Kind == EventSpeech && event.Speech.Sequence == 0 {
			close(release)
		}
	}
	if count := len(only(events, EventSpeech)); count != 2 {
		t.Fatalf("chunks = %d (%s)", count, kinds(events))
	}
}

// gatedClient streams first, waits for gate, then streams rest. streamed, when
// set, is closed once first is handed over.
type gatedClient struct {
	first, rest string
	gate        <-chan struct{}
	streamed    chan struct{}
}

func (g *gatedClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	events := make(chan llm.Event)
	go func() {
		defer close(events)
		events <- llm.Event{Kind: llm.EventTextDelta, Text: g.first}
		if g.streamed != nil {
			close(g.streamed)
		}
		select {
		case <-g.gate:
		case <-time.After(3 * time.Second):
			events <- llm.Event{Kind: llm.EventError, Err: errors.New("speech never arrived before the reply ended")}
			return
		case <-ctx.Done():
			return
		}
		events <- llm.Event{Kind: llm.EventTextDelta, Text: g.rest}
		events <- llm.Event{Kind: llm.EventEnd}
	}()
	return events, nil
}

func (g *gatedClient) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not used")
}

func TestRun_ToolCallFlushesPendingSentence(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "Ich schaue im Katalog nach"},
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "search", Arguments: `{}`}},
			{Kind: llm.EventEnd},
		}},
		replyTurn(0, "Es gibt drei Duschtassen."),
	)
	pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
	session, _ := newSession(t, chat)
	events := runTurn(t, pipeline, session, Input{Text: "Welche Duschtassen gibt es?", Output: OutputSpeech})
	speech := only(events, EventSpeech)
	if len(speech) != 2 || speech[0].Speech.Text != "Ich schaue im Katalog nach" {
		t.Fatalf("speech = %+v (%s)", speech, kinds(events))
	}
	if len(only(events, EventToolCall)) != 1 || len(only(events, EventToolResult)) != 1 {
		t.Errorf("tool events not passed through: %s", kinds(events))
	}
}

func TestRun_NoSpeechSkipsTheAgent(t *testing.T) {
	t.Parallel()
	transcriber := llmtest.NewStubClient(transcriptTurn("[keine Sprache]"))
	chat := llmtest.NewStubClient()
	pipeline := newPipeline(t, &Options{Transcriber: transcriber, Speech: &fakeSpeech{}})
	session, _ := newSession(t, chat)
	events := runTurn(t, pipeline, session, Input{Audio: []byte("x"), ContentType: "audio/webm", Output: OutputSpeech})
	if kinds(events) != "transcript,done" || events[0].Text != "" || events[1].EndReason != EndReasonNoSpeech {
		t.Fatalf("events = %s %+v", kinds(events), events)
	}
	if len(chat.Requests()) != 0 {
		t.Error("agent ran on silence")
	}
}

func TestRun_VocabularyCorrectsTheTranscript(t *testing.T) {
	t.Parallel()
	transcriber := llmtest.NewStubClient(transcriptTurn("Passt der wasch tisch von Pfeifer und Mai?"))
	chat := llmtest.NewStubClient(replyTurn(0, "Ja."))
	pipeline := newPipeline(t, &Options{Transcriber: transcriber, Vocabulary: &llm.Vocabulary{
		Terms:        []string{"Waschtisch", "PFEIFFER & MAY"},
		Replacements: map[string]string{"pfeifer und mai": "PFEIFFER & MAY"},
	}})
	session, _ := newSession(t, chat)
	events := runTurn(t, pipeline, session, Input{Audio: []byte("x"), ContentType: "audio/webm"})
	if events[0].Text != "Passt der Waschtisch von PFEIFFER & MAY?" || events[0].Raw != "Passt der wasch tisch von Pfeifer und Mai?" {
		t.Errorf("transcript = %+v", events[0])
	}
	if got := chat.Requests()[0].Messages[1].Content; got != events[0].Text {
		t.Errorf("agent got %q, want the corrected transcript", got)
	}
}

func TestRun_FailedChunkDoesNotEndTheTurn(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(replyTurn(0, firstAnswer+secondAnswer))
	speech := &fakeSpeech{fail: func(text string) bool { return strings.HasPrefix(text, "Die Dusche") }}
	pipeline := newPipeline(t, &Options{Speech: speech})
	session, _ := newSession(t, chat)
	events := runTurn(t, pipeline, session, Input{Text: "Hallo", Output: OutputSpeech})
	failures := only(events, EventError)
	if len(failures) != 1 || failures[0].Speech == nil || failures[0].Speech.Sequence != 0 || !errors.Is(failures[0].Err, llm.ErrServerError) {
		t.Fatalf("errors = %+v", failures)
	}
	chunks := only(events, EventSpeech)
	if len(chunks) != 1 || chunks[0].Speech.Sequence != 1 {
		t.Errorf("speech = %+v", chunks)
	}
	if last := events[len(events)-1]; last.EndReason != agent.EndReasonStop || last.Usage.SpeechCharacters != len([]rune(secondAnswer)) {
		t.Errorf("done = %+v %+v", last, last.Usage)
	}
}

func TestRun_TranscriptionErrorEndsTheTurn(t *testing.T) {
	t.Parallel()
	transcriber := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrRateLimited}}})
	pipeline := newPipeline(t, &Options{Transcriber: transcriber})
	session, _ := newSession(t, llmtest.NewStubClient())
	events := runTurn(t, pipeline, session, Input{Audio: []byte("x"), ContentType: "audio/webm"})
	if kinds(events) != "error,done" || !errors.Is(events[0].Err, llm.ErrRateLimited) || events[1].EndReason != agent.EndReasonError {
		t.Fatalf("events = %s %+v", kinds(events), events)
	}
}

// failingRunner fails before the agent starts.
type failingRunner struct{}

func (failingRunner) RunWithOptions(context.Context, string, *agent.RunOptions) (<-chan agent.Event, error) {
	return nil, errors.New("session locked")
}

func TestRun_RunnerErrorEndsTheTurn(t *testing.T) {
	t.Parallel()
	pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
	events := runTurn(t, pipeline, failingRunner{}, Input{Text: "Hallo", Output: OutputSpeech})
	if kinds(events) != "error,done" || events[1].EndReason != agent.EndReasonError {
		t.Fatalf("events = %s %+v", kinds(events), events)
	}
}

// panickingRunner panics in the turn's goroutine, as a buggy Runner would.
type panickingRunner struct{}

func (panickingRunner) RunWithOptions(context.Context, string, *agent.RunOptions) (<-chan agent.Event, error) {
	panic("runner bug")
}

type panickingSpeech struct{}

func (panickingSpeech) Speak(context.Context, llm.SpeechRequest) (llm.SpeechResponse, error) {
	panic("speech bug")
}

func TestRun_PanicsEndTheTurnInsteadOfTheProcess(t *testing.T) {
	t.Parallel()
	t.Run("runner", func(t *testing.T) {
		t.Parallel()
		pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
		events := runTurn(t, pipeline, panickingRunner{}, Input{Text: "Hallo", Output: OutputSpeech})
		if kinds(events) != "error,done" || !strings.Contains(events[0].Err.Error(), "runner bug") || events[1].EndReason != agent.EndReasonError {
			t.Fatalf("events = %s %+v", kinds(events), events)
		}
	})
	t.Run("speech", func(t *testing.T) {
		t.Parallel()
		pipeline := newPipeline(t, &Options{Speech: panickingSpeech{}})
		session, _ := newSession(t, llmtest.NewStubClient(replyTurn(0, firstAnswer)))
		events := runTurn(t, pipeline, session, Input{Text: "Hallo", Output: OutputSpeech})
		failures := only(events, EventError)
		if len(failures) != 1 || failures[0].Speech == nil || !strings.Contains(failures[0].Err.Error(), "speech bug") {
			t.Fatalf("errors = %+v (%s)", failures, kinds(events))
		}
		if last := lastDone(t, events); last.EndReason != agent.EndReasonStop {
			t.Errorf("end reason = %q, want the turn to go on", last.EndReason)
		}
	})
}

func TestRun_RejectsInvalidInput(t *testing.T) {
	t.Parallel()
	session, _ := newSession(t, llmtest.NewStubClient())
	textOnly := newPipeline(t, &Options{Speech: &fakeSpeech{}})
	transcriptionOnly := newPipeline(t, &Options{Transcriber: llmtest.NewStubClient()})
	for name, testCase := range map[string]struct {
		pipeline *Pipeline
		runner   Runner
		input    Input
	}{
		"empty":                     {textOnly, session, Input{Text: "  "}},
		"audio without transcriber": {textOnly, session, Input{Audio: []byte("x"), ContentType: "audio/webm"}},
		"speech without generator":  {transcriptionOnly, session, Input{Text: "x", Output: OutputSpeech}},
		"unknown output":            {textOnly, session, Input{Text: "x", Output: "video"}},
		"unknown audio type":        {transcriptionOnly, session, Input{Audio: []byte("x"), ContentType: "audio/midi"}},
		"nil runner":                {textOnly, nil, Input{Text: "x"}},
	} {
		if _, err := testCase.pipeline.Run(context.Background(), testCase.runner, testCase.input); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, options := range map[string]*Options{"nil": nil, "neither": {}} {
		if _, err := New(options); err == nil {
			t.Errorf("New with %s Options must fail", name)
		}
	}
}

func TestEndReason(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		reason       agent.EndReason
		contextError error
		want         agent.EndReason
	}{
		{agent.EndReasonStop, nil, agent.EndReasonStop},
		{agent.EndReasonStop, context.Canceled, agent.EndReasonCancelled},
		{agent.EndReasonError, context.DeadlineExceeded, agent.EndReasonTimeout},
		{agent.EndReasonError, nil, agent.EndReasonError},
		{EndReasonNoSpeech, context.Canceled, EndReasonNoSpeech},
		{agent.EndReasonCancelled, context.DeadlineExceeded, agent.EndReasonCancelled},
	} {
		if got := endReason(testCase.reason, testCase.contextError); got != testCase.want {
			t.Errorf("endReason(%q, %v) = %q, want %q", testCase.reason, testCase.contextError, got, testCase.want)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		text     string
		want     []string
		wantRest string
	}{
		{"Die Dusche passt gut. Der Wasch", []string{"Die Dusche passt gut."}, " Der Wasch"},
		{"Das kostet 3.", nil, "Das kostet 3."},
		{"Das kostet 3.500 Euro. Und", []string{"Das kostet 3.500 Euro."}, " Und"},
		{"Ja. Die Dusche passt gut. ", []string{"Ja. Die Dusche passt gut."}, " "},
		{"Wirklich? Ja, absolut sicher! Gut", []string{"Wirklich? Ja, absolut sicher!"}, " Gut"},
		{"- Duschtasse\n- Waschtisch\n", []string{"- Duschtasse\n", "- Waschtisch\n"}, ""},
		{"Und dann kam lange nichts… Gut", []string{"Und dann kam lange nichts…"}, " Gut"},
	} {
		got, rest := splitSentences(testCase.text)
		if fmt.Sprint(got) != fmt.Sprint(testCase.want) || rest != testCase.wantRest {
			t.Errorf("splitSentences(%q) = %q, %q; want %q, %q", testCase.text, got, rest, testCase.want, testCase.wantRest)
		}
	}
}

func TestSpeakable(t *testing.T) {
	t.Parallel()
	for markdown, want := range map[string]string{
		"**Wichtig:** die _Duschtasse_ ist `flach`.": "Wichtig: die _Duschtasse_ ist flach.",
		"## Ergebnis": "Ergebnis",
		"- Die [Serie Liv](https://example.com/liv) passt.":              "Die Serie Liv passt.",
		"Mehr unter https://pfeiffer-may.de/katalog?x=1 oder www.liv.de": "Mehr unter oder",
		"| Breite | 90 cm |":        "Breite, 90 cm",
		"|---|---|":                 "",
		"```go\nfmt.Println()\n```": "",
		"> Zitat\n1. Erster Punkt":  "Zitat Erster Punkt",
		"![Grundriss](plan.png)":    "Grundriss",
		"…":                         "",
	} {
		if got := Speakable(markdown); got != want {
			t.Errorf("Speakable(%q) = %q, want %q", markdown, got, want)
		}
	}
}

func TestRun_CancelMidLLMStillDeliversDone(t *testing.T) {
	t.Parallel()
	transcriber := llmtest.NewStubClient(transcriptTurn("Hallo."))
	chat := &gatedClient{first: "Die Dusche ", gate: make(chan struct{})}
	pipeline := newPipeline(t, &Options{Transcriber: transcriber})
	session, _ := newSession(t, chat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := pipeline.Run(ctx, session, Input{Audio: []byte("x"), ContentType: "audio/wav"})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for event := range stream {
		events = append(events, event)
		if event.Kind == EventText {
			cancel()
		}
	}
	done := lastDone(t, events)
	if done.EndReason != agent.EndReasonCancelled || done.Usage.Transcription == nil || done.Usage.CostUSD != 0.0001 {
		t.Fatalf("done = %+v usage %+v", done, done.Usage)
	}
}

func TestRun_DeadlineMidTTSStillDeliversDone(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(replyTurn(0.002, firstAnswer+secondAnswer))
	speech := &fakeSpeech{delay: func(string) time.Duration { return time.Minute }}
	pipeline := newPipeline(t, &Options{Speech: speech, SpeechUSDPerCharacter: 0.00002})
	session, _ := newSession(t, chat)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stream, err := pipeline.Run(ctx, session, Input{Text: "Hallo", Output: OutputSpeech})
	if err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, collect(t, stream))
	if done.EndReason != agent.EndReasonTimeout || done.Usage.LLM.CostUSD != 0.002 || done.Usage.SpeechCharacters != 0 {
		t.Fatalf("done = %+v usage %+v", done, done.Usage)
	}
}

// The subtests run in a synctest bubble: synctest.Wait says when the turn is
// blocked on its consumer, and the fake clock runs out terminalGrace at once.
func TestRun_ConsumerThatStopsReading(t *testing.T) {
	t.Parallel()
	t.Run("done lands in the buffer", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
			chat := &gatedClient{first: "Die Dusche ", gate: make(chan struct{}), streamed: make(chan struct{})}
			session, _ := newSession(t, chat)
			ctx, cancel := context.WithCancel(t.Context())
			stream, err := pipeline.Run(ctx, session, Input{Text: "Hallo"})
			if err != nil {
				t.Fatal(err)
			}
			<-chat.streamed
			synctest.Wait() // the turn waits for the agent, nobody reads
			cancel()
			synctest.Wait() // the turn ended without a reader
			if done := lastDone(t, collect(t, stream)); done.EndReason != agent.EndReasonCancelled {
				t.Fatalf("end reason = %q", done.EndReason)
			}
		})
	})
	t.Run("full buffer gives up instead of blocking", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			deltas := make([]string, 100)
			for index := range deltas {
				deltas[index] = "x"
			}
			pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
			pipeline.terminalGrace = 20 * time.Millisecond
			session, _ := newSession(t, llmtest.NewStubClient(replyTurn(0.002, deltas...)))
			ctx, cancel := context.WithCancel(t.Context())
			stream, err := pipeline.Run(ctx, session, Input{Text: "Hallo"})
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait() // the buffer is full, nobody reads
			cancel()
			time.Sleep(time.Second) // the fake clock passes terminalGrace
			// The run gave up on done and closed the channel; a blocked run would
			// hand done over now that we read.
			if events := collect(t, stream); len(only(events, EventDone)) != 0 || len(events) != cap(stream) {
				t.Fatalf("got %d events (%s)", len(events), kinds(events))
			}
		})
	})
}
