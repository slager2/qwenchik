package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
	ApprovalExpired  ApprovalStatus = "expired"
)

type TaskStatus string

const (
	StatusInbox           TaskStatus = "inbox"
	StatusQueued          TaskStatus = "queued"
	StatusPlanning        TaskStatus = "planning"
	StatusRunning         TaskStatus = "running"
	StatusWaitingApproval TaskStatus = "waiting_approval"
	StatusPaused          TaskStatus = "paused"
	StatusVerifying       TaskStatus = "verifying"
	StatusCompleted       TaskStatus = "completed"
	StatusFailed          TaskStatus = "failed"
	StatusCancelled       TaskStatus = "cancelled"
)

type Stage string

const (
	StageIntake        Stage = "intake"
	StagePlanning      Stage = "planning"
	StageSearch        Stage = "search"
	StageFetch         Stage = "fetch"
	StageExtract       Stage = "extract"
	StageBrowser       Stage = "browser"
	StageCodeExecution Stage = "code_execution"
	StageSynthesis     Stage = "synthesis"
	StageVerification  Stage = "verification"
	StageFinalization  Stage = "finalization"
)

var ErrInvalidTransition = errors.New("invalid task status transition")
var ErrUnsupportedPlanKind = errors.New("unsupported plan step kind")
var ErrInvalidPlan = errors.New("invalid plan")

var planStepIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

var transitions = map[TaskStatus]map[TaskStatus]struct{}{
	StatusInbox:           {StatusQueued: {}, StatusCancelled: {}},
	StatusQueued:          {StatusPlanning: {}, StatusPaused: {}, StatusCancelled: {}},
	StatusPlanning:        {StatusRunning: {}, StatusPaused: {}, StatusFailed: {}, StatusCancelled: {}},
	StatusRunning:         {StatusWaitingApproval: {}, StatusPaused: {}, StatusVerifying: {}, StatusFailed: {}, StatusCancelled: {}},
	StatusWaitingApproval: {StatusRunning: {}, StatusQueued: {}, StatusPaused: {}, StatusFailed: {}, StatusCancelled: {}},
	StatusPaused:          {StatusQueued: {}, StatusRunning: {}, StatusCancelled: {}},
	StatusVerifying:       {StatusCompleted: {}, StatusFailed: {}, StatusPaused: {}, StatusCancelled: {}},
	StatusFailed:          {StatusQueued: {}},
	StatusCompleted:       {},
	StatusCancelled:       {},
}

func (s TaskStatus) Valid() bool {
	_, ok := transitions[s]
	return ok
}

func CanTransition(from, to TaskStatus) bool {
	if from == to && (from == StatusRunning || from == StatusVerifying) {
		return true
	}
	allowed, ok := transitions[from]
	if !ok {
		return false
	}
	_, ok = allowed[to]
	return ok
}

