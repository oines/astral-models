// Package models supplies provider configuration and diagnostics for Astral.
// Providers implement astral.Model and use Core's messages directly.
package models

import (
	"context"
	"net/http"

	astral "github.com/oines/astral-core"
	"github.com/oines/astral-models/internal/reqheaders"
)

type Dialect string

const (
	OpenAI     Dialect = "openai"
	DeepSeek   Dialect = "deepseek"
	Qwen       Dialect = "qwen"
	OpenRouter Dialect = "openrouter"
)

// Config binds one immutable model profile to a provider connection.
// Spec.ID is the host's identity; Model is the remote API's model identifier.
// Credentials and HTTP transport are never copied into Core history.
type Config struct {
	Spec        astral.ModelSpec
	Model       string
	BaseURL     string
	APIKey      string
	Headers     map[string]string
	HTTPClient  *http.Client
	Dialect     Dialect
	ReplayScope string
	Streaming   *bool
	Reasoning   *ReasoningCapabilities
	// ReasoningReplay selects the assistant field used by Chat Completions:
	// reasoning_content, reasoning_details, or reasoning. Empty uses the dialect.
	ReasoningReplay        string
	RequireReasoningReplay bool
	// ImageTokenBudget is the host's conservative per-image input reservation.
	// Estimation refuses images unless an explicit positive budget is supplied.
	ImageTokenBudget int
}

// ReasoningCapabilities describes a specific connection/protocol/model route.
// Nil means unknown. An empty non-nil Efforts list means no effort control.
// No implicit low/medium/high list or effort-to-budget mapping is introduced.
type ReasoningCapabilities struct {
	Toggle    *bool    `json:"toggle,omitempty"`
	Disable   *bool    `json:"disable,omitempty"`
	DefaultOn *bool    `json:"default_on,omitempty"`
	Efforts   []string `json:"efforts"`
	MinBudget *int     `json:"min_budget,omitempty"`
	MaxBudget *int     `json:"max_budget,omitempty"`
	OffEffort string   `json:"off_effort,omitempty"`
}

// ModelInfo is discovery data, not a claim that a model passed tool/replay tests.
type ModelInfo struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

func WithClientRequestID(ctx context.Context, id string) context.Context {
	return reqheaders.WithClientRequestID(ctx, id)
}
