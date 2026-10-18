// Package agent implements Paper Scout's own bounded Chat Completions loop.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"

	"paper-scout/internal/deepseek"
)

// Scope is supplied by the server, never decoded from model arguments. Tools
// must use Owner for every data access. Nested analysis uses Complete so it
// cannot bypass the run's request/token budget or cancellation.
type Scope struct {
	Owner          string
	ConversationID string
	RunID          string
	Complete       func(context.Context, deepseek.Request) (deepseek.Response, error)
}

type Tool struct {
	Definition deepseek.Tool
	// Validate must check required fields, types, sizes and domain limits. Execute
	// receives only validated JSON. Callbacks must honor context and finish all
	// nested calls before returning; Scope must not be retained for background work.
	// Unknown errors are sanitized; ToolError exposes only a stable code.
	Validate func(json.RawMessage) error
	Execute  func(context.Context, Scope, json.RawMessage) (json.RawMessage, error)
}

type ToolError struct{ Code string }

func (e *ToolError) Error() string { return e.Code }

type Registry struct{ tools map[string]Tool }

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		name := t.Definition.Function.Name
		var schema map[string]any
		if t.Definition.Type != "function" || !toolName.MatchString(name) || t.Validate == nil || t.Execute == nil || json.Unmarshal(t.Definition.Function.Parameters, &schema) != nil || schema["type"] != "object" {
			return nil, errors.New("invalid tool registration")
		}
		if _, exists := r.tools[name]; exists {
			return nil, errors.New("duplicate tool registration")
		}
		t.Definition.Function.Parameters = bytes.Clone(t.Definition.Function.Parameters)
		r.tools[name] = t
	}
	return r, nil
}

func (r *Registry) Definitions() []deepseek.Tool {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]deepseek.Tool, 0, len(names))
	for _, name := range names {
		t := r.tools[name].Definition
		t.Function.Parameters = bytes.Clone(t.Function.Parameters)
		out = append(out, t)
	}
	return out
}

// DecodeArgs rejects unknown fields, trailing input, non-objects and null. A
// tool's validator still checks required values and its own semantic limits.
func DecodeArgs(raw json.RawMessage, target any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("invalid arguments")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid arguments")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid arguments")
	}
	return nil
}

func errorResult(code string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code}})
	return b
}
