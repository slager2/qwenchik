package pdfextract

import (
	"context"
	"errors"
)

type Capability struct {
	State                   string `json:"state"`
	IsolatedAvailable       bool   `json:"isolated_available"`
	PureGoFallbackAvailable bool   `json:"pure_go_fallback_available"`
	SmokeTested             bool   `json:"smoke_tested"`
}

// Availability is a bounded preflight, never a claim that a document parsed.
func (e *Extractor) Capability(ctx context.Context) Capability {
	result := Capability{State: "unavailable"}
	if e == nil || ctx.Err() != nil {
		return result
	}
	result.IsolatedAvailable = e.Check(ctx) == nil
	result.PureGoFallbackAvailable = e.FallbackAvailable()
	if result.IsolatedAvailable {
		result.State = "isolated_available"
	} else if result.PureGoFallbackAvailable {
		result.State = "pure_go_fallback_available"
	}
	return result
}

func Outcome(err error) string {
	switch {
	case err == nil:
		return "extracted"
	case errors.Is(err, ErrPolicyRejected):
		return "policy_rejected"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, ErrInputTooLarge):
		return "source_too_large"
	default:
		return "extraction_failed"
	}
}
