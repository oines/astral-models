package client

import (
	"encoding/json"
	"fmt"

	astral "github.com/oines/astral-core"
)

type Origin struct {
	Protocol string `json:"protocol"`
	Scope    string `json:"scope"`
	Model    string `json:"model"`
	Field    string `json:"field"`
}

type metadata struct {
	Origin Origin `json:"astral_models"`
}

func (c *Client) Metadata(field string) json.RawMessage {
	data, _ := json.Marshal(metadata{Origin: Origin{Protocol: c.Protocol, Scope: c.Scope, Model: c.Config.Model, Field: field}})
	return data
}

// Replay binds opaque provider state to the connection, protocol, and model
// that issued it. A changed connection must not silently discard that state.
func (c *Client) Replay(part astral.Content, field string) error {
	var value metadata
	if err := json.Unmarshal(part.Metadata, &value); err != nil {
		return fmt.Errorf("%w: opaque model content has no valid replay origin", astral.ErrInvalid)
	}
	if value.Origin.Protocol != c.Protocol || value.Origin.Scope != c.Scope || value.Origin.Model != c.Config.Model || value.Origin.Field != field {
		return fmt.Errorf("%w: opaque model content belongs to a different provider route", astral.ErrInvalid)
	}
	return nil
}

func (c *Client) ResponseMetadata(id, requestID, actualModel, rawFinish string) json.RawMessage {
	result, _ := json.Marshal(struct {
		Protocol     string `json:"protocol"`
		ModelID      string `json:"model_id"`
		Revision     string `json:"revision,omitempty"`
		Model        string `json:"model,omitempty"`
		ResponseID   string `json:"response_id,omitempty"`
		RequestID    string `json:"request_id,omitempty"`
		FinishReason string `json:"finish_reason,omitempty"`
	}{c.Protocol, c.Config.Spec.ID, c.Config.Spec.Revision, actualModel, id, requestID, rawFinish})
	return result
}

func (c *Client) Estimate(body map[string]any, req astral.Request) (int, error) {
	imageCount := 0
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.Type == "image" {
				imageCount++
			}
			if part.Type == "file" {
				return 0, fmt.Errorf("%w: file inputs require a host-provided token estimator", astral.ErrInvalid)
			}
		}
	}
	if imageCount > 0 && c.Config.ImageTokenBudget <= 0 {
		return 0, fmt.Errorf("%w: image inputs require a configured token reservation", astral.ErrInvalid)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	var wire any
	if err := json.Unmarshal(raw, &wire); err != nil {
		return 0, err
	}
	stripImageBytes(wire)
	raw, err = json.Marshal(wire)
	if err != nil {
		return 0, err
	}
	// Byte-based BPE upper estimate. Credentials are not in body. Host callers
	// may replace this conservative estimator with a model-specific tokenizer.
	const overhead = 64
	if imageCount > 0 && c.Config.ImageTokenBudget > (int(^uint(0)>>1)-len(raw)-overhead)/imageCount {
		return 0, fmt.Errorf("%w: image token reservation overflow", astral.ErrInvalid)
	}
	return len(raw) + overhead + imageCount*c.Config.ImageTokenBudget, nil
}

func stripImageBytes(value any) {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			stripImageBytes(child)
		}
	case map[string]any:
		if node["type"] == "base64" && node["media_type"] != nil {
			node["data"] = "[image]"
		}
		if inline, ok := node["inlineData"].(map[string]any); ok {
			inline["data"] = "[image]"
		}
		if node["type"] == "image_url" || node["type"] == "input_image" {
			if _, ok := node["image_url"]; ok {
				node["image_url"] = "[image]"
			}
		}
		for _, child := range node {
			stripImageBytes(child)
		}
	}
}
