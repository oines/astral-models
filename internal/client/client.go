package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	astral "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
	"github.com/oines/astral-models/internal/errorformat"
	"github.com/oines/astral-models/internal/utils"
)

type Client struct {
	Config   models.Config
	Protocol string
	Scope    string
}

func New(config models.Config, protocol string) (*Client, error) {
	if config.Spec.ID == "" || config.Model == "" || config.Spec.ContextWindow < 1 || config.Spec.MaxOutputTokens < 1 || config.Spec.MaxInputTokens < 0 {
		return nil, fmt.Errorf("%w: model identity and positive token limits are required", astral.ErrInvalid)
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid provider base URL", astral.ErrInvalid)
	}
	if config.Dialect == "" {
		config.Dialect = models.OpenAI
	}
	if !slices.Contains([]models.Dialect{models.OpenAI, models.DeepSeek, models.Qwen, models.OpenRouter}, config.Dialect) {
		return nil, fmt.Errorf("%w: unsupported dialect %q", astral.ErrInvalid, config.Dialect)
	}
	config.Headers = utils.MergeHeaders(config.Headers)
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	if config.Streaming != nil {
		value := *config.Streaming
		config.Streaming = &value
	}
	if config.Reasoning != nil {
		raw, err := json.Marshal(config.Reasoning)
		if err != nil {
			return nil, err
		}
		config.Reasoning = new(models.ReasoningCapabilities)
		if err := json.Unmarshal(raw, config.Reasoning); err != nil {
			return nil, err
		}
	}
	scope := config.ReplayScope
	if scope == "" {
		headers, _ := json.Marshal(config.Headers)
		digest := sha256.Sum256([]byte(config.BaseURL + "\x00" + config.APIKey + "\x00" + string(headers)))
		scope = hex.EncodeToString(digest[:])
	}
	return &Client{Config: config, Protocol: protocol, Scope: scope}, nil
}

func (c *Client) Spec() astral.ModelSpec { return c.Config.Spec }

func (c *Client) Streaming() bool { return c.Config.Streaming == nil || *c.Config.Streaming }

func (c *Client) Options(ctx context.Context, path string, body any) *utils.RequestOptions {
	var defaults map[string]string
	if c.Config.APIKey != "" {
		defaults = utils.AuthHeader(c.Config.APIKey)
	}
	headers := utils.RequestHeaders(ctx, defaults, c.Config.Headers)
	headers = utils.AddClientRequestID(ctx, headers, utils.ClientRequestIDHeader)
	return &utils.RequestOptions{
		Method: http.MethodPost, BaseURL: c.Config.BaseURL, Path: path,
		Headers: headers, Body: body, Provider: c.Protocol, DecodeError: c.DecodeError,
	}
}

func (c *Client) DecodeError(e *models.APIError) {
	errorformat.DecodeOpenAI(e)
	switch c.Config.Dialect {
	case models.OpenRouter:
		requestID := e.RequestID
		errorformat.DecodeOpenRouter(e)
		if e.RequestID == "" {
			e.RequestID = requestID
		}
	case models.Qwen:
		if e.Message == "" {
			errorformat.DecodeDashScope(e)
		}
	}
}

func (c *Client) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	options := c.Options(ctx, "/models", nil)
	options.Method = http.MethodGet
	data, err := utils.FetchJSON[struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}](ctx, c.Config.HTTPClient, options)
	if err != nil {
		return nil, models.ModelError(err)
	}
	result := make([]models.ModelInfo, 0, len(data.Data))
	seen := map[string]bool{}
	for _, item := range data.Data {
		if item.ID != "" && !seen[item.ID] {
			seen[item.ID] = true
			result = append(result, models.ModelInfo{ID: item.ID, OwnedBy: item.OwnedBy})
		}
	}
	return result, nil
}

