package completions

import (
	"encoding/json"
	"fmt"
	"strings"

	astral "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
	"github.com/oines/astral-models/internal/client"
)

// RequestBody is the exact credential-free JSON body used for this request.
func (m *Model) RequestBody(req astral.Request) (map[string]any, error) {
	if err := m.client.Validate(req); err != nil {
		return nil, err
	}
	messages := []any{}
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	var toolImages []any
	flushImages := func() {
		if len(toolImages) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": toolImages})
			toolImages = nil
		}
	}
	for _, message := range req.Messages {
		if message.Role != astral.RoleTool {
			flushImages()
		}
		if message.Role == astral.RoleAssistant {
			value, err := m.assistant(message)
			if err != nil {
				return nil, err
			}
			messages = append(messages, value)
			continue
		}
		if message.Role == astral.RoleTool {
			var text strings.Builder
			for _, part := range message.Content {
				switch part.Type {
				case "text":
					text.WriteString(part.Text)
				case "image":
					if part.URL == "" {
						return nil, fmt.Errorf("%w: tool image requires a resolved URL", astral.ErrInvalid)
					}
					text.WriteString("\n[Image returned by this tool; attached after the tool-result batch.]")
					toolImages = append(toolImages, map[string]any{"type": "text", "text": "Image from tool call " + message.ToolCallID + ":"},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": part.URL}})
				default:
					return nil, fmt.Errorf("%w: unsupported tool output part %q", astral.ErrInvalid, part.Type)
				}
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": message.ToolCallID, "content": text.String()})
			continue
		}
		parts, err := inputParts(message.Content)
		if err != nil {
			return nil, err
		}
		role := string(message.Role)
		if message.Role == astral.RoleDeveloper && (m.client.Config.Dialect == models.DeepSeek || m.client.Config.Dialect == models.Qwen) {
			role = "system"
		}
		messages = append(messages, map[string]any{"role": role, "content": parts})
	}
	flushImages()
	body := map[string]any{"model": m.client.Config.Model, "messages": messages, "stream": m.client.Streaming(), "n": 1}
	if m.client.Config.Dialect == models.DeepSeek || m.client.Config.Dialect == models.Qwen || m.client.Config.Dialect == models.Compatible || m.client.Config.Dialect == models.MiMo {
		body["max_tokens"] = req.MaxOutputTokens
	} else {
		body["max_completion_tokens"] = req.MaxOutputTokens
	}
	if m.client.Streaming() {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
			}})
		}
		body["tools"] = tools
	}
	options := req.Options
	if options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}
	if options.TopP != nil {
		body["top_p"] = *options.TopP
	}
	if options.Seed != nil {
		body["seed"] = *options.Seed
	}
	if options.FrequencyPenalty != nil {
		body["frequency_penalty"] = *options.FrequencyPenalty
	}
	if options.PresencePenalty != nil {
		body["presence_penalty"] = *options.PresencePenalty
	}
	if options.Stop != nil {
		body["stop"] = options.Stop
	}
	if choice := options.ToolChoice; choice.Mode != "" {
		if choice.Mode == "tool" {
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}
		} else {
			body["tool_choice"] = choice.Mode
		}
	}
	if format := options.ResponseFormat; format != nil {
		wire := map[string]any{"type": format.Type}
		if format.Type == "json_schema" {
			schema := map[string]any{"name": format.Name, "schema": format.Schema}
			if format.Strict != nil {
				schema["strict"] = *format.Strict
			}
			wire["json_schema"] = schema
		}
		body["response_format"] = wire
	}
	if err := m.reasoning(body, options.Reasoning); err != nil {
		return nil, err
	}
	if err := m.client.ApplyExtensions(body, req); err != nil {
		return nil, err
	}
	return body, nil
}

func inputParts(parts []astral.Content) ([]any, error) {
	result := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			result = append(result, map[string]any{"type": "text", "text": part.Text})
		case "image":
			if part.URL == "" {
				return nil, fmt.Errorf("%w: image requires a resolved URL or data URL", astral.ErrInvalid)
			}
			result = append(result, map[string]any{"type": "image_url", "image_url": map[string]any{"url": part.URL}})
		default:
			return nil, fmt.Errorf("%w: unsupported chat input part %q", astral.ErrInvalid, part.Type)
		}
	}
	return result, nil
}

func (m *Model) replayField() string {
	if m.client.Config.ReasoningReplay != "" {
		return m.client.Config.ReasoningReplay
	}
	if m.client.Config.Dialect == models.OpenRouter {
		return "reasoning_details"
	}
	return "reasoning_content"
}

