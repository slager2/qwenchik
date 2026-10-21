package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/slager2/qwenchik/internal/policy"
)

type unavailableValidationFixture struct{ cause error }

func (e unavailableValidationFixture) Error() string                      { return "validation dependency unavailable" }
func (e unavailableValidationFixture) Unwrap() error                      { return e.cause }
func (e unavailableValidationFixture) OperationalValidationFailure() bool { return true }

func TestOperationalValidationDoesNotBecomeInvalidArguments(t *testing.T) {
	for _, mode := range []string{"dependency", "cancel_during_validation", "cancel_after_success", "child_deadline", "invalid_arguments", "schema_first"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("fixture failure")
			registry := NewRegistry()
			runs, validations := 0, 0
			if err := registry.RegisterValidated(Descriptor{Name: "validation.fixture", Description: "Test validation", ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object"}`), RequiredCapability: "text.inspect", RiskClass: "read", TimeoutSeconds: 1, MaxOutputBytes: 100, AllowedRoles: []string{"analyst"}}, func(context.Context, json.RawMessage) error {
				validations++
				switch mode {
				case "cancel_during_validation":
					cancel()
					return cause
				case "cancel_after_success":
					cancel()
					return nil
				case "child_deadline":
					return context.DeadlineExceeded
				case "invalid_arguments":
					return cause
				default:
					return unavailableValidationFixture{cause: cause}
				}
			}, func(context.Context, json.RawMessage) (any, error) { runs++; return map[string]any{}, nil }); err != nil {
				t.Fatal(err)
			}
			arguments := json.RawMessage(`{"value":"valid"}`)
			if mode == "schema_first" {
				arguments = json.RawMessage(`{"unexpected":true}`)
			}
			_, decision, err := registry.Authorize(ctx, "analyst", "validation.fixture", arguments)
			if err == nil || decision.Action != policy.Deny {
				t.Fatalf("authorization=%+v err=%v", decision, err)
			}
			wantInvalid := mode == "schema_first" || mode == "invalid_arguments"
			if errors.Is(err, ErrInvalidArguments) != wantInvalid {
				t.Fatalf("misclassified validation error: %v", err)
			}
			if (mode == "cancel_during_validation" || mode == "cancel_after_success") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if mode == "child_deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", err)
			}
			if mode == "dependency" && !errors.Is(err, cause) {
				t.Fatalf("dependency cause lost: %v", err)
			}
			if mode == "schema_first" && validations != 0 {
				t.Fatal("schema failure reached dependency validator")
			}
			if runs != 0 {
				t.Fatal("failed authorization invoked executor")
			}
		})
	}
}