func (c *Client) Validate(req astral.Request) error {
	if req.ModelID != c.Config.Spec.ID || (req.ModelRevision != "" && req.ModelRevision != c.Config.Spec.Revision) {
		return fmt.Errorf("%w: request does not match the bound model profile", astral.ErrInvalid)
	}
	if req.MaxOutputTokens < 1 || req.MaxOutputTokens > c.Config.Spec.MaxOutputTokens {
		return fmt.Errorf("%w: output token limit is outside the model profile", astral.ErrInvalid)
	}
	if err := req.Options.Validate(); err != nil {
		return err
	}
	for _, message := range req.Messages {
		if err := message.Validate(); err != nil {
			return err
		}
		if message.Failed() {
			return fmt.Errorf("%w: failed assistant message in model projection", astral.ErrInvalid)
		}
	}
	tools := map[string]bool{}
	for _, tool := range req.Tools {
		if tool.Name == "" || tools[tool.Name] || !IsObject(tool.Parameters) {
			return fmt.Errorf("%w: invalid or duplicate tool definition", astral.ErrInvalid)
		}
		tools[tool.Name] = true
	}
	if choice := req.Options.ToolChoice; choice.Mode == "tool" && !tools[choice.Name] {
		return fmt.Errorf("%w: selected tool is not declared", astral.ErrInvalid)
	}
	for namespace := range req.Options.ProviderOptions {
		if namespace != c.Protocol {
			return fmt.Errorf("%w: options for %q cannot be applied to %q", astral.ErrInvalid, namespace, c.Protocol)
		}
	}
	return c.validateReasoning(req.Options.Reasoning)
}

func (c *Client) validateReasoning(options astral.ReasoningOptions) error {
	capabilities := c.Config.Reasoning
	if capabilities == nil {
		return nil
	}
	if options.Enabled != nil && capabilities.Toggle != nil && !*capabilities.Toggle {
		return fmt.Errorf("%w: this model has no reasoning toggle", astral.ErrInvalid)
	}
	if options.Enabled != nil && !*options.Enabled && capabilities.Disable != nil && !*capabilities.Disable {
		return fmt.Errorf("%w: reasoning cannot be disabled for this model", astral.ErrInvalid)
	}
	if options.Effort != "" && options.Effort != capabilities.OffEffort && capabilities.Efforts != nil && !slices.Contains(capabilities.Efforts, options.Effort) {
		return fmt.Errorf("%w: unsupported reasoning effort %q", astral.ErrInvalid, options.Effort)
	}
	if budget := options.BudgetTokens; budget != nil {
		if capabilities.MinBudget != nil && *budget < *capabilities.MinBudget || capabilities.MaxBudget != nil && *budget > *capabilities.MaxBudget {
			return fmt.Errorf("%w: reasoning budget is outside the supported range", astral.ErrInvalid)
		}
	}
	return nil
}

// ApplyExtensions preserves unmodeled native fields but never lets them replace
// message history, tool declarations, stream controls, or selected parameters.
func (c *Client) ApplyExtensions(body map[string]any, req astral.Request) error {
	raw := req.Options.ProviderOptions[c.Protocol]
	if len(raw) == 0 {
		return nil
	}
	extension, err := Object(raw)
	if err != nil {
		return err
	}
	for key := range extension {
		switch key {
		case "model", "messages", "input", "instructions", "tools", "stream", "stream_options",
			"max_tokens", "max_completion_tokens", "max_output_tokens", "previous_response_id", "conversation",
			"functions", "function_call", "background", "truncation":
			return fmt.Errorf("%w: provider extension %q is owned by the request contract", astral.ErrInvalid, key)
		}
	}
	return merge(body, extension, "")
}

func merge(dst, src map[string]any, prefix string) error {
	for key, value := range src {
		if previous, exists := dst[key]; exists {
			left, lOK := previous.(map[string]any)
			right, rOK := value.(map[string]any)
			if lOK && rOK {
				if err := merge(left, right, prefix+key+"."); err != nil {
					return err
				}
				continue
			}
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(value)
			if string(a) != string(b) {
				return fmt.Errorf("%w: provider extension %q conflicts with selected parameters", astral.ErrInvalid, prefix+key)
			}
		}
		dst[key] = value
	}
	return nil
}

func IsObject(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return strings.HasPrefix(text, "{") && json.Valid(raw)
}

func Object(raw json.RawMessage) (map[string]any, error) {
	if !IsObject(raw) {
		return nil, fmt.Errorf("%w: expected a JSON object", astral.ErrInvalid)
	}
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	err := decoder.Decode(&object)
	return object, err
}
