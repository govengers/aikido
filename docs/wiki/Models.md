# Models

`llm.Client` is the provider-agnostic model interface: `Stream(ctx, llm.Request) (<-chan llm.Event, error)` and `Complete(ctx, llm.Request) (llm.Response, error)`. `llm/openrouter` and `llm/codex` implement it; tests use `llm/llmtest.StubClient` ([Testing](Testing.md)). Images, video, speech and decision models are on [Media](Media.md), errors and retries on [Errors-and-Retry](Errors-and-Retry.md).

## Request

| Field | Meaning |
|---|---|
| `Model` | provider model id, verbatim (`"anthropic/claude-sonnet-4.6"`); empty uses the client's default where it has one |
| `Messages` | the conversation, system prompt first; `llm.Message{Role, Content, Images, Audio, ToolCalls, ToolCallID, Cache}` with roles `llm.RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool` |
| `Tools` | `[]llm.ToolDefinition{Name, Description, Parameters}` (JSON Schema) |
| `MaximumTokens` | completion cap, 0 leaves the provider default |
| `Temperature` | `*float32`, nil leaves the default; `new(float32(0))` asks for deterministic decoding |
| `Thinking` | `llm.ThinkingByEffort(...)` or `llm.ThinkingByBudget(tokens)`; OpenRouter maps a budget onto an effort (below 2048 low, below 16384 medium, else high) |
| `StopSequences` | end the generation on one of them |
| `Modalities`, `ImageConfiguration` | image output ([Media](Media.md#images)) |
| `ResponseFormat` | `*llm.JSONSchema{Name, Schema, Strict}` for structured output (OpenRouter `response_format`, Codex `text.format`) |

`Message.Cache` (`&llm.CacheBreakpoint{}`, TTL `llm.CacheTTLFiveMinutes` or `CacheTTLOneHour`) marks a prompt-cache breakpoint; the agent sets them itself ([Agent](Agent.md#prompt-caching)).

## Response, Collect and streaming

`llm.Response` holds `Text` (without reasoning), `ToolCalls`, `Images`, `Usage` (nil when the provider reported none) and `FinishReason`. `llm.Collect(ctx, client, request)` drains a stream into one; after an `EventError` it reads on until the channel closes, so the usage of the failed (and billed) call is kept, and returns the first error. On a cancelled `ctx` it returns what it gathered with the error. From [llm/example_test.go](../../llm/example_test.go) (`ExampleCollect`):

```go
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
```

The stream contract: an error before the first byte (authentication, invalid request, exhausted retries) is returned by `Stream` directly, later failures arrive as `EventError`; an OpenRouter stream that stops before both `[DONE]` and a finish reason was cut off and is an `EventError` with `llm.ErrServerError`. A finish reason `error` without an error envelope is `llm.ErrServerError` too, on `Stream` and on `Complete` (which returns the partial response and its usage with it). A stream `data:` payload that is not valid JSON ends the stream with `EventError` (`llm.ErrInvalidResponse`) and drops its pending tool calls; a trailing usage block still arrives. `EventEnd` is always last, then the client closes the channel; it closes early when `ctx` ends. Read until it closes or cancel `ctx`, otherwise the producer goroutine blocks. From [llm/openrouter/example_test.go](../../llm/openrouter/example_test.go) (`ExampleClient_Stream`):

```go
	// Drain until the channel closes; EventEnd is always the last event.
	for event := range events {
		switch event.Kind {
		case llm.EventTextDelta:
			fmt.Printf("%q\n", event.Text)
		case llm.EventEnd:
			fmt.Println("end:", event.FinishReason)
		default:
		}
	}
```

Event kinds: `EventTextDelta` (`Text`), `EventThinking` (`Text`), `EventToolCall` (`Tool`, one complete call), `EventImage` (`Image`), `EventUsage` (`Usage`), `EventError` (`Err`), `EventEnd` (`FinishReason`).

`llm.Usage`: `PromptTokens` (cached included), `CompletionTokens` (reasoning included), `CacheReadTokens`, `CacheWriteTokens`, `CostUSD` (the provider's billed price, not an estimate), `Provider` (who served it), `Latency` (to first output), `Duration`, `TokensPerSecond`. A call that ends in an error or a content filter was billed too; its usage arrives with the error.

Use `Complete` instead of `Stream` for image output: a base64 image in one SSE line exceeds the line cap.

## OpenRouter

`openrouter.NewClient(&openrouter.Options{APIKey: key})`; the client is safe for concurrent use.

| Option | Default | Meaning |
|---|---|---|
| `APIKey` | required | sent as `Authorization: Bearer` |
| `BaseURL` | `openrouter.DefaultBaseURL` (`https://openrouter.ai/api/v1`) | a proxy or a test server |
| `HTTPClient` | waits at most 10 min for response headers, no overall timeout | bound long streams with a `ctx` deadline instead |
| `HTTPReferer`, `XTitle` | empty | OpenRouter attribution headers |
| `Provider` | none | default `*openrouter.ProviderPreferences` for chat and decision calls |
| `ProviderOrder` | none | shorthand: one entry pins it (no fallbacks), several form a fallback chain; ignored when `Provider` is set |
| `MaximumAttempts` | 3 | HTTP attempts per call on 429, 5xx and network errors before a status arrived, on every path (the video submit on 429 only); a 2xx is never sent again, also when its body breaks off (`llm.ErrServerError`); 1 disables retry |
| `VideoPollInterval` | 3 s | `GenerateVideo` polling |

Every chat request asks OpenRouter for usage accounting, so `CostUSD` is set on streamed and non-streamed calls alike.

Size caps (fixed, not options): a non-streaming answer (`Complete`, images, speech, decisions, model lists, video jobs) is read up to 64 MiB, a video clip up to 512 MiB; a larger answer fails with `llm.ErrInvalidResponse` and is not retried. Of an error answer at most 64 KiB is read, its message keeps 256 bytes. A stream line may be 1 MiB; one stream may carry at most 128 tool calls with 16 MiB of arguments together, beyond that it ends with `EventError` (`llm.ErrInvalidResponse`) and its tool calls are dropped.

### Provider routing

`openrouter.ProviderPreferences` is OpenRouter's `provider` block: `Order`, `AllowFallbacks` (`*bool`), `Only`, `Ignore`, `Sort` (`"price"`, `"throughput"`, `"latency"`), `Quantizations`, `RequireParameters`, `DataCollection` (`"deny"`), `ZDR` (`true`: only endpoints with zero data retention, stricter than `DataCollection`), `MaximumPrice` (`*openrouter.MaximumPrice{Prompt, Completion}` in USD per million tokens). `openrouter.WithProvider(ctx, preferences)` replaces the client's preferences (no merge) for every call made with that context: agent runs, voice turns and `Transcribe` included. From `ExampleWithProvider`:

```go
	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:   "sk-or-...",
		BaseURL:  server.URL,
		Provider: &openrouter.ProviderPreferences{Sort: "price"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	request := llm.Request{Model: "anthropic/claude-haiku-4.5", Messages: []llm.Message{{Role: llm.RoleUser, Content: "Hi"}}}

	if _, err := client.Complete(context.Background(), request); err != nil {
		fmt.Println(err)
		return
	}
	pinned := openrouter.WithProvider(context.Background(), &openrouter.ProviderPreferences{
		Only:           []string{"google-vertex/europe"},
		AllowFallbacks: new(false),
	})
```

### Model catalogue

- `client.Models(ctx)` lists `[]openrouter.Model` (id, context length, `Architecture` modalities, `Pricing`, `TopProvider`, supported parameters). Images-only models are missing from it.
- `client.ModelEndpoints(ctx, "author/slug")` lists the providers serving a model: `Endpoint.Tag` (the slug for `ProviderPreferences`), quantization, limits, `Pricing`, `Status`, `UptimeLast30Minutes`, `LatencyLast30Minutes` and `ThroughputLast30Minutes` (`*openrouter.Percentiles` P50 to P99).
- `client.VideoModels(ctx)` and `client.DecisionModels(ctx)` list the models of those endpoints ([Media](Media.md)).
- Prices are `openrouter.USD` per token; `PerMillion()` converts.

### APIError

Every answer with an unexpected HTTP status fails with `*openrouter.APIError{StatusCode, Code, Message}`; `errors.Is` still matches the `llm` sentinel for the status. From `ExampleAPIError`:

```go
	var apiError *openrouter.APIError
	if errors.As(err, &apiError) {
		fmt.Println(apiError.StatusCode, apiError.Message)
	}
	fmt.Println(errors.Is(err, llm.ErrAuthentication))
```

Status mapping and retry: [Errors-and-Retry](Errors-and-Retry.md).

## Codex (ChatGPT subscription)

`llm/codex` talks to the Codex backend of a ChatGPT subscription the way the Codex CLI does, and bills the subscription instead of an API key. It supports system messages, user and assistant text, image input (`URL`, or `Data` sent inline as a data URI; an image with neither is `llm.ErrInvalidRequest`) and `ResponseFormat`. Tools, tool results, audio input and `Thinking` fail with `llm.ErrInvalidRequest`; `MaximumTokens`, `Temperature`, `StopSequences`, `Modalities` and `ImageConfiguration` are ignored. So it serves one-shots and structured output, not agents with tools.

Login:

1. `codex.StartDeviceLogin(ctx, nil)` returns a `codex.DeviceCode`; show its `VerificationURL` and `UserCode` (`codex.ErrDeviceLoginDisabled` if the issuer offers no device login).
2. `codex.PollDeviceLogin(ctx, nil, deviceCode)` waits until the user confirmed and returns `codex.Tokens`. Alternatively `codex.ParseLogin(raw)` reads a Codex CLI `~/.codex/auth.json`, which then shares its refresh token with that CLI.
3. `codex.NewTokenSource(&codex.TokenSourceOptions{Tokens: tokens, OnRefresh: save})` keeps the login valid; `OnRefresh` receives every rotated login and must persist it, aikido writes tokens nowhere.
4. `codex.NewClient(&codex.Options{Tokens: source, Originator: "my-app"})`.

From [llm/codex/example_test.go](../../llm/codex/example_test.go) (`ExampleNewClient`, a test server stands in for the backend):

```go
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
```

The refresh token is single use: keep one `TokenSource` per login and process. A refresh token used twice (two processes, or this and the Codex CLI) revokes the login; `Token` then fails with `codex.ErrReauthenticationRequired` and the user logs in again. Known limit: a refresh whose answer does not arrive (connection cut, timeout, a 200 without tokens) fails with `codex.ErrRefreshInterrupted`, because the issuer may have rotated the refresh token and the new one is lost. It carries its cause as text only and is not `llm.ErrServerError`, so `llm.CollectWithRetry` and `llm.CompleteWithRetry` do not replay it. No client can repair that: `Stream` and `Models` never retry a failed refresh, and the next `Token` call tries the previous refresh token once more, which either works or fails with `codex.ErrReauthenticationRequired`; then the user has to sign in again. `client.Models(ctx)` lists the subscription's model ids. Reads are capped: a login endpoint answer over 1 MiB and a models catalog over 8 MiB are errors, a stream line over 4 MiB ends the stream with an error.
