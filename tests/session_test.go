package tests_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	astral "github.com/oines/astral-core"
	"github.com/oines/astral-core/session"
	"github.com/oines/astral-core/store/sqlite"
	models "github.com/oines/astral-models"
	"github.com/oines/astral-models/provider/openai/completions"
	"github.com/oines/astral-models/provider/openai/responses"
)

type provider interface {
	astral.Model
	EstimateTokens(context.Context, astral.Request) (int, error)
}

// The provider receives real Core projections, including the next request
// after a tool result, after an SQLite reopen, and after a no_reply control.
func TestDurableToolAndNoReplyRoundTrip(t *testing.T) {
	for _, protocol := range []string{completions.Protocol, responses.Protocol} {
		t.Run(protocol, func(t *testing.T) {
			var calls, executions atomic.Int32
			captured := make(chan string, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				captured <- string(body)
				n := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				name, id := "lookup", "c1"
				if n == 3 {
					name, id = "no_reply", "quiet"
				}
				if protocol == completions.Protocol {
					delta, finish := `{"content":"done"}`, "stop"
					if n == 1 || n == 3 {
						delta = fmt.Sprintf(`{"reasoning_details":[{"type":"reasoning.encrypted","id":"rs%d","data":"cipher%d"}],"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":"{}"}}]}`, n, n, id, name)
						finish = "tool_calls"
					}
					fmt.Fprintf(w, "data: {\"id\":\"r%d\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", n, delta, finish)
				} else {
					output := `[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[]}]}]`
					if n == 1 || n == 3 {
						output = fmt.Sprintf(`[{"type":"reasoning","id":"rs%d","summary":[],"encrypted_content":"cipher%d"},{"type":"function_call","id":"f%d","call_id":%q,"name":%q,"arguments":"{}"}]`, n, n, n, id, name)
					}
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r%d\",\"status\":\"completed\",\"output\":%s}}\n\n", n, output)
				}
			}))
			defer server.Close()
			cfg := models.Config{Spec: astral.ModelSpec{ID: "profile:1", Revision: "rev1", ContextWindow: 100000, MaxOutputTokens: 4096}, BaseURL: server.URL, Model: "native", ReplayScope: "connection:1"}
			var m provider
			var err error
			if protocol == completions.Protocol {
				cfg.Dialect = models.OpenRouter
				m, err = completions.New(cfg)
			} else {
				m, err = responses.New(cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			lookup := astral.ToolFunc{Spec: astral.ToolDefinition{Name: "lookup", Version: "1", Parameters: json.RawMessage(`{"type":"object"}`)}, Run: func(context.Context, astral.Invocation, func(astral.ToolProgress)) (astral.ToolExecution, error) {
				executions.Add(1)
				return astral.ToolExecution{Result: &astral.ToolResult{Content: astral.Text("found"), Outcome: astral.OutcomeSucceeded}}, nil
			}}
			quiet := astral.ToolFunc{Spec: astral.ToolDefinition{Name: "no_reply", Version: "1", MayEndRun: true, Parameters: json.RawMessage(`{"type":"object"}`)}, Run: func(context.Context, astral.Invocation, func(astral.ToolProgress)) (astral.ToolExecution, error) {
				return astral.ToolExecution{EndRun: &astral.RunEnd{Reason: "quiet"}}, nil
			}}
			db := filepath.Join(t.TempDir(), "history.db")
			open := func() (*session.Manager, *sqlite.Store) {
				st, err := sqlite.Open(db)
				if err != nil {
					t.Fatal(err)
				}
				manager, err := session.New(session.Config{Model: m, Store: st, Tools: []astral.Tool{lookup, quiet}, Retry: astral.RetryPolicy{MaxAttempts: 1},
					Options: astral.GenerationOptions{Reasoning: astral.ReasoningOptions{Effort: "max"}},
					EstimateTokens: func(ctx context.Context, _ astral.Model, req astral.Request) (int, error) {
						return m.EstimateTokens(ctx, req)
					},
					Compaction: session.CompactionPolicy{Disabled: true}})
				if err != nil {
					_ = st.Close()
					t.Fatal(err)
				}
				return manager, st
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			manager, store := open()
			chat, err := manager.NewSession(ctx, session.SessionOptions{ID: "persisted"})
			if err != nil {
				t.Fatal(err)
			}
			run := func(chat *session.Session, text string) astral.RunResult {
				r, err := chat.Start(ctx, astral.User(text))
				if err != nil {
					t.Fatal(err)
				}
				result, err := r.Wait(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			run(chat, "first")
			if executions.Load() != 1 || calls.Load() != 2 {
				t.Fatalf("tool loop calls=%d execution=%d", calls.Load(), executions.Load())
			}
			first, second := <-captured, <-captured
			if !strings.Contains(first, `"effort":"max"`) || !strings.Contains(second, "cipher1") || !strings.Contains(second, "found") {
				t.Fatalf("tool or options replay missing: %s", second)
			}
			if err := manager.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			manager, store = open()
			defer store.Close()
			defer manager.Close(context.Background())
			chat, err = manager.OpenSession(ctx, "persisted")
			if err != nil {
				t.Fatal(err)
			}
			result := run(chat, "nothing needed")
			if result.EndReason != "quiet" || calls.Load() != 3 {
				t.Fatalf("control did not end the run: %+v calls=%d", result, calls.Load())
			}
			if third := <-captured; !strings.Contains(third, "cipher1") {
				t.Fatal("ciphertext lost on reopen")
			}
			run(chat, "new work")
			fourth := <-captured
			if strings.Contains(fourth, `"call_id":"quiet"`) || strings.Contains(fourth, `"id":"quiet"`) || strings.Contains(fourth, "cipher3") {
				t.Fatalf("no_reply control leaked into model protocol: %s", fourth)
			}
			if !strings.Contains(fourth, "cipher1") || executions.Load() != 1 {
				t.Fatal("ordinary history or tool execution was lost/duplicated")
			}
		})
	}
}
