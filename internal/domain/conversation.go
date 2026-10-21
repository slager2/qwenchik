package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var ErrInvalidConversation = errors.New("invalid conversation request")

type Conversation struct {
	ID               string         `json:"id"`
	Title            string         `json:"title"`
	Mode             string         `json:"mode"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	ActiveRunID      string         `json:"active_run_id,omitempty"`
	LatestRunID      string         `json:"latest_run_id,omitempty"`
	ArchivedAt       *time.Time     `json:"archived_at,omitempty"`
	ModelProfile     map[string]any `json:"model_profile,omitempty"`
	ResearchSettings map[string]any `json:"research_settings,omitempty"`
}

type ConversationMessage struct {
	ID              string            `json:"id"`
	ConversationID  string            `json:"conversation_id"`
	RunID           string            `json:"run_id"`
	Role            string            `json:"role"`
	Content         string            `json:"content"`
	Status          string            `json:"status"`
	ParentMessageID string            `json:"parent_message_id,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	LastEventSeq    int64             `json:"last_event_seq"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	Citations       []json.RawMessage `json:"citations,omitempty"`
}

type Run struct {
	PauseReason           string     `json:"pause_reason,omitempty"`
	ResourceReason        string     `json:"resource_reason,omitempty"`
	ResumeAfter           *time.Time `json:"resume_after,omitempty"`
	ResourceProbeAttempts int        `json:"resource_probe_attempts"`
	TerminalOutcome       string     `json:"terminal_outcome,omitempty"`
	ID                    string     `json:"id"`
	ConversationID        string     `json:"conversation_id"`
	UserMessageID         string     `json:"user_message_id"`
	AssistantMessageID    string     `json:"assistant_message_id"`
	Mode                  string     `json:"mode"`
	Operation             string     `json:"operation"`
	ContinueFromMessageID string     `json:"continue_from_message_id,omitempty"`
	Status                TaskStatus `json:"status"`
	Stage                 *Stage     `json:"stage,omitempty"`
	ErrorCode             string     `json:"error_code,omitempty"`
	ErrorMessage          string     `json:"error_message,omitempty"`
	RetryCount            int        `json:"retry_count"`
	CancelRequested       bool       `json:"cancel_requested"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	CompletedAt           *time.Time `json:"completed_at,omitempty"`
	HeartbeatAt           *time.Time `json:"heartbeat_at,omitempty"`
	DeadlineAt            *time.Time `json:"deadline_at,omitempty"`
}

type SubmitTurn struct {
	ConversationID  string                `json:"conversation_id"`
	Content         string                `json:"content"`
	Mode            string                `json:"mode"`
	ParentMessageID string                `json:"parent_message_id,omitempty"`
	Operation       string                `json:"operation,omitempty"`
	SourceRunID     string                `json:"source_run_id,omitempty"`
	DeadlineSeconds int                   `json:"-"`
	ResearchProfile string                `json:"-"`
	ResearchPolicy  *ResearchPolicy       `json:"-"`
	ResearchBudget  *ResearchBudget       `json:"-"`
	ModelSnapshot   *ModelProfileSnapshot `json:"-"`
}

type SubmitResponse struct {
	ConversationID   string              `json:"conversation_id"`
	UserMessage      ConversationMessage `json:"user_message"`
	AssistantMessage ConversationMessage `json:"assistant_message"`
	Run              Run                 `json:"run"`
}
