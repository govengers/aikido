# Voice

`voice.Pipeline` wraps one agent turn with speech: text or recorded audio in, text or text plus speech out, all four combinations through one `Run` and one event stream. Audio input is transcribed with `llm.TranscribeDetailed`; speech output is synthesized sentence by sentence while the model is still writing, so playback starts before the answer is complete. The package has no web framework dependency.

## Pipeline

From [voice/example_test.go](../../voice/example_test.go) (`ExamplePipeline_Run`; a stub model and an echoing speech generator stand in for OpenRouter):

```go
	session, err := agent.NewSession(&agent.SessionOptions{
		ID: "kitchen-42", Client: model, History: memory.New(), Locker: agent.NewLocalLocker(), Model: "any/model",
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
```

`voice.Options`:

| Field | Default | Meaning |
|---|---|---|
| `Transcriber` | none | the `llm.Client` for audio input; without it audio input is `llm.ErrInvalidRequest` |
| `TranscribeModel` | `llm.DefaultTranscribeModel` | |
| `Vocabulary` | none | biases and corrects every transcript ([Media](Media.md#transcription-and-vocabulary)) |
| `Speech` | none | an `llm.SpeechGenerator`; without it speech output is `llm.ErrInvalidRequest` |
| `SpeechModel`, `Voice`, `Format` | `voice.DefaultSpeechModel` (`elevenlabs/eleven-v4-turbo`), `voice.DefaultVoice` (`sarah`), `mp3` | |
| `Speed` | provider default | `*float32` |
| `SpeechUSDPerCharacter` | 0 | price per synthesized character, the only way to a speech cost (eleven-v4-turbo lists 0.00002) |
| `SpeechConcurrency` | 2 | parallel synthesis calls; chunks still arrive in order |
| `SpokenStyle` | none | appended to the system prompt for speech turns; `voice.DefaultSpokenStyle` asks for short plain sentences |

`New` needs at least a `Transcriber` or a `Speech` generator. `Run(ctx, runner, input)` takes any `voice.Runner` (`*agent.Session` is one) and a `voice.Input{Text | Audio + ContentType, Language, Output}`; invalid input is refused up front with `llm.ErrInvalidRequest`. `voice.Speakable(markdown)` is what turns a reply into speakable text: code, URLs and markup dropped.

## Events

| Kind | Fields |
|---|---|
| `EventTranscript` | `Text` (corrected), `Raw`; once, first. `Text` `""` is followed by `EventDone` with `voice.EndReasonNoSpeech` and the agent does not run |
| `EventText` | reply delta |
| `EventToolCall`, `EventToolResult` | passed through from the agent |
| `EventSpeech` | `Speech` (`voice.SpeechChunk{Sequence, Text, Audio, ContentType, GenerationID}`), in `Sequence` order |
| `EventError` | `Err`; with `Speech` set only that chunk failed and the turn goes on, otherwise `EventDone` follows |
| `EventDone` | `EndReason` (an `agent.EndReason` or `EndReasonNoSpeech`) and `Usage`; always last, exactly once |

Every `Run` ends with one `EventDone`, also once `ctx` is over (`EndReason` `cancelled` or `timeout`), so a timed-out turn still reports its cost. A consumer that stopped reading does not block the turn forever. Panics of the runner, the transcriber or the speech generator end the turn with `EventError` and `EventDone`.

## Usage

`voice.Usage` sums one turn: `Transcription` (`*llm.Usage`), `LLM` (sum over the agent's model calls), `LLMCalls` (one `llm.Usage` per call with provider, latency and throughput), `SpeechCharacters`, `SpeechCostUSD` (characters times `SpeechUSDPerCharacter`) and `CostUSD` over all three.

## HTTP handler

`voice.NewHandler(&voice.HandlerOptions{Pipeline, Session})` serves one turn per POST as NDJSON (`application/x-ndjson`, one JSON object per line, flushed per event). It mounts on anything that takes an `http.Handler` (`gin.WrapH` on gin).

- Request: `application/json` `{"text","output","language"}`, `multipart/form-data` with an `audio` file part (or a `text` field) plus `output` and `language`, or a raw `audio/*` body with `?output=` and `?language=`.
- Lines: `{"type":"transcript",...}`, `text`, `tool_call`, `tool_result`, `speech` (`seq`, `text`, `content_type`, base64 `audio`), `error`, and the final `done` with `end_reason` and `usage` (incl. `llm_calls`).
- `Session(request)` resolves the `voice.Runner` per request (auth, tenant, conversation); an error answers 400. Check authentication in a middleware in front for a 401. The handler restores a JSON body, so `Session` can decode extra fields from it again.
- `ToolResult` picks what of a tool result reaches the client; the default sends no content.
- `MaximumAudioBytes` (default 8 MiB, larger is 413) and `Timeout` (default 5 minutes). The turn runs detached from the request context, so a closed tab does not lose it.
- `WriteTimeout` (default 30 s) caps writing and flushing one line. A client that does not take a line within it counts as gone: the handler stops writing and drains the turn to its end. It needs a `ResponseWriter` that supports `http.ResponseController.SetWriteDeadline` (net/http's does); without one lines are written without a cap. The deadline is cleared after each line, so a turn may wait longer than `WriteTimeout` for its next event (a long tool call) without the stream being reset. A wrapper without `Flush` (logging or gzip middleware without `Unwrap`) still gets every line, unflushed.
- Input errors answer 4xx with `{"error": "..."}` before any line.

Complete program: [examples/voicebot](../../examples/voicebot/main.go), a push-to-talk page over an embedded corpus.