func (m *Model) assistant(message astral.Message) (map[string]any, error) {
	wire := map[string]any{"role": "assistant", "content": nil}
	var text strings.Builder
	var calls []any
	for _, part := range message.Content {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "refusal":
			wire["refusal"] = part.Text
		case "reasoning":
			if err := m.client.Replay(part, "chat.reasoning"); err != nil {
				return nil, err
			}
			reasoning, err := client.Object(part.Data)
			if err != nil {
				return nil, err
			}
			field := m.replayField()
			if details, ok := reasoning["reasoning_details"].([]any); ok && field != "reasoning_details" {
				for _, detail := range details {
					if item, ok := detail.(map[string]any); ok && item["type"] == "reasoning.encrypted" {
						return nil, fmt.Errorf("%w: encrypted reasoning requires reasoning_details replay", astral.ErrInvalid)
					}
				}
			}
			if value, exists := reasoning[field]; exists {
				wire[field] = value
			} else if field == "reasoning_details" {
				return nil, fmt.Errorf("%w: reasoning_details are missing from this model response", astral.ErrInvalid)
			} else {
				wire[field] = part.Text
			}
		case "tool_call":
			call := part.ToolCall
			calls = append(calls, map[string]any{"id": call.ID, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)}})
		case "image":
			if err := m.client.Replay(part, "chat.image"); err != nil {
				return nil, err
			}
			images, _ := wire["images"].([]any)
			wire["images"] = append(images, json.RawMessage(part.Data))
		default:
			return nil, fmt.Errorf("%w: unsupported assistant part %q", astral.ErrInvalid, part.Type)
		}
	}
	if text.Len() > 0 || len(calls) == 0 {
		wire["content"] = text.String()
	}
	if len(calls) > 0 {
		wire["tool_calls"] = calls
		if m.client.Config.RequireReasoningReplay || m.client.Config.Dialect == models.DeepSeek {
			field := m.replayField()
			if _, exists := wire[field]; !exists {
				if field == "reasoning_details" {
					return nil, fmt.Errorf("%w: tool-call message lacks required reasoning_details", astral.ErrInvalid)
				}
				wire[field] = ""
			}
		}
	}
	return wire, nil
}

func (m *Model) reasoning(body map[string]any, options astral.ReasoningOptions) error {
	switch m.client.Config.Dialect {
	case models.DeepSeek, models.MiMo:
		if options.Enabled != nil {
			value := "disabled"
			if *options.Enabled {
				value = "enabled"
			}
			body["thinking"] = map[string]any{"type": value}
		}
		if options.Effort != "" {
			body["reasoning_effort"] = options.Effort
		}
		if options.BudgetTokens != nil {
			return fmt.Errorf("%w: DeepSeek Chat does not expose a thinking token budget", astral.ErrInvalid)
		}
	case models.Qwen:
		if options.Enabled != nil {
			body["enable_thinking"] = *options.Enabled
		}
		if options.BudgetTokens != nil {
			body["thinking_budget"] = *options.BudgetTokens
		}
		if options.Effort != "" {
			return fmt.Errorf("%w: Qwen Chat uses thinking_budget, not a universal effort scale", astral.ErrInvalid)
		}
	case models.OpenRouter:
		reasoning := map[string]any{}
		if options.Enabled != nil {
			reasoning["enabled"] = *options.Enabled
		}
		if options.Effort != "" {
			reasoning["effort"] = options.Effort
		}
		if options.BudgetTokens != nil {
			if options.Effort != "" {
				return fmt.Errorf("%w: OpenRouter accepts effort or max_tokens, not both", astral.ErrInvalid)
			}
			reasoning["max_tokens"] = *options.BudgetTokens
		}
		if len(reasoning) > 0 {
			body["reasoning"] = reasoning
		}
	default:
		if options.BudgetTokens != nil {
			return fmt.Errorf("%w: OpenAI Chat has no thinking token budget", astral.ErrInvalid)
		}
		effort := options.Effort
		if options.Enabled != nil {
			capabilities := m.client.Config.Reasoning
			if *options.Enabled {
				if capabilities != nil && effort != "" && effort == capabilities.OffEffort {
					return fmt.Errorf("%w: reasoning-on conflicts with the off effort", astral.ErrInvalid)
				}
				if effort == "" && (capabilities == nil || capabilities.DefaultOn == nil || !*capabilities.DefaultOn) {
					return fmt.Errorf("%w: enabling reasoning needs an explicit effort or a verified reasoning default", astral.ErrInvalid)
				}
			} else {
				if capabilities == nil || capabilities.OffEffort == "" {
					return fmt.Errorf("%w: this profile has no known reasoning-off value", astral.ErrInvalid)
				}
				if effort != "" && effort != capabilities.OffEffort {
					return fmt.Errorf("%w: reasoning-off conflicts with selected effort", astral.ErrInvalid)
				}
				effort = capabilities.OffEffort
			}
		}
		if effort != "" {
			body["reasoning_effort"] = effort
		}
	}
	return nil
}
