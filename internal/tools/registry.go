package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrUnknownTool = errors.New("unknown tool")
	ErrToolDenied  = errors.New("tool is not allowed for this role")
)

type Descriptor struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	ArgumentsSchema    json.RawMessage `json:"arguments_schema"`
	OutputSchema       json.RawMessage `json:"output_schema"`
	RequiredCapability string          `json:"required_capability"`
	RiskClass          string          `json:"risk_class"`
	TimeoutSeconds     int             `json:"timeout_seconds"`
	Idempotent         bool            `json:"idempotent"`
	MaxOutputBytes     int             `json:"max_output_bytes"`
	AllowedRoles       []string        `json:"allowed_roles"`
}

type Executor func(context.Context, json.RawMessage) (any, error)

type registeredTool struct {
	descriptor Descriptor
	executor   Executor
}

type Registry struct {
	byName map[string]registeredTool
}

func NewRegistry() *Registry {
	registry := &Registry{byName: make(map[string]registeredTool)}
	registry.MustRegister(Descriptor{
		Name: "text.inspect", Description: "Count characters, words, and lines in bounded text.",
		ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","maxLength":20000}},"required":["text"],"additionalProperties":false}`),
		OutputSchema:       json.RawMessage(`{"type":"object","properties":{"characters":{"type":"integer"},"words":{"type":"integer"},"lines":{"type":"integer"}},"required":["characters","words","lines"],"additionalProperties":false}`),
		RequiredCapability: "task.text.inspect", RiskClass: "read", TimeoutSeconds: 2,
		Idempotent: true, MaxOutputBytes: 1024, AllowedRoles: []string{"analyst", "verifier"},
	}, inspectText)
	return registry
}

func (r *Registry) MustRegister(descriptor Descriptor, executor Executor) {
	if err := r.Register(descriptor, executor); err != nil {
		panic(err)
	}
}

func (r *Registry) Register(descriptor Descriptor, executor Executor) error {
	if descriptor.Name == "" || executor == nil || descriptor.MaxOutputBytes < 1 || descriptor.TimeoutSeconds < 1 || !json.Valid(descriptor.ArgumentsSchema) || !json.Valid(descriptor.OutputSchema) {
		return errors.New("tool descriptor is incomplete")
	}
	if _, exists := r.byName[descriptor.Name]; exists {
		return fmt.Errorf("tool %q already registered", descriptor.Name)
	}
	r.byName[descriptor.Name] = registeredTool{descriptor: descriptor, executor: executor}
	return nil
}

func (r *Registry) Descriptors() []Descriptor {
	result := make([]Descriptor, 0, len(r.byName))
	for _, tool := range r.byName {
		result = append(result, tool.descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) Execute(ctx context.Context, role, name string, arguments json.RawMessage) (any, error) {
	tool, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	allowed := false
	for _, candidate := range tool.descriptor.AllowedRoles {
		if candidate == role {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("%w: %s cannot call %s", ErrToolDenied, role, name)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(tool.descriptor.TimeoutSeconds)*time.Second)
	defer cancel()
	result, err := tool.executor(ctx, arguments)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode tool output: %w", err)
	}
	if len(encoded) > tool.descriptor.MaxOutputBytes {
		return nil, errors.New("tool output exceeds configured limit")
	}
	return result, nil
}

type inspectArgs struct {
	Text *string `json:"text"`
}

func inspectText(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var args inspectArgs
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("invalid text.inspect arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("text.inspect accepts only one JSON object")
	}
	if args.Text == nil {
		return nil, errors.New("text.inspect requires a text string")
	}
	if len(*args.Text) > 20000 {
		return nil, errors.New("text.inspect text exceeds 20000 bytes")
	}
	text := *args.Text
	lines := 0
	if text != "" {
		lines = strings.Count(text, "\n") + 1
	}
	return map[string]any{
		"characters": utf8.RuneCountInString(text),
		"words":      len(strings.Fields(text)),
		"lines":      lines,
	}, nil
}
