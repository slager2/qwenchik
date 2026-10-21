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

	"github.com/slager2/qwenchik/internal/policy"
)

var (
	ErrUnknownTool      = errors.New("unknown tool")
	ErrToolDenied       = errors.New("tool is not allowed for this role")
	ErrApprovalRequired = errors.New("tool requires an approval")
	ErrUnknownTarget    = errors.New("tool target is not in the approved task context")
	ErrInvalidArguments = errors.New("tool arguments did not pass validation")
	ErrInvalidOutput    = errors.New("tool output did not pass validation")
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
type ArgumentsValidator func(context.Context, json.RawMessage) error

type OperationalValidationError interface {
	error
	OperationalValidationFailure() bool
}

type executionParentContextKey struct{}

func executionContextError(ctx context.Context) error {
	if parent, ok := ctx.Value(executionParentContextKey{}).(context.Context); ok {
		if err := parent.Err(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type registeredTool struct {
	descriptor Descriptor
	executor   Executor
	validator  ArgumentsValidator
}

type Registry struct {
	byName    map[string]registeredTool
	evaluator *policy.Evaluator
	urlLedger *URLLedger
	cleanup   []func(context.Context, string) error
}

func (r *Registry) RegisterTaskCleanup(cleanup func(context.Context, string) error) {
	if cleanup != nil {
		r.cleanup = append(r.cleanup, cleanup)
	}
}

func (r *Registry) CleanupTask(ctx context.Context, taskID string) error {
	r.ClearTaskURLs(taskID)
	var firstErr error
	for _, cleanup := range r.cleanup {
		if err := cleanup(ctx, taskID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func NewRegistry(evaluators ...*policy.Evaluator) *Registry {
	evaluator := policy.NewDefaultEvaluator()
	if len(evaluators) > 0 && evaluators[0] != nil {
		evaluator = evaluators[0]
	}
	registry := &Registry{byName: make(map[string]registeredTool), evaluator: evaluator}
	registry.MustRegisterValidated(Descriptor{
		Name: "text.inspect", Description: "Count characters, words, and lines in bounded text.",
		ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","maxLength":20000}},"required":["text"],"additionalProperties":false}`),
		OutputSchema:       json.RawMessage(`{"type":"object","properties":{"characters":{"type":"integer"},"words":{"type":"integer"},"lines":{"type":"integer"}},"required":["characters","words","lines"],"additionalProperties":false}`),
		RequiredCapability: "task.text.inspect", RiskClass: "read", TimeoutSeconds: 2,
		Idempotent: true, MaxOutputBytes: 1024, AllowedRoles: []string{"analyst", "verifier"},
	}, validateInspectArgs, inspectText)
	return registry
}

func (r *Registry) MustRegister(descriptor Descriptor, executor Executor) {
	if err := r.Register(descriptor, executor); err != nil {
		panic(err)
	}
}

func (r *Registry) MustRegisterValidated(descriptor Descriptor, validator ArgumentsValidator, executor Executor) {
	if err := r.RegisterValidated(descriptor, validator, executor); err != nil {
		panic(err)
	}
}

func (r *Registry) Register(descriptor Descriptor, executor Executor) error {
	return r.RegisterValidated(descriptor, nil, executor)
}

func (r *Registry) RegisterValidated(descriptor Descriptor, validator ArgumentsValidator, executor Executor) error {
	if descriptor.Name == "" || executor == nil || descriptor.MaxOutputBytes < 1 || descriptor.TimeoutSeconds < 1 || !json.Valid(descriptor.ArgumentsSchema) || !json.Valid(descriptor.OutputSchema) {
		return errors.New("tool descriptor is incomplete")
	}
	if _, exists := r.byName[descriptor.Name]; exists {
		return fmt.Errorf("tool %q already registered", descriptor.Name)
	}
	r.byName[descriptor.Name] = registeredTool{descriptor: descriptor, executor: executor, validator: validator}
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

func (r *Registry) DescriptorsForRole(role, riskClass string) []Descriptor {
	result := make([]Descriptor, 0)
	for _, tool := range r.byName {
		if riskClass != "" && tool.descriptor.RiskClass != riskClass {
			continue
		}
		for _, allowedRole := range tool.descriptor.AllowedRoles {
			if allowedRole == role {
				result = append(result, tool.descriptor)
				break
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) HasRiskForRole(role, riskClass string) bool {
	return len(r.DescriptorsForRole(role, riskClass)) > 0
}

func (r *Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

func (r *Registry) Descriptor(name string) (Descriptor, bool) {
	tool, ok := r.byName[name]
	return tool.descriptor, ok
}

func (r *Registry) SeedTaskURLs(taskID, text string) {
	if r.urlLedger != nil {
		r.urlLedger.SeedText(taskID, text)
	}
}

func (r *Registry) KnownTaskURLs(taskID string) []string {
	if r.urlLedger == nil {
		return []string{}
	}
	return r.urlLedger.URLs(taskID)
}

func (r *Registry) FetchedTaskURLs(taskID string) []string {
	if r.urlLedger == nil {
		return []string{}
	}
	return r.urlLedger.FetchedURLs(taskID)
}

func (r *Registry) WasTaskURLFetched(taskID, rawURL string) bool {
	return r.urlLedger != nil && r.urlLedger.WasFetched(taskID, rawURL)
}

func (r *Registry) RememberURLs(taskID string, value any) {
	if r.urlLedger != nil {
		r.urlLedger.Remember(taskID, value)
	}
}

func (r *Registry) RememberFetchedURLs(taskID string, value any) {
	if r.urlLedger == nil {
		return
	}
	var visit func(any)
	visit = func(current any) {
		switch item := current.(type) {
		case map[string]any:
			if evidenceID, ok := item["evidence_id"].(string); ok && evidenceID != "" {
				for _, key := range []string{"url", "requested_url"} {
					if rawURL, ok := item[key].(string); ok {
						r.urlLedger.MarkFetched(taskID, rawURL)
					}
				}
			}
			for _, child := range item {
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		}
	}
	visit(value)
}

func (r *Registry) ClearTaskURLs(taskID string) {
	if r.urlLedger != nil {
		r.urlLedger.Clear(taskID)
	}
}

func (r *Registry) Decide(role, name string) (Descriptor, policy.Decision) {
	tool, ok := r.byName[name]
	if !ok {
		return Descriptor{Name: name}, policy.Decision{Action: policy.Deny, Reason: "tool is not registered"}
	}
	descriptor := tool.descriptor
	decision := r.evaluator.Evaluate(role, policy.Tool{
		Name: descriptor.Name, RequiredCapability: descriptor.RequiredCapability,
		RiskClass: descriptor.RiskClass, Idempotent: descriptor.Idempotent,
		AllowedRoles: descriptor.AllowedRoles,
	})
	return descriptor, decision
}

func (r *Registry) Authorize(ctx context.Context, role, name string, arguments json.RawMessage) (Descriptor, policy.Decision, error) {
	tool, ok := r.byName[name]
	if !ok {
		return Descriptor{Name: name}, policy.Decision{Action: policy.Deny, Reason: "tool is not registered"}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	descriptor := tool.descriptor
	roleAllowed := false
	for _, allowedRole := range descriptor.AllowedRoles {
		if allowedRole == role {
			roleAllowed = true
			break
		}
	}
	if !roleAllowed {
		_, decision := r.Decide(role, name)
		return descriptor, decision, nil
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, time.Duration(descriptor.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		decision := policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool authorization timed out or was cancelled"}
		return descriptor, decision, err
	}
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		decision := policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "arguments are not a JSON object"}
		return descriptor, decision, ErrInvalidArguments
	}
	if err := ValidateJSONSchema(descriptor.ArgumentsSchema, trimmed); err != nil {
		decision := policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "arguments did not match the registered schema"}
		return descriptor, decision, errors.Join(ErrInvalidArguments, err)
	}
	if tool.validator != nil {
		if err := tool.validator(ctx, trimmed); err != nil {
			if parent.Err() != nil {
				return descriptor, policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool authorization timed out or was cancelled"}, parent.Err()
			}
			if ctx.Err() != nil {
				return descriptor, policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool authorization timed out or was cancelled"}, ctx.Err()
			}
			var operational OperationalValidationError
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &operational) && operational.OperationalValidationFailure() {
				return descriptor, policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool validation dependency is unavailable"}, err
			}
			decision := policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "arguments failed backend validation"}
			return descriptor, decision, errors.Join(ErrInvalidArguments, err)
		}
	}
	if parent.Err() != nil {
		return descriptor, policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool authorization timed out or was cancelled"}, parent.Err()
	}
	if ctx.Err() != nil {
		return descriptor, policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool authorization timed out or was cancelled"}, ctx.Err()
	}
	_, decision := r.Decide(role, name)
	return descriptor, decision, nil
}

func DecisionError(name string, decision policy.Decision) error {
	switch decision.Action {
	case policy.ApprovalRequired:
		return fmt.Errorf("%w: %s", ErrApprovalRequired, name)
	default:
		return fmt.Errorf("%w: %s (%s)", ErrToolDenied, name, decision.Reason)
	}
}

func (r *Registry) Execute(ctx context.Context, role, name string, arguments json.RawMessage) (any, error) {
	return r.execute(ctx, role, name, arguments, "")
}

func (r *Registry) ExecuteApproved(ctx context.Context, role, name string, arguments json.RawMessage, approvalID string) (any, error) {
	if approvalID == "" {
		return nil, ErrApprovalRequired
	}
	return r.execute(ctx, role, name, arguments, approvalID)
}

func (r *Registry) execute(ctx context.Context, role, name string, arguments json.RawMessage, approvalID string) (any, error) {
	tool, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	_, decision, err := r.Authorize(ctx, role, name, arguments)
	if err != nil {
		return nil, err
	}
	switch decision.Action {
	case policy.Allow:
	case policy.ApprovalRequired:
		if approvalID == "" {
			return nil, DecisionError(name, decision)
		}
	default:
		return nil, DecisionError(name, decision)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, time.Duration(tool.descriptor.TimeoutSeconds)*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, executionParentContextKey{}, parent)
	if approvalID != "" {
		ctx = context.WithValue(ctx, approvalContextKey{}, approvalID)
	}
	if err := executionContextError(ctx); err != nil {
		return nil, err
	}
	result, err := tool.executor(ctx, arguments)
	if err != nil {
		if contextErr := executionContextError(ctx); contextErr != nil && !errors.Is(err, contextErr) {
			return nil, errors.Join(err, contextErr)
		}
		return nil, err
	}
	if contextErr := executionContextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode tool output: %w", err)
	}
	if len(encoded) > tool.descriptor.MaxOutputBytes {
		return nil, errors.New("tool output exceeds configured limit")
	}
	if err := ValidateJSONSchema(tool.descriptor.OutputSchema, encoded); err != nil {
		return nil, errors.Join(ErrInvalidOutput, err)
	}
	return result, nil
}

type approvalContextKey struct{}

func ApprovalIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(approvalContextKey{}).(string)
	return value
}

type inspectArgs struct {
	Text *string `json:"text"`
}

func inspectText(_ context.Context, raw json.RawMessage) (any, error) {
	args, err := parseInspectArgs(raw)
	if err != nil {
		return nil, err
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

func validateInspectArgs(ctx context.Context, raw json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := parseInspectArgs(raw)
	return err
}

func parseInspectArgs(raw json.RawMessage) (inspectArgs, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var args inspectArgs
	if err := decoder.Decode(&args); err != nil {
		return inspectArgs{}, fmt.Errorf("invalid text.inspect arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return inspectArgs{}, errors.New("text.inspect accepts only one JSON object")
	}
	if args.Text == nil {
		return inspectArgs{}, errors.New("text.inspect requires a text string")
	}
	if len(*args.Text) > 20000 {
		return inspectArgs{}, errors.New("text.inspect text exceeds 20000 bytes")
	}
	return args, nil
}
