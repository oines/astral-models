# astral-models

Provider adapters for [Astral Core](https://github.com/oines/astral-core). Each adapter implements `astral.Model` directly and accepts Core’s ordered messages. There is no second agent loop or SDK message model.

This is an Astral-focused fork of [Twilight AI](https://github.com/felinics/twilight), built on the original work of its [authors and contributors](https://github.com/felinics/twilight/graphs/contributors). HTTP/SSE utilities, provider error handling, wire structures, and reasoning replay code derive from Twilight. The original [Apache License 2.0](LICENSE) and existing copyright notices are preserved unchanged. The API has intentionally diverged from upstream.

## Supported routes

| Protocol | Dialects | Reasoning controls |
| --- | --- | --- |
| Chat Completions | OpenAI, DeepSeek, Qwen, OpenRouter, MiMo, compatible endpoints | Explicit effort/toggle/budget formats, including Together and Fireworks |
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

## Reasoning controls

Bind `Config.ReasoningFormat` and `Config.Reasoning` to the exact connection, protocol and remote model. `ReasoningFormats(protocol)` lists compatible formats; `ResolveReasoningControls(protocol, config)` returns only selections the same request encoder can execute. The host supplies capability facts and owns profile defaults and session inheritance. An empty `ReasoningOptions` reaches the provider without reasoning parameters, even when capability metadata describes an enabled API default.

For a verified Responses compatibility difference, set `Config.ResponsesCompatibility.SupportsInclude` or `SupportsStore` to `false`. The encoder then omits its automatic `include: ["reasoning.encrypted_content"]` or `store: false` field and rejects an explicit value for that key in `ProviderOptions`. When only the encrypted reasoning include value is unsupported, set `SupportsEncryptedReasoningInclude` to `false`; other native include values remain available. Nil retains standard Responses behavior. Native output items, including any encrypted reasoning the service returns, are still preserved and replayed without modification.

`Efforts` is a list of native strings: `nil` means unknown, while `[]` explicitly means no effort selector. `Toggle`, `Disable`, `Budget` and budget bounds describe independent controls. `DefaultEffort` and `DefaultOn` explain API defaults without adding request parameters. `OnEffort` and `OffEffort` explicitly bind on/off to native effort tokens when that route uses them. `ParseReasoningMetadata` extracts sparse capability fields from live model records, preserving explicit null, false and empty lists. It never chooses a request format from the model name.

The `custom` format accepts a bounded `ReasoningMapping` of literal on/off and effort JSON fragments. Fragments cannot modify messages, tools, credentials, routing, output limits or other request-owned fields. Every advertised selection is validated through `EncodeReasoning`, which also powers each provider's `RequestBody`. With an explicit format, `ProviderOptions` cannot bypass the reasoning selection through another managed field; unrelated siblings such as `reasoning.summary` remain available. An omitted format retains legacy dialect behavior for existing callers; new integrations should bind a format explicitly, including `protocol-default` where appropriate.

## Contracts

- A `Stream` call makes one HTTP attempt and stops emitting before it returns. Core's single `Session` implementation owns retries, tool execution, input queues, compaction, and forks. Ordinary generation and summaries share a total attempt budget; adapters never retry independently.
- Host code owns credentials, catalogs, profile revisions, default/override selection, media resolution, and capability verification. Bind an immutable provider instance to each profile revision.
- `GenerationOptions` preserves unset versus explicit zero/false. Effort names remain native values; the adapter does not invent a universal low/medium/high scale or convert effort to a token budget.
- Protocol-specific JSON belongs under its protocol key in `ProviderOptions`: `openai-completions`, `openai-responses`, `anthropic-messages` or `google-generative-ai`. Conflicting fields and history/stream controls are rejected.
- Responses items retain original order and opaque JSON, including encrypted reasoning, item/call IDs, annotations, and unknown fields. Native item data is authoritative for replay; parsed text and calls support display and execution, without a second comparison during replay. Chat retains the native reasoning payload, including late encrypted details and structured thinking/text chunks returned by Mistral-compatible routes. Display text cannot replace replay data.
- Opaque state is bound to protocol, remote model, and replay scope. A route change fails explicitly. Set `ReplayScope` to the Host’s connection/credential revision; otherwise the library derives it from endpoint, key, and configured headers.
- If a provider explicitly rejects its own encrypted reasoning or signature, `RecoverRequest` offers Core a copy with that opaque replay state removed. Core permits one repair within the same total attempt budget, rechecks its budget, and records both actual requests. Plain text, tool calls and results remain intact; durable history is unchanged. Authentication failures, timeouts and generic validation errors never trigger this removal. Failed stream fragments and retry diagnostics never become subsequent conversation or summary input.
- Normalized `Usage.InputTokens` includes cached reads/writes; cache fields are breakdowns, not additional input. Provider context-limit errors are exposed to Core as `ModelError.ContextOverflow`, distinct from transient rate/server errors.
- `RequestBody` exposes the credential-free wire body. `EstimateTokens` estimates model-visible text, reasoning, tool arguments/schemas and system content. It uses roughly four ASCII characters per token, one token per non-ASCII character (two for supplementary characters), and 1200 tokens per image by default; `ImageTokenBudget` overrides the image approximation. Encoded image bytes and HTTP envelope keys are not text tokens. `ValidateRequest` separately checks the complete wire contract, including tool pairing and final output constraints. Full requests, empty requests and incremental message tails use the same estimator; a Host may supply a model-specific tokenizer.
- `Usage.Available` distinguishes a missing usage report from a present report. `InputMissing` and `OutputMissing` mark counts omitted by a present report, including an empty usage object; explicit zero remains known. Chat and Responses accept each other's input/output and detail field names as fallbacks, with native fields (including zero) taking precedence. Totals never supply missing counts by subtraction. Anthropic merges reported fields across stream events before adding cache reads/writes to input; Gemini adds reported thoughts to output but keeps output missing when the candidate count was omitted. Optional cache/reasoning counts remain optional. `models.APIError` remains available through `errors.As`, with Core retry classification around it.

## Development

For work alongside the Core checkout, create a local, ignored Go workspace containing this module and `../agent-core`. Release builds use the exact Core version pinned in `go.mod`; no developer-specific path is committed.

```sh
go test -race ./...
go vet ./...
```

Tests cover native parameter mapping, encrypted reasoning replay, output order, stream indices, malformed/truncated/error responses, cancellation, multimodal tool output, and Core SQLite close/reopen plus `no_reply` boundaries.
