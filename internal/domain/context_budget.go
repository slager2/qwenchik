package domain

import "time"

const ContextSystemReserve = 2048

// Observations are nullable: configured/training values are never substituted
// for an unavailable server capacity or an unmeasured prompt token count.
type ContextBudget struct {
	Version               int       `json:"version"`
	ConfiguredContext     int       `json:"configured_context"`
	ServerReportedContext *int      `json:"server_reported_context"`
	TrainingContext       *int      `json:"training_context"`
	SessionContext        *int      `json:"session_context"`
	MaxOutputTokens       int       `json:"max_output_tokens"`
	ReservedOutputTokens  int       `json:"reserved_output_tokens"`
	ReservedSystemTokens  int       `json:"reserved_system_tokens"`
	AvailableInputTokens  *int      `json:"available_input_tokens"`
	PromptTokens          *int      `json:"prompt_tokens"`
	ContextStatus         string    `json:"context_status"`
	CountMethod           string    `json:"count_method"`
	Decision              string    `json:"decision"`
	ServerSource          string    `json:"server_source"`
	Reason                string    `json:"reason,omitempty"`
	RuntimeFingerprint    string    `json:"runtime_fingerprint,omitempty"`
	RuntimeBuild          string    `json:"runtime_build,omitempty"`
	ChatTemplateHash      string    `json:"chat_template_sha256,omitempty"`
	ModelQuantization     string    `json:"model_quantization,omitempty"`
	ObservedAt            time.Time `json:"observed_at"`
}

func NewContextBudget(configured, session, output int, server *int) ContextBudget {
	d := ContextBudget{Version: 1, ConfiguredContext: configured, ServerReportedContext: server, MaxOutputTokens: output, ReservedOutputTokens: output, ReservedSystemTokens: ContextSystemReserve, ContextStatus: "unavailable", CountMethod: "unavailable", Decision: "unverified"}
	if session > 0 {
		v := session
		d.SessionContext = &v
	}
	if server == nil {
		return d
	}
	d.ContextStatus = "matched"
	d.Decision = "not_counted"
	effective := min(configured, *server)
	if session > 0 {
		effective = min(effective, session)
	}
	available := max(0, effective-output-ContextSystemReserve)
	d.AvailableInputTokens = &available
	if configured != *server || (session > 0 && session != *server) {
		d.ContextStatus = "context_mismatch"
		d.Decision = "blocked"
		d.Reason = "context_values_differ"
	}
	return d
}
