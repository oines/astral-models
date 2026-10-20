# Provider verification

The normal suite runs against local HTTP fixtures and needs no credentials.
It covers request encoding, opaque reasoning, ordered Responses replay, stable
stream indices, incomplete calls, transport/provider errors, cancellation, and
Core session persistence with SQLite.

On 2026-10-20, the opt-in live suite passed against `deepseek-flash` at
`https://api.deepseek.com` for both Chat Completions and Responses:

| Scenario | Chat Completions | Responses |
| --- | --- | --- |
| One real function call and result continuation | Passed | Passed |
| Native reasoning retained and replayed | Passed | Passed |
| SQLite close/reopen and subsequent request | Passed | Passed |
| `no_reply` ends without an extra model request | Passed | Passed |
| New activation after `no_reply` | Passed | Passed |
| Usage reported | 5 / 5 requests | 5 / 5 requests |

Each route made five model requests, used `reasoning` effort `low`, and
executed the test `echo` tool once. This validates the tested model/endpoint
combination, not every DeepSeek model or every provider.

To repeat with your own credential in the process environment:

```sh
go test -tags=integration ./tests -run TestDeepSeekLive -v -count=1
```

The test skips without `DEEPSEEK_API_KEY`. It does not load `.env`, print keys,
dump response bodies on errors, or retain its temporary session database.
OpenAI, Qwen, and OpenRouter have fixture coverage; live verification for those
routes is not claimed here.
