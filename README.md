# astral-models

Provider adapters for [Astral Core](https://github.com/oines/astral-core). Each adapter implements `astral.Model` directly and accepts Core’s ordered messages. There is no second agent loop or SDK message model.

This is an Astral-focused fork of [Twilight AI](https://github.com/felinics/twilight), built on the original work of its [authors and contributors](https://github.com/felinics/twilight/graphs/contributors). HTTP/SSE utilities, provider error handling, wire structures, and reasoning replay code derive from Twilight. The original [Apache License 2.0](LICENSE) and existing copyright notices are preserved unchanged. The API has intentionally diverged from upstream.

## Supported routes

| Protocol | Dialects | Reasoning controls |
| --- | --- | --- |
| Chat Completions | OpenAI, DeepSeek, Qwen, OpenRouter, MiMo, compatible endpoints | Native effort/toggle/budget fields for each dialect |
| Responses | OpenAI, DeepSeek | Native `reasoning.effort`, ordered native-item replay |
| Anthropic Messages | Anthropic-compatible endpoints | Native thinking blocks, budgets and signed replay |
| Google GenerateContent | Gemini-compatible endpoints | Native thinking configuration and thought signatures |

Text, resolved image URLs/data URLs, function tools, streaming and non-streaming replies are supported. Unsupported input parts and unsupported options fail explicitly. Embedding, audio/video generation, and Twilight’s agent runtime are outside this fork’s current scope. Compatibility is model/endpoint-specific; see [verification results](docs/verification.md) for the tested DeepSeek routes and the limits of fixture coverage.

## Use

Requires Go 1.25.7 or later.

```go
import (
    "context"
    "os"

    astral "github.com/oines/astral-core"
    models "github.com/oines/astral-models"
    "github.com/oines/astral-models/provider/openai/responses"
)

func generate(ctx context.Context) (astral.Reply, error) {
    model, err := responses.New(models.Config{
        Spec: astral.ModelSpec{
            ID: "my-profile:revision-1", Revision: "revision-1",
            ContextWindow: 128000, MaxOutputTokens: 8192,
        },
        Model: os.Getenv("MODEL_ID"),
        APIKey: os.Getenv("OPENAI_API_KEY"),
        ReplayScope: "my-connection:credential-revision-1",
    })
    if err != nil { return astral.Reply{}, err }
    return model.Stream(ctx, astral.Request{
        ModelID: model.Spec().ID, ModelRevision: model.Spec().Revision,
        Messages: []astral.Message{astral.User("Hello")},
        MaxOutputTokens: 1024,
    }, func(event astral.ModelEvent) {
        // Observe progress. Only the returned Reply is durable content.
    })
}
```

Token limits in this example are configuration values, not a model catalog. Supply verified limits for the selected model. `ListModels` returns discovery identities; it does not assert reasoning, image, tool, or replay support.

## Contracts

- A `Stream` call makes one HTTP attempt and stops emitting before it returns. Core's single `Session` implementation owns retries, tool execution, input queues, compaction, and forks. Ordinary generation and summaries share a total attempt budget; adapters never retry independently.
- Host code owns credentials, catalogs, profile revisions, default/override selection, media resolution, and capability verification. Bind an immutable provider instance to each profile revision.
- `GenerationOptions` preserves unset versus explicit zero/false. Effort names remain native values; the adapter does not invent a universal low/medium/high scale or convert effort to a token budget.
- Protocol-specific JSON belongs under its protocol key in `ProviderOptions`: `openai-completions`, `openai-responses`, `anthropic-messages` or `google-generative-ai`. Conflicting fields and history/stream controls are rejected.
- Responses items retain original order and opaque JSON, including encrypted reasoning, item/call IDs, annotations, and unknown fields. Chat retains the native reasoning payload, including late encrypted details. Display text cannot replace replay data.
- Opaque state is bound to protocol, remote model, and replay scope. A route change fails explicitly. Set `ReplayScope` to the Host’s connection/credential revision; otherwise the library derives it from endpoint, key, and configured headers.
- If a provider explicitly rejects its own encrypted reasoning or signature, `RecoverRequest` offers Core a copy with that opaque replay state removed. Core permits one repair within the same total attempt budget, rechecks its budget, and records both actual requests. Plain text, tool calls and results remain intact; durable history is unchanged. Authentication failures, timeouts and generic validation errors never trigger this removal. Failed stream fragments and retry diagnostics never become subsequent conversation or summary input.
- `RequestBody` exposes the credential-free wire body. `EstimateTokens` uses a conservative JSON-byte estimate, requires an explicit `ImageTokenBudget` for images, and can be replaced by the Host’s model-specific estimator.
- `Usage.Available` distinguishes missing usage from a reported zero. Optional cache/reasoning counts remain optional. `models.APIError` remains available through `errors.As`, with Core retry classification around it.

## Development

For work alongside the Core checkout, create a local, ignored Go workspace containing this module and `../agent-core`. Release builds use the exact Core version pinned in `go.mod`; no developer-specific path is committed.

```sh
go test -race ./...
go vet ./...
```

Tests cover native parameter mapping, encrypted reasoning replay, output order, stream indices, malformed/truncated/error responses, cancellation, multimodal tool output, and Core SQLite close/reopen plus `no_reply` boundaries.
