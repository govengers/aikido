# Media

Beyond chat, model abilities are optional interfaces on the client: `llm.ImageGenerator`, `llm.SpeechGenerator`, `llm.VideoGenerator`, `llm.Decider`. Code that holds an `llm.Client` type-asserts; `*openrouter.Client` and `*llmtest.StubClient` implement all four, `*codex.Client` none. Transcription runs on any client whose provider accepts audio input. Every snippet below is from [llm/example_test.go](../../llm/example_test.go), where a `StubClient` stands in for OpenRouter.

## Images

Two paths, by model:

- **Images endpoint** (`POST /api/v1/images`): `openai/gpt-image-2.5-flare` and friends answer only there and are missing from `GET /models`. Use `GenerateImage`.
- **Chat completions**: models that answer there (the Gemini image family, `openai/gpt-5.4-image-2`) use `Complete` with `Modalities: []string{"image", "text"}`, the only path that also carries a conversation. Not `Stream`: one base64 image exceeds the SSE line cap.

From `ExampleImageGenerator`:

```go
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
```

From `ExampleRequest_imageOutput`:

```go
	response, err := client.Complete(context.Background(), llm.Request{
		Model:              "google/gemini-3.1-flash-image-preview",
		Messages:           []llm.Message{{Role: llm.RoleUser, Content: "A pixel-art fox in tall grass."}},
		Modalities:         []string{"image", "text"},
		ImageConfiguration: &llm.ImageConfiguration{AspectRatio: "16:9", ImageSize: "2K"},
	})
```

- `ImageRequest`: `AspectRatio` (`1:1`, `3:2`, `16:9`, ...; OpenAI images models refuse `4:5`), `ImageSize` (`1K`, `2K`, `4K`), or `Size` as explicit `"WIDTHxHEIGHT"` (both sides divisible by 16, long edge at most 3840 on OpenAI) instead of the two; `Quality` `auto` to `max` (about 16x the cost of `auto` on OpenAI); `References` are input images as `data:` or `https` URLs.
- `ImagePart.Data` holds inline bytes (decoded from a `data:` URI), `URL` a remote link. Agent runs surface images as `EventImage`, and `Drain` puts them on the assistant message.
- Complete program: [examples/image-generation](../../examples/image-generation/main.go); the [CLI](CLI.md#image) picks the path and frame for you.

## Video

`GenerateVideo` submits a job, polls it and downloads the clip in one blocking call, minutes on a real provider; bound it with `ctx`. From `ExampleVideoGenerator`:

```go
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
```

Every model accepts a closed list of durations, resolutions, ratios and sizes and rejects the rest; `openrouter.Client.VideoModels(ctx)` lists them, and unset fields stay off the wire. `FirstFrame`/`LastFrame` anchor the clip, `References` guide style. `VideoResponse` has `Video`, `ContentType` (sniffed), `JobID` (also set on errors after the submit) and `Usage.CostUSD`. A refused prompt, and a job that completes without a clip (Veo's silent rejection), is `llm.ErrContentFiltered`.

## Speech

`Speak` returns the whole clip; OpenRouter does not stream it, so speak sentence by sentence for early playback ([Voice](Voice.md) does). From `ExampleSpeechGenerator`:

```go
	response, err := speaker.Speak(context.Background(), llm.SpeechRequest{
		Model:  "elevenlabs/eleven-v4-turbo",
		Input:  "The shower goes by the window.",
		Voice:  "sarah",
		Format: "mp3",
	})
```

`Format` is `mp3` (`audio/mpeg`) or `pcm` (16-bit little-endian mono, OpenRouter's default). OpenRouter reports no cost on speech: `SpeechResponse` has no `Usage`, the price is on `GET /api/v1/generation?id=<GenerationID>` some seconds later, or computed per character ([Voice](Voice.md#usage)).

## Transcription and vocabulary

`llm.Transcribe(ctx, client, llm.TranscribeRequest{...})` turns a clip into text with one chat call on `client.Complete`, temperature 0, default model `llm.DefaultTranscribeModel` (`google/gemini-3.1-flash-lite`). `Format` is the container (`wav`, `mp3`, `m4a`, `webm`, `ogg`, `flac`, `aiff`); `llm.AudioFormat("audio/webm;codecs=opus")` derives it from a browser MIME type. A clip without speech returns `""`, not an error. `TranscribeDetailed` returns `Raw` and corrected `Text`. From `ExampleTranscribe`:

```go
	text, _, err := llm.Transcribe(context.Background(), client, llm.TranscribeRequest{
		Audio:      []byte("RIFF...."),
		Format:     "wav",
		Hint:       "The speaker speaks German.",
		Vocabulary: &llm.Vocabulary{Terms: []string{"Waschtisch"}},
	})
```

Keep example sentences and amounts out of `Hint`: for a silent clip the model repeats them as the transcript.

`llm.Vocabulary` fixes words a speech model gets wrong (brand names, article numbers, trade words): `Terms` go into the prompt (at most `llm.MaximumVocabularyPromptTerms`, 200, and `MaximumVocabularyPromptBytes`, 3000), then `Correct` rewrites the transcript: `Replacements` (heard variant to term), exact term matches ignoring spaces, punctuation and case, and with `Fuzzy` near misses of terms with at least 6 letters (edit distance 1, 2 from 9 letters on, same first letter, every heard word at least 4 letters, so short common words are never touched). From `ExampleVocabulary_Correct`:

```go
	vocabulary := &llm.Vocabulary{
		Terms:        []string{"LIV60G", "Duschtasse"},
		Replacements: map[string]string{"pfeifer und mai": "PFEIFFER & MAY"},
		Fuzzy:        true,
	}
	fmt.Println(vocabulary.Correct("Die Duschtase LIV 60 g von Pfeifer und Mai."))
	// Output:
	// Die Duschtasse LIV60G von PFEIFFER & MAY.
```

`llm/codex` rejects audio.

## Decisions

Decision models (`typesafe/jev-1.14`, `perplexity/pplx-decider-v1.2`, ...) answer typed questions about a state with probabilities instead of prose, on OpenRouter's alpha Decisions API; chat completions rejects them. From `ExampleDecider`:

```go
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
```

- Questions: `llm.NoulQuestion` (yes/no, answer `Noul` = probability of yes), `llm.ChoiceQuestion(instructions, options)` (`Choice`, `Probabilities`, `Confidence`), `llm.ScoreQuestion(instructions, levels...)` (`Score`, 0 = first level, `Legend`), `llm.BoxQuestion` (a `Box` in fractions of the image; always returns one, so pair it with a noul on presence).
- `State` is a string or any JSON-marshalable value; images go into a `[]any` state as `llm.ImagePart` values next to text.
- A question left unanswered is `llm.ErrServerError`, returned with the answers that did arrive. Provider routing applies. `openrouter.Client.DecisionModels(ctx)` lists the models.