func (s TaskStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

type Task struct {
	Lease                 *ExecutionLease `json:"lease,omitempty"`
	PauseReason           string          `json:"pause_reason,omitempty"`
	ResourceReason        string          `json:"resource_reason,omitempty"`
	ResumeAfter           *time.Time      `json:"resume_after,omitempty"`
	ResourceProbeAttempts int             `json:"resource_probe_attempts"`
	TerminalOutcome       string          `json:"terminal_outcome,omitempty"`
	ID                    string          `json:"id"`
	Title                 string          `json:"title"`
	Description           string          `json:"description"`
	Status                TaskStatus      `json:"status"`
	Stage                 *Stage          `json:"stage,omitempty"`
	Priority              int             `json:"priority"`
	Plan                  *Plan           `json:"plan,omitempty"`
	Version               int64           `json:"version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type Message struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type TaskStep struct {
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	StepID          string    `json:"step_id"`
	Position        int       `json:"position"`
	Kind            string    `json:"kind"`
	Description     string    `json:"description"`
	Dependencies    []string  `json:"dependencies"`
	ExpectedOutput  string    `json:"expected_output"`
	SuccessCriteria []string  `json:"success_criteria"`
	Status          string    `json:"status"`
	Attempts        int       `json:"attempts"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Artifact struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mime_type"`
	SHA256    string    `json:"sha256"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type QuoteSpan struct {
	CoordinateSpace string `json:"coordinate_space,omitempty"`
	Start           int    `json:"start"`
	End             int    `json:"end"`
	Page            int    `json:"page,omitempty"`
	Text            string `json:"text"`
}

type Evidence struct {
	ID               string      `json:"id"`
	TaskID           string      `json:"task_id"`
	Provider         string      `json:"provider"`
	OriginalQuery    string      `json:"original_query"`
	OriginalURL      string      `json:"original_url"`
	CanonicalURL     string      `json:"canonical_url"`
	Title            string      `json:"title"`
	Language         string      `json:"language"`
	RetrievedAt      time.Time   `json:"retrieved_at"`
	HTTPStatus       int         `json:"http_status"`
	ContentSHA256    string      `json:"content_sha256"`
	ExtractionMethod string      `json:"extraction_method"`
	RawArtifactID    string      `json:"raw_artifact_id,omitempty"`
	TextArtifactID   string      `json:"text_artifact_id,omitempty"`
	Excerpt          string      `json:"excerpt"`
	QuoteSpans       []QuoteSpan `json:"quote_spans"`
	QualityFlags     []string    `json:"quality_flags"`
	Taint            string      `json:"taint"`
}

type Approval struct {
	ID              string          `json:"id"`
	TaskID          string          `json:"task_id"`
	ToolName        string          `json:"tool_name"`
	Arguments       json.RawMessage `json:"arguments"`
	ArgumentsSHA256 string          `json:"-"`
	Target          string          `json:"target"`
	RiskExplanation string          `json:"risk_explanation"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Status          ApprovalStatus  `json:"status"`
	CreatedAt       time.Time       `json:"created_at"`
	DecisionAt      *time.Time      `json:"decision_at,omitempty"`
	Actor           string          `json:"actor,omitempty"`
	ConsumedAt      *time.Time      `json:"consumed_at,omitempty"`
}

type Event struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	Seq           int64           `json:"seq"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	CreatedAt     time.Time       `json:"created_at"`
	Stage         *Stage          `json:"stage,omitempty"`
	Visibility    string          `json:"visibility"`
	Payload       json.RawMessage `json:"payload"`
}

type Checkpoint struct {
	ParentID            string    `json:"parent_id,omitempty"`
	StateSchemaVersion  int       `json:"state_schema_version"`
	StateHash           string    `json:"state_hash,omitempty"`
	ContextSnapshotHash string    `json:"context_snapshot_hash,omitempty"`
	ID                  string    `json:"id"`
	TaskID              string    `json:"task_id"`
	PlanVersion         int       `json:"plan_version"`
	State               any       `json:"state"`
	LastEventSeq        int64     `json:"last_event_seq"`
	CreatedAt           time.Time `json:"created_at"`
}

type Plan struct {
	SchemaVersion    int        `json:"schema_version"`
	Goal             string     `json:"goal"`
	Subtasks         []PlanStep `json:"subtasks"`
	MaxSteps         int        `json:"max_steps"`
	FinalDeliverable string     `json:"final_deliverable"`
}

type PlanStep struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Description     string   `json:"description"`
	Dependencies    []string `json:"dependencies"`
	ExpectedOutput  string   `json:"expected_output"`
	SuccessCriteria []string `json:"success_criteria"`
}

func ValidatePlan(plan Plan, maxSteps int) error {
	if err := validatePlan(plan, maxSteps); err != nil {
		if errors.Is(err, ErrInvalidPlan) {
			return err
		}
		if errors.Is(err, ErrUnsupportedPlanKind) {
			return fmt.Errorf("%w: %w", ErrInvalidPlan, err)
		}
		return fmt.Errorf("%w: %v", ErrInvalidPlan, err)
	}
	return nil
}

func validatePlan(plan Plan, maxSteps int) error {
	if plan.SchemaVersion != 1 {
		return fmt.Errorf("unsupported plan schema_version %d", plan.SchemaVersion)
	}
	if strings.TrimSpace(plan.Goal) == "" || len(plan.Goal) > 1000 {
		return errors.New("plan goal must contain 1 to 1000 characters")
	}
	if len(plan.Subtasks) == 0 || len(plan.Subtasks) > maxSteps || len(plan.Subtasks) > 12 {
		return fmt.Errorf("plan must contain between 1 and %d subtasks", min(maxSteps, 12))
	}
	if plan.MaxSteps < len(plan.Subtasks) || plan.MaxSteps > maxSteps || plan.MaxSteps > 12 {
		return fmt.Errorf("plan max_steps must be between %d and %d", len(plan.Subtasks), min(maxSteps, 12))
	}
	if strings.TrimSpace(plan.FinalDeliverable) == "" || len(plan.FinalDeliverable) > 1000 {
		return errors.New("plan final_deliverable must contain 1 to 1000 characters")
	}
	steps := make(map[string]PlanStep, len(plan.Subtasks))
	for _, step := range plan.Subtasks {
		if !planStepIDPattern.MatchString(step.ID) {
			return fmt.Errorf("%w: plan step id %q must match ^[a-zA-Z0-9_-]{1,64}$", ErrInvalidPlan, step.ID)
		}
		if _, exists := steps[step.ID]; exists {
			return fmt.Errorf("duplicate plan step id %q", step.ID)
		}
		if step.Kind != "analysis" && step.Kind != "search" && step.Kind != "fetch" && step.Kind != "code_execution" && step.Kind != "external_action" {
			return fmt.Errorf("%w: %q", ErrUnsupportedPlanKind, step.Kind)
		}
		if strings.TrimSpace(step.Description) == "" || len(step.Description) > 1000 {
			return fmt.Errorf("step %q description must contain 1 to 1000 characters", step.ID)
		}
		if len(step.ExpectedOutput) > 500 {
			return fmt.Errorf("step %q expected_output exceeds 500 characters", step.ID)
		}
		if len(step.SuccessCriteria) == 0 || len(step.SuccessCriteria) > 10 {
			return fmt.Errorf("step %q must have between 1 and 10 success criteria", step.ID)
		}
		for _, criterion := range step.SuccessCriteria {
			if strings.TrimSpace(criterion) == "" || len(criterion) > 500 {
				return fmt.Errorf("step %q success criteria must contain 1 to 500 non-whitespace characters", step.ID)
			}
		}
		steps[step.ID] = step
	}
	for _, step := range plan.Subtasks {
		for _, dependency := range step.Dependencies {
			if _, exists := steps[dependency]; !exists {
				return fmt.Errorf("step %q references unknown dependency %q", step.ID, dependency)
			}
			if dependency == step.ID {
				return fmt.Errorf("step %q cannot depend on itself", step.ID)
			}
		}
	}
	if hasDependencyCycle(steps) {
		return errors.New("plan dependencies contain a cycle")
	}
	return nil
}

func hasDependencyCycle(steps map[string]PlanStep) bool {
	const (
		unvisited = iota
		visiting
		visited
	)
	marks := make(map[string]int, len(steps))
	var visit func(string) bool
	visit = func(id string) bool {
		if marks[id] == visiting {
			return true
		}
		if marks[id] == visited {
			return false
		}
		marks[id] = visiting
		for _, dependency := range steps[id].Dependencies {
			if visit(dependency) {
				return true
			}
		}
		marks[id] = visited
		return false
	}
	for id := range steps {
		if marks[id] == unvisited && visit(id) {
			return true
		}
	}
	return false
}
