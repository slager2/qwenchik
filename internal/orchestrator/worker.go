package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/policy"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/keepawake"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"github.com/slager2/qwenchik/internal/runtime/sandbox"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

var ErrInterrupted = errors.New("task execution interrupted by lifecycle command")
var ErrRecoveryRequired = errors.New("non-idempotent action outcome requires user review")
var ErrAwaitingApproval = errors.New("task is waiting for user approval")
var errVerificationIncomplete = errors.New("verifier found unmet requirements")
var errMissingEvidenceCitation = errors.New("research response lacks an evidence citation")

type verificationIncompleteError struct {
	Summary string
	Missing []string
}

func (e verificationIncompleteError) Error() string { return errVerificationIncomplete.Error() }
func (e verificationIncompleteError) Unwrap() error { return errVerificationIncomplete }

var citationPattern = regexp.MustCompile(`\[([^\]]+)\]`)
var userURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)
var fetchHTTPStatusPattern = regexp.MustCompile(`(?i)\bHTTP\s+(\d{3})\b`)

const maxToolCallArgumentsBytes = 64 << 10

type Repository interface {
	GetTask(context.Context, string) (domain.Task, error)
	Messages(context.Context, string) ([]domain.Message, error)
	Artifacts(context.Context, string) ([]domain.Artifact, error)
	Evidence(context.Context, string) ([]domain.Evidence, error)
	TransitionTask(context.Context, string, domain.TaskStatus, *domain.Stage, string, string, map[string]any) (domain.Task, error)
	EventsAfter(context.Context, string, int64, int) ([]domain.Event, error)
	RecordEvent(context.Context, string, string, *domain.Stage, string, map[string]any) (domain.Event, error)
	RecordMessageDelta(context.Context, string, string, string, int, string) (domain.Event, error)
	SavePlan(context.Context, string, domain.Plan, any) (int64, error)
	SaveCheckpoint(context.Context, string, int, any) (domain.Checkpoint, error)
	LatestCheckpoint(context.Context, string) (domain.Checkpoint, error)
	SetStepStatus(context.Context, string, string, string) error
	ClaimNext(context.Context) (domain.Task, bool, error)
	RecoverInterrupted(context.Context) (int64, error)
	ExpireApprovals(context.Context) (int, error)
	CompleteTask(context.Context, string, string, any) (domain.Task, error)
	CreateApproval(context.Context, domain.Approval, any) (domain.Approval, error)
	ConsumeApproval(context.Context, string, string, json.RawMessage) (string, bool, error)
	SearchMemories(context.Context, string, string, int) ([]domain.MemoryMatch, error)
}

type Options struct {
	HoldResourcePauses bool
	ContextProbe       func(context.Context, domain.ModelProfileSnapshot) (domain.ContextBudget, error)
	ReportArtifacts    interface {
		Put(context.Context, string, string, string, []byte) (domain.Artifact, string, error)
	}
	OnBundleChanged       func(domain.AgentBundle)
	RuntimeBundleHash     string
	MaxSteps              int
	MaxSearchQueries      int
	MaxFetchBytes         int64
	TaskTimeout           time.Duration
	MaxOutputTokens       int
	MaxInputBytes         int
	Temperature           float64
	TopP                  float64
	PollInterval          time.Duration
	Logger                *slog.Logger
	RuntimeResearchPolicy *domain.ResearchPolicy
	RuntimeModelContext   int
	ResourceMonitor       resources.Monitor
	ResourcePolicy        resources.Policy
	KeepAwake             bool
	SleepProvider         keepawake.Provider
	UseWorkerLeases       bool
}

type Worker struct {
	repo             Repository
	client           llm.Client
	tools            *tools.Registry
	options          Options
	wake             chan struct{}
	activeMu         sync.Mutex
	active           map[string]context.CancelFunc
	heartbeat        time.Time
	runtimeMu        sync.RWMutex
	sleepStatus      keepawake.Status
	activeRunID      string
	sleepWanted      bool
	runTelemetry     *runTelemetrySource
	telemetryMonitor resources.Monitor
}

type checkpointState struct {
	ResearchPlans              *domain.ResearchPlans         `json:"research_plans,omitempty"`
	Report                     *domain.ResearchReportState   `json:"research_report,omitempty"`
	SourceNotes                []domain.SourceNote           `json:"source_notes,omitempty"`
	Adaptive                   *domain.AdaptiveResearchState `json:"adaptive_research,omitempty"`
	Completion                 completionState               `json:"completion,omitempty"`
	PlanAttempt                int                           `json:"plan_attempt,omitempty"`
	AttemptStartSeq            int64                         `json:"attempt_start_seq,omitempty"`
	HistoricalObservations     []planObservations            `json:"historical_observations,omitempty"`
	LifetimeSearchQueryHistory []string                      `json:"lifetime_search_query_history,omitempty"`
	PlanVersion                int                           `json:"plan_version"`
	SearchQueries              int                           `json:"search_queries"`
	SearchQueryHistory         []string                      `json:"search_query_history,omitempty"`
	SearchQueryByStep          map[string]string             `json:"search_query_by_step,omitempty"`
	FetchedBytes               int64                         `json:"fetched_bytes"`
	SourceFailures             []sourceFailure               `json:"source_failures,omitempty"`
	CurrentCandidate           int                           `json:"current_candidate"`
	Partial                    bool                          `json:"partial,omitempty"`
	PartialOutcome             bool                          `json:"partial_outcome,omitempty"`
	PendingTool                string                        `json:"pending_tool,omitempty"`
	PendingToolIdempotent      bool                          `json:"pending_tool_idempotent,omitempty"`
	PendingApproval            bool                          `json:"pending_approval,omitempty"`
	PendingArguments           json.RawMessage               `json:"pending_arguments,omitempty"`
	PendingSummary             string                        `json:"pending_summary,omitempty"`
	PendingTarget              string                        `json:"pending_target,omitempty"`
	PendingRisk                string                        `json:"pending_risk,omitempty"`
	FailedFetchURLs            []string                      `json:"failed_fetch_urls,omitempty"`
	RejectedFetchURLs          []string                      `json:"rejected_fetch_urls,omitempty"`
	CurrentStep                string                        `json:"current_step,omitempty"`
	CompletedSteps             []string                      `json:"completed_steps"`
	SkippedSteps               []string                      `json:"skipped_steps,omitempty"`
	ToolResults                map[string]any                `json:"tool_results,omitempty"`
	Summary                    string                        `json:"summary,omitempty"`
}

type planObservations struct {
	PlanAttempt        int               `json:"plan_attempt"`
	ToolResults        map[string]any    `json:"tool_results"`
	SearchQueryHistory []string          `json:"search_query_history,omitempty"`
	SearchQueryByStep  map[string]string `json:"search_query_by_step,omitempty"`
}

type actionDecision struct {
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	ToolCall *struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_call"`
}

type verification struct {
	Complete             bool                  `json:"complete"`
	Status               verificationOutcome   `json:"status"`
	Summary              string                `json:"summary"`
	Missing              []string              `json:"missing"`
	SupplementaryMissing []string              `json:"supplementary_missing"`
	CriterionAssessments []criterionAssessment `json:"criterion_assessments"`
}

type criterionAssessment struct {
	StepID         string   `json:"step_id"`
	Criterion      int      `json:"criterion_index"`
	Classification string   `json:"classification"`
	Met            bool     `json:"met"`
	EvidenceIDs    []string `json:"evidence_ids"`
	Rationale      string   `json:"rationale"`
}

func New(repo Repository, client llm.Client, registry *tools.Registry, options Options) *Worker {
	if options.MaxSteps < 1 || options.MaxSteps > 12 {
		options.MaxSteps = 12
	}
	if options.TaskTimeout <= 0 {
		options.TaskTimeout = 2 * time.Hour
	}
	if options.MaxSearchQueries < 1 {
		options.MaxSearchQueries = 12
	}
	if options.MaxFetchBytes < 1 {
		options.MaxFetchBytes = domain.DefaultTaskFetchBudgetBytes
	}
	if options.MaxOutputTokens < 1 {
		options.MaxOutputTokens = 4096
	}
	if options.MaxInputBytes < 1 {
		options.MaxInputBytes = 128 << 10
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	telemetryMonitor := options.ResourceMonitor
	if telemetryMonitor != nil {
		options.ResourceMonitor = legacyResourceStatusMonitor{Monitor: telemetryMonitor}
	}
	return &Worker{
		repo: repo, client: client, tools: registry, options: options,
		wake: make(chan struct{}, 1), active: make(map[string]context.CancelFunc),
		runTelemetry: newRunTelemetrySource(), telemetryMonitor: telemetryMonitor,
	}
}

func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) Busy() bool {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	return len(w.active) > 0
}

func (w *Worker) Heartbeat(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	return w.heartbeat, nil
}

func (w *Worker) touchHeartbeat() {
	w.activeMu.Lock()
	w.heartbeat = time.Now().UTC()
	w.activeMu.Unlock()
}

func (w *Worker) Interrupt(taskID string) {
	w.activeMu.Lock()
	cancel := w.active[taskID]
	w.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.Wake()
}

func (w *Worker) Run(ctx context.Context) error {
	w.touchHeartbeat()
	recovered, err := w.repo.RecoverInterrupted(ctx)
	if err != nil {
		return fmt.Errorf("recover interrupted tasks: %w", err)
	}
	if recovered > 0 {
		w.options.Logger.Info("recovered interrupted tasks", "count", recovered)
	}
	if expired, err := w.repo.ExpireApprovals(ctx); err != nil {
		w.options.Logger.Warn("expire approvals failed", "error", err)
	} else if expired > 0 {
		w.options.Logger.Info("expired approval requests", "count", expired)
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.expireApprovalLoop(workerContext)
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		task, ok, err := w.repo.ClaimNext(workerContext)
		if err != nil {
			if workerContext.Err() != nil {
				return nil
			}
			w.options.Logger.Error("claim task failed", "error", err)
			select {
			case <-workerContext.Done():
				return nil
			case <-ticker.C:
			}
			continue
		}
		if ok {
			w.runTask(workerContext, task)
			continue
		}
		select {
		case <-workerContext.Done():
			return nil
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

func (w *Worker) expireApprovalLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.touchHeartbeat()
			workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if w.options.UseWorkerLeases {
				_, _ = w.repo.RecoverInterrupted(workCtx)
			}
			w.probeResourcePauses(workCtx)
			expired, err := w.repo.ExpireApprovals(workCtx)
			cancel()
			if err != nil {
				w.options.Logger.Warn("expire approvals failed", "error", err)
			} else if expired > 0 {
				w.options.Logger.Info("expired approval requests", "count", expired)
			}
		}
	}
}

func (w *Worker) runTask(parent context.Context, task domain.Task) {
	w.runtimeMu.Lock()
	w.activeRunID = task.ID
	w.runtimeMu.Unlock()
	defer func() { w.runtimeMu.Lock(); w.activeRunID = ""; w.runtimeMu.Unlock() }()
	deadline, researchDeadline := w.runDeadline(parent, task.ID)
	ctx, cancel := context.WithDeadline(parent, deadline)
	if task.Lease != nil {
		ctx = domain.WithExecutionLease(ctx, *task.Lease)
	}
	ctx = tools.WithTaskID(ctx, task.ID)
	ctx, guard, stopGuard := w.guardRun(ctx, task)
	defer stopGuard()
	w.activeMu.Lock()
	w.active[task.ID] = cancel
	w.activeMu.Unlock()
	defer func() {
		cancel()
		if current, err := w.repo.GetTask(context.Background(), task.ID); err == nil && (current.Status == domain.StatusCompleted || current.Status == domain.StatusCancelled) {
			if err := w.tools.CleanupTask(context.Background(), task.ID); err != nil {
				w.options.Logger.Warn("task workspace cleanup failed", "task_id", task.ID, "error", err)
			}
		}
		w.activeMu.Lock()
		delete(w.active, task.ID)
		w.activeMu.Unlock()
	}()

	w.options.Logger.Info("task execution started", "task_id", task.ID, "stage", domain.StagePlanning)
	var err error
	if err = checkRunResources(ctx); err == nil {
		releaseSleep := w.acquireSleep(ctx, task)
		err = w.executeRun(ctx, task)
		releaseSleep()
	}
	if err == nil || parent.Err() != nil {
		return
	}
	if errors.Is(context.Cause(ctx), sqlite.ErrLeaseLost) || errors.Is(err, sqlite.ErrLeaseLost) {
		return
	}
	reason := guard.pauseReason()
	if reason == "" {
		candidate := modelResourceReason(err)
		if w.options.ResourcePolicy.Enabled || strings.HasPrefix(candidate, "context_") {
			reason = candidate
		}
	}
	if reason != "" {
		if pauseErr := w.persistResourcePause(ctx, task, reason, err); pauseErr != nil && !errors.Is(pauseErr, sqlite.ErrLeaseLost) && !errors.Is(pauseErr, sqlite.ErrConflict) {
			w.options.Logger.Error("resource pause persistence failed", "task_id", task.ID, "error", pauseErr)
		}
		return
	}
	current, getErr := w.repo.GetTask(context.Background(), task.ID)
	if getErr != nil || current.Status == domain.StatusPaused || current.Status == domain.StatusWaitingApproval || current.Status == domain.StatusCancelled || current.Status.Terminal() {
		return
	}
	// Session-backed runs finalize inside executeRun's reserved model executor.
	sessionBacked := false
	if reader, ok := w.repo.(interface {
		GetResearchSession(context.Context, string) (domain.ResearchSession, error)
	}); ok {
		_, sessionErr := reader.GetResearchSession(ctx, task.ID)
		sessionBacked = sessionErr == nil
	}
	if errors.Is(err, errCompletionBlocked) && !sessionBacked {
		completed, partialErr := w.completeBlockedPartial(ctx, current, err)
		if completed && partialErr == nil {
			w.options.Logger.Info("completion blocked; committed evidence-bounded partial answer", "task_id", task.ID)
			return
		}
		if partialErr != nil {
			reason := guard.pauseReason()
			if reason == "" {
				candidate := modelResourceReason(partialErr)
				if w.options.ResourcePolicy.Enabled || strings.HasPrefix(candidate, "context_") {
					reason = candidate
				}
			}
			if reason != "" {
				if pauseErr := w.persistResourcePause(ctx, task, reason, partialErr); pauseErr != nil && !errors.Is(pauseErr, sqlite.ErrLeaseLost) && !errors.Is(pauseErr, sqlite.ErrConflict) {
					w.options.Logger.Error("resource pause persistence failed", "task_id", task.ID, "error", pauseErr)
				}
				return
			}
			w.options.Logger.Warn("evidence-bounded partial completion failed; preserving blocked outcome", "task_id", task.ID, "error", partialErr)
		}
	}
	code := "worker_error"
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut {
		code = "task_timeout"
		if researchDeadline {
			code = "run_deadline"
		}
	}
	if errors.Is(err, ErrInterrupted) || errors.Is(err, ErrAwaitingApproval) || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if !timedOut {
		if errors.Is(err, errCompletionBlocked) {
			code = "completion_blocked"
		} else if errors.Is(err, sqlite.ErrResearchBudgetExceeded) {
			code = "research_budget_exceeded"
		} else if errors.Is(err, ErrRecoveryRequired) {
			code = "failed_recovery_required"
		} else if errors.Is(err, errResearchRuntimeChanged) {
			code = "research_runtime_changed"
		} else if errors.Is(err, sqlite.ErrRefinementFrozen) {
			code = "refinement_frozen"
		} else if errors.Is(err, errRunDeadline) {
			code = "run_deadline"
		} else if errors.Is(err, errWorkerPanic) {
			code = "internal_error"
		} else if errors.Is(err, llm.ErrModelTimeout) {
			code = "model_timeout"
		} else if errors.Is(err, llm.ErrOutputTruncated) {
			code = "model_output_truncated"
		} else if errors.Is(err, llm.ErrStreamInterrupted) {
			code = "model_stream_interrupted"
		} else if errors.Is(err, llm.ErrInvalidModelOutput) {
			code = "invalid_model_output"
		} else if errors.Is(err, errLLMUnavailable) {
			code = "llm_unavailable"
		} else if errors.Is(err, research.ErrSearchFilteredByPolicy) {
			code = "search_filtered_by_policy"
		} else if errors.Is(err, research.ErrSearchResponseTooBig) {
			code = "search_response_too_big"
		} else if errors.Is(err, errSearchUnavailable) || errors.Is(err, research.ErrSearchUnavailable) {
			code = "search_unavailable"
		} else if errors.Is(err, errInvalidModelOutput) {
			code = "invalid_model_output"
		} else if errors.Is(err, errVerificationIncomplete) {
			code = "verification_incomplete"
		} else if errors.Is(err, errResearchPlanIncomplete) {
			code = "research_plan_incomplete"
		} else if errors.Is(err, errResearchPlanBudget) {
			code = "research_plan_budget"
		} else if errors.Is(err, errDuplicateSearchQuery) {
			code = "duplicate_search_query"
		} else if errors.Is(err, errUnsupportedStep) {
			code = "unsupported_step"
		} else if errors.Is(err, errInputBudgetExceeded) {
			code = "context_budget_exceeded"
		} else if errors.Is(err, errSearchBudgetExceeded) {
			code = "search_budget_exceeded"
		} else if errors.Is(err, errFetchBudgetExceeded) {
			code = "task_fetch_budget_exceeded"
		} else if errors.Is(err, research.ErrDNSLookupFailed) && !errors.Is(err, research.ErrUnsafeURL) {
			code = "fetch_dns_failed"
		} else if errors.Is(err, research.ErrFetchSourceUnavailable) || errors.Is(err, research.ErrFetchRequestFailed) && !errors.Is(err, research.ErrUnsafeURL) {
			code = "fetch_source_unavailable"
		} else if errors.Is(err, errFetchOutcomeUnknown) {
			code = "fetch_outcome_unknown"
		} else if errors.Is(err, tools.ErrApprovalRequired) {
			code = "approval_required"
		} else if errors.Is(err, tools.ErrUnknownTarget) {
			code = "unknown_research_target"
		} else if errors.Is(err, research.ErrPDFExtractorUnavailable) {
			code = "pdf_extractor_unavailable"
		} else if errors.Is(err, research.ErrPDFTooManyPages) {
			code = "pdf_page_limit_exceeded"
		} else if errors.Is(err, research.ErrPDFEncrypted) {
			code = "pdf_encrypted"
		} else if errors.Is(err, research.ErrPDFTooLarge) || errors.Is(err, research.ErrPDFNoText) || errors.Is(err, research.ErrPDFExtractionFailed) {
			code = "pdf_extraction_failed"
		} else if errors.Is(err, research.ErrResponseTooBig) {
			code = "source_response_too_large"
		} else if errors.Is(err, research.ErrExtractionChallenge) {
			code = "extraction_challenge"
		} else if errors.Is(err, research.ErrExtractionEmpty) {
			code = "extraction_empty"
		} else if errors.Is(err, sandbox.ErrSandboxUnavailable) {
			code = "sandbox_unavailable"
		} else if errors.Is(err, ErrRecoveryRequired) {
			code = "failed_recovery_required"
		} else if errors.Is(err, sqlite.ErrApprovalExpired) {
			code = "approval_expired"
		} else if errors.Is(err, tools.ErrInvalidArguments) {
			code = "invalid_tool_arguments"
		} else if errors.Is(err, tools.ErrUnknownTool) || errors.Is(err, tools.ErrToolDenied) {
			code = "tool_denied"
		} else if errors.Is(err, tools.ErrInvalidOutput) {
			code = "invalid_tool_output"
		}
	}
	searchCode, providerOutcomes := searchFailureMetadata(err)
	var sourceError *research.FetchFailure
	if !timedOut && !errors.Is(err, errRunDeadline) && (errors.As(err, &sourceError) || errors.Is(err, errFetchBudgetExceeded) || errors.Is(err, research.ErrResponseTooBig)) {
		if sourceCode := fetchFailureCode(err); sourceCode != "" {
			code = sourceCode
		}
	}
	if !timedOut && searchCode != "" {
		code = searchCode
	}
	if checkpoint, checkpointErr := w.currentCheckpoint(context.Background(), task.ID); checkpointErr == nil && checkpoint.CurrentStep != "" && !isConditionalSearchRetryStepID(checkpoint.CurrentStep) {
		_ = w.repo.SetStepStatus(context.WithoutCancel(ctx), task.ID, checkpoint.CurrentStep, "failed")
	}
	w.options.Logger.Error("task execution failed", "task_id", task.ID, "error_code", code, "error", err)
	failCtx, failCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer failCancel()
	failurePayload := map[string]any{"error_code": code, "message": publicError(code)}
	var plannerErr *plannerFailureError
	if errors.As(err, &plannerErr) {
		failurePayload["failure_stage"] = "planning"
		failurePayload["failure_role"] = plannerErr.Role
		failurePayload["validation_error"] = plannerErr.ValidationError
		failurePayload["planner_attempt"] = plannerErr.Attempt
		failurePayload["plan_summary"] = plannerErr.PlanSummary
		if code == "research_plan_incomplete" {
			failurePayload["message"] = publicPlannerError(plannerErr.ValidationError)
		}
	}
	if searchCode != "" {
		failurePayload["provider_outcomes"] = providerOutcomes
		failurePayload["outcomes"] = providerOutcomes
		retryable := false
		for _, outcome := range providerOutcomes {
			retryable = retryable || outcome["retryable"] == true
		}
		if code == "search_provider_timeout" || code == "search_validation_timeout" || code == "search_no_usable_hits" {
			retryable = true
		}
		if code == "search_no_results" || code == "search_filtered_by_policy" {
			retryable = false
		}
		diagnosticsID, idErr := newSynthesisID()
		if idErr != nil {
			diagnosticsID = task.ID
		}
		_, detail := safeSearchCode(code)
		failurePayload["retryable"] = retryable
		failurePayload["diagnostics_id"] = diagnosticsID
		failurePayload["details"] = map[string]any{"code": code, "message": detail, "provider_outcomes": providerOutcomes}
	}
	var verificationErr verificationIncompleteError
	if errors.As(err, &verificationErr) {
		missing := make([]string, 0, min(len(verificationErr.Missing), 20))
		for _, item := range verificationErr.Missing {
			if len(missing) == 20 {
				break
			}
			missing = append(missing, truncateRunes(item, 500))
		}
		failurePayload["verification_summary"] = truncateRunes(verificationErr.Summary, 1000)
		failurePayload["missing"] = missing
	}
	_, transitionErr := w.repo.TransitionTask(failCtx, task.ID, domain.StatusFailed, nil, "task.failed", "user", failurePayload)
	if transitionErr != nil && !errors.Is(transitionErr, domain.ErrInvalidTransition) {
		w.options.Logger.Error("persist task failure failed", "task_id", task.ID, "error", transitionErr)
	}
}

func (w *Worker) execute(ctx context.Context, claimed domain.Task) error {
	for {
		err := w.executeAttempt(ctx, claimed)
		if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok && session.Policy.AdaptiveEnabled && err != nil && ctx.Err() == nil {
			var incomplete verificationIncompleteError
			if errors.As(err, &incomplete) {
				if assessmentErr := w.assessDeterministicAdaptiveGap(ctx, claimed.ID, incomplete); assessmentErr != nil {
					return assessmentErr
				}
			} else if errors.Is(err, sqlite.ErrResearchBudgetExceeded) || errors.Is(err, errFetchBudgetExceeded) || errors.Is(err, errSearchBudgetExceeded) {
				if stopper, ok := w.repo.(interface {
					StopAdaptiveResearch(context.Context, string, string) error
				}); ok {
					if stopErr := stopper.StopAdaptiveResearch(ctx, claimed.ID, "research_budget"); stopErr != nil {
						return err
					}
					stage := domain.StageVerification
					if _, transitionErr := w.repo.TransitionTask(ctx, claimed.ID, domain.StatusVerifying, &stage, "task.status_changed", "user", nil); transitionErr != nil {
						return transitionErr
					}
					return errors.Join(errCompletionBlocked, verificationIncompleteError{Summary: "Adaptive research stopped at the persisted budget.", Missing: []string{"The bounded research budget ended before all critical requirements could be verified."}})
				}
			}
		}
		if !errors.Is(err, errVerificationIncomplete) {
			return err
		}
		continued, continuationErr := w.requestCompletionContinuation(ctx, claimed.ID, err)
		if continuationErr != nil {
			return continuationErr
		}
		if !continued {
			return err
		}
	}
}

func (w *Worker) executeAttempt(ctx context.Context, claimed domain.Task) error {
	task, err := w.repo.GetTask(ctx, claimed.ID)
	if err != nil {
		return err
	}
	messages, err := w.repo.Messages(ctx, task.ID)
	if err != nil {
		return err
	}
	goal := composeGoal(task.Description, messages)
	if len(goal) > 32<<10 {
		return errInputBudgetExceeded
	}
	w.tools.SeedTaskURLs(task.ID, goal)
	defer w.tools.ClearTaskURLs(task.ID)
	if minimumResearchSearches(task.Title+"\n"+goal) > 0 && !w.tools.Has("search") {
		return &plannerFailureError{Cause: errSearchUnavailable, Role: "planner_initial", Attempt: 1, ValidationError: safePlannerReason(errSearchUnavailable), PlanSummary: plannerPlanSummary(domain.Plan{})}
	}
	if required := requiredPlanSteps(task.Title, goal); required > w.options.MaxSteps {
		cause := fmt.Errorf("%w: required research steps exceed the configured step capacity", errResearchPlanIncomplete)
		return &plannerFailureError{Cause: cause, Role: "planner_initial", Attempt: 1, ValidationError: safePlannerReason(cause), PlanSummary: plannerPlanSummary(domain.Plan{})}
	}
	memoryMatches, err := w.repo.SearchMemories(ctx, truncateRunes(task.Title+"\n"+goal, 4000), task.ID, 5)
	if err != nil {
		return err
	}
	memoryContext := formatMemoryContext(memoryMatches)
	if len(memoryMatches) > 0 {
		memoryIDs := make([]string, 0, len(memoryMatches))
		for _, match := range memoryMatches {
			memoryIDs = append(memoryIDs, match.Memory.ID)
		}
		if _, err := w.repo.RecordEvent(ctx, task.ID, "memory.retrieved", nil, "audit", map[string]any{"memory_ids": memoryIDs}); err != nil {
			return err
		}
	}
	var plan domain.Plan
	newPlan := task.Plan == nil
	initialPlanState := checkpointState{PlanVersion: 1, CompletedSteps: []string{}, ToolResults: map[string]any{}}
	if newPlan {
		plan, err = w.createPlan(ctx, task, goal, memoryContext)
		if err != nil {
			return err
		}
	} else {
		plan = *task.Plan
		retried, retryErr := w.wasExplicitFailedRetry(ctx, task.ID)
		if retryErr != nil {
			return retryErr
		}
		failureCode := ""
		if retried {
			failureCode, err = w.latestFailureCode(ctx, task.ID)
			if err != nil {
				return err
			}
		}
		if retried && !roundPlanSession(ctx) && (failureCode == "verification_incomplete" || sourceFallbackCode(failureCode)) {
			previousState, canReplace, replaceErr := w.canReplacePlan(ctx, task.ID)
			if replaceErr != nil {
				return replaceErr
			}
			if !canReplace {
				return ErrRecoveryRequired
			}
			plan, err = w.createPlan(ctx, task, goal, memoryContext)
			if err != nil {
				return err
			}
			initialPlanState, err = w.resetPlanCheckpoint(ctx, task.ID, previousState)
			if err != nil {
				return err
			}
			newPlan = true
		}
	}
	if err := w.validateTaskPlan(baseCompletionPlan(plan), task.Title, goal); err != nil {
		if !errors.Is(err, errResearchPlanIncomplete) || newPlan {
			return err
		}
		previousState, canReplace, replaceErr := w.canReplacePlan(ctx, task.ID)
		if replaceErr != nil {
			return replaceErr
		}
		if !canReplace {
			return err
		}
		initialPlanState, err = w.resetPlanCheckpoint(ctx, task.ID, previousState)
		if err != nil {
			return err
		}
		plan, err = w.createPlan(ctx, task, goal, memoryContext)
		if err != nil {
			return err
		}
		newPlan = true
	}
	if newPlan {
		if _, err := w.repo.SavePlan(ctx, task.ID, plan, initialPlanState); err != nil {
			return err
		}
	}
	if err := w.ensureActive(ctx, task.ID); err != nil {
		return err
	}
	stage := executionStage(plan.Subtasks[0].Kind)
	if _, err := w.repo.TransitionTask(ctx, task.ID, domain.StatusRunning, &stage, "task.status_changed", "user", map[string]any{"plan_steps": len(plan.Subtasks)}); err != nil {
		return err
	}
	state, err := w.currentCheckpoint(ctx, task.ID)
	if err != nil {
		return err
	}
	state, checkpointChanged, err := w.reconcileResearchState(ctx, task.ID, state)
	if err != nil {
		return err
	}
	if state.PendingTool != "" && !state.PendingToolIdempotent && !state.PendingApproval {
		return ErrRecoveryRequired
	}
	if state.PendingTool != "" && !state.PendingApproval {
		state.PendingTool = ""
		state.PendingToolIdempotent = false
		checkpointChanged = true
	}
	if checkpointChanged {
		if _, err := w.repo.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
			return err
		}
	}
	if roundPlanSession(ctx) {
		if state.ResearchPlans == nil {
			return ErrRecoveryRequired
		}
		plan, err = state.ResearchPlans.Current()
		if err != nil {
			return ErrRecoveryRequired
		}
	}
	w.tools.ClearTaskURLs(task.ID)
	w.tools.SeedTaskURLs(task.ID, goal)
	w.tools.RememberURLs(task.ID, state.ToolResults)
	w.tools.RememberFetchedURLs(task.ID, state.ToolResults)
	requiredResearchSources := minimumResearchFetches(task.Title + "\n" + goal)
	requiredResearchFacets := minimumResearchSearches(task.Title + "\n" + goal)
	initialSearchStepID := firstSearchStepID(plan)
	attemptedFetchURLs := make(map[string]struct{})
	for _, step := range plan.Subtasks {
		if step.ID == initialSearchStepID && contains(state.CompletedSteps, step.ID) && !contains(state.SkippedSteps, step.ID) {
			if err := w.retryInitialSearchIfEmpty(ctx, task.ID, &stage, step, &state); err != nil {
				return err
			}
		}
		if contains(state.CompletedSteps, step.ID) || contains(state.SkippedSteps, step.ID) {
			continue
		}
		if !isCompletionStep(step.ID) && step.Kind == "search" && requiredResearchFacets > 0 && completedSearchFacetCount(plan, state) >= requiredResearchFacets && w.researchSearchResultsMeetSourceFloor(task.ID, state, requiredResearchSources) {
			if err := w.skipPlanStep(ctx, task.ID, &stage, step, "the completed search facets already produced enough distinct candidate sites", &state); err != nil {
				return err
			}
			continue
		}
		if !isCompletionStep(step.ID) && step.Kind == "fetch" && requiredResearchSources > 0 && distinctResearchSourceCount(state.ToolResults) >= requiredResearchSources && !hasPendingDirectDependents(plan, step.ID, state) {
			if err := w.skipPlanStep(ctx, task.ID, &stage, step, "the required distinct-source evidence threshold is already met", &state); err != nil {
				return err
			}
			continue
		}
		if step.Kind == "search" && state.SearchQueries >= w.options.MaxSearchQueries {
			return errSearchBudgetExceeded
		}
		if step.Kind == "fetch" && state.FetchedBytes >= w.options.MaxFetchBytes {
			return errFetchBudgetExceeded
		}
		for _, dependency := range step.Dependencies {
			if !contains(state.CompletedSteps, dependency) && !(contains(state.SkippedSteps, dependency) && isPlanSearchStep(plan, dependency)) {
				return fmt.Errorf("%w: step %s is not ready because %s is incomplete", errInvalidModelOutput, step.ID, dependency)
			}
		}
		if err := w.ensureActive(ctx, task.ID); err != nil {
			return err
		}
		state.CurrentStep = step.ID
		nextStage := executionStage(step.Kind)
		if stage != nextStage {
			stage = nextStage
			if _, err := w.repo.TransitionTask(ctx, task.ID, domain.StatusRunning, &stage, "task.status_changed", "user", nil); err != nil {
				return err
			}
		}
		if err := w.repo.SetStepStatus(ctx, task.ID, step.ID, "running"); err != nil {
			return err
		}
		if _, err := w.repo.RecordEvent(ctx, task.ID, "step.started", &stage, "user", map[string]any{"step_id": step.ID, "description": step.Description}); err != nil {
			return err
		}
		if _, err := w.repo.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
			return err
		}
		role, expectedTool, expectedRisk, ok := w.toolForStep(step.Kind)
		if !ok {
			return fmt.Errorf("%w: %s", errUnsupportedStep, step.Kind)
		}
		var decision actionDecision
		approvalGrant := ""
		var fetchURLs []string
		requiredURL := step.Kind == "fetch" && step.ID == plan.Subtasks[0].ID && hasUserURL(goal) && (noDiscoveryPattern.MatchString(goal) || !requiresWebResearch(goal))
		if step.Kind == "fetch" {
			fetchURLs = w.availableFetchURLs(task.ID, state.ToolResults, state.FailedFetchURLs, userProvidedURLs(goal), minimumResearchFetches(task.Title+"\n"+goal) > 1)
			if requiredURL {
				fetchURLs = userProvidedURLs(goal)
			}
			fetchURLs = filterFetchURLs(fetchURLs, attemptedFetchURLs, state.RejectedFetchURLs)
			fetchURLs = filterFailedSourceIdentities(fetchURLs, state.SourceFailures)
			if len(fetchURLs) == 0 {
				if requiredURL && len(state.SourceFailures) > 0 {
					return &research.FetchFailure{Err: research.ErrFetchSourceUnavailable, Code: state.SourceFailures[len(state.SourceFailures)-1].Code}
				}
				if deferred, err := w.deferFailedFetchToDiscovery(ctx, task, goal, plan, step, &stage, &state, requiredURL); err != nil {
					return err
				} else if deferred {
					continue
				}
				if searchesExhaustedWithoutResults(plan, state) {
					return w.searchNoResultsFailure(ctx, task.ID)
				}
				if incomplete := insufficientResearchEvidence(task.Title, goal, state); incomplete != nil {
					return incomplete
				}
				if distinctResearchSourceCount(state.ToolResults) == 0 {
					return insufficientFetchEvidenceError()
				}
				state.CurrentStep = ""
				if _, err := w.repo.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
					return err
				}
				if err := w.skipPlanStep(ctx, task.ID, &stage, step, "no unused approved source URL remained and the collected evidence already meets the source threshold", &state); err != nil {
					return err
				}
				continue
			}
		}
		if state.PendingApproval && state.CurrentStep == step.ID {
			approvalGrant, ok, err = w.repo.ConsumeApproval(ctx, task.ID, state.PendingTool, state.PendingArguments)
			if errors.Is(err, sqlite.ErrApprovalExpired) {
				err = nil
				ok = false
			}
			if err != nil {
				if errors.Is(err, sqlite.ErrApprovalAlreadyConsumed) {
					return ErrRecoveryRequired
				}
				return err
			}
			if !ok {
				_, err := w.repo.CreateApproval(ctx, domain.Approval{
					TaskID: task.ID, ToolName: state.PendingTool, Arguments: state.PendingArguments,
					Target: state.PendingTarget, RiskExplanation: state.PendingRisk,
					ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
				}, state)
				if err != nil {
					return err
				}
				return ErrAwaitingApproval
			}
			decision = actionDecision{Status: "continue", Summary: state.PendingSummary, ToolCall: &struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}{Name: state.PendingTool, Arguments: state.PendingArguments}}
		} else {
			var deterministic bool
			decision, deterministic, err = deterministicResearchAction(step, goal, expectedTool, state, fetchURLs)
			if err != nil {
				return err
			}
			if !deterministic {
				decision, err = w.chooseAction(ctx, task, goal, memoryContext, step, role, expectedTool, expectedRisk, state, fetchURLs)
				if err != nil {
					return err
				}
			}
		}
		decision.Summary = truthfulActionSummary(decision.ToolCall.Name, decision.Summary)
		if descriptor, exists := w.tools.Descriptor(decision.ToolCall.Name); exists {
			normalized, err := normalizeToolArguments(decision.ToolCall.Arguments, descriptor)
			if err != nil {
				return fmt.Errorf("%w: tool arguments failed local schema normalization: %v", errInvalidModelOutput, err)
			}
			decision.ToolCall.Arguments = normalized
		}
		if decision.ToolCall.Name == "search" {
			var arguments struct {
				Query      string `json:"query"`
				Language   string `json:"language"`
				Recency    string `json:"recency"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(decision.ToolCall.Arguments, &arguments); err != nil {
				return fmt.Errorf("%w: search query arguments could not be decoded", errInvalidModelOutput)
			}
			query := strings.TrimSpace(arguments.Query)
			if duplicateResearchQuery(query, state.SearchQueryHistory) && !pendingSearchQueryMatches(state, step.ID, query) {
				if diversified := diversifyResearchQuery(query, state.SearchQueryHistory); diversified != "" {
					arguments.Query = diversified
					encoded, encodeErr := json.Marshal(arguments)
					if encodeErr != nil {
						return fmt.Errorf("%w: diversified search query could not be encoded", errInvalidModelOutput)
					}
					decision.ToolCall.Arguments = encoded
					query = diversified
					decision.Summary = "Search query was automatically diversified after a repeated query."
				} else {
					return fmt.Errorf("%w: search query repeats an earlier research question", errDuplicateSearchQuery)
				}
			}
			if !duplicateResearchQuery(query, state.SearchQueryHistory) {
				if len(state.LifetimeSearchQueryHistory) == 0 {
					state.LifetimeSearchQueryHistory = append([]string(nil), state.SearchQueryHistory...)
				}
				state.SearchQueryHistory = append(state.SearchQueryHistory, query)
				state.LifetimeSearchQueryHistory = append(state.LifetimeSearchQueryHistory, query)
			}
			if state.SearchQueryByStep == nil {
				state.SearchQueryByStep = make(map[string]string)
			}
			state.SearchQueryByStep[step.ID] = query
		}
		var descriptor tools.Descriptor
		var result any
		if step.Kind == "fetch" {
			var exhausted bool
			result, descriptor, exhausted, err = w.executeFetchCandidates(
				ctx, task.ID, &stage, step, role, expectedRisk, fetchURLs, &decision, &state, attemptedFetchURLs, approvalGrant, requiredURL,
			)
			if err != nil {
				return err
			}
			if exhausted {
				if deferred, err := w.deferFailedFetchToDiscovery(ctx, task, goal, plan, step, &stage, &state, requiredURL); err != nil {
					return err
				} else if deferred {
					continue
				}
				if incomplete := insufficientResearchEvidence(task.Title, goal, state); incomplete != nil {
					return incomplete
				}
				if distinctResearchSourceCount(state.ToolResults) == 0 {
					return insufficientFetchEvidenceError()
				}
				state.CurrentStep = ""
				if _, err := w.repo.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
					return err
				}
				if err := w.skipPlanStep(ctx, task.ID, &stage, step, "all approved source URLs were unavailable and the collected evidence already meets the source threshold", &state); err != nil {
					return err
				}
				continue
			}
		} else {
			var policyDecision policy.Decision
			var authorizeErr error
			if expectedTool != "" && decision.ToolCall.Name != expectedTool {
				descriptor, policyDecision = w.tools.Decide(role, decision.ToolCall.Name)
				policyDecision = policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool does not match the active plan step"}
				if w.tools.Has(decision.ToolCall.Name) {
					authorizeErr = tools.ErrToolDenied
				} else {
					authorizeErr = tools.ErrUnknownTool
				}
			} else {
				descriptor, policyDecision, authorizeErr = w.tools.Authorize(ctx, role, decision.ToolCall.Name, decision.ToolCall.Arguments)
				if expectedRisk != "" && descriptor.RiskClass != expectedRisk {
					policyDecision = policy.Decision{Action: policy.Deny, Capability: descriptor.RequiredCapability, RiskClass: descriptor.RiskClass, Reason: "tool risk class does not match the active plan step"}
					authorizeErr = tools.ErrToolDenied
				}
				if authorizeErr == nil && policyDecision.Action == policy.ApprovalRequired && approvalGrant != "" {
					policyDecision.Action = policy.Allow
					policyDecision.Reason = "matching single-use approval was consumed"
				}
			}
			if _, err := w.repo.RecordEvent(ctx, task.ID, "policy.decision", &stage, "audit", map[string]any{
				"tool": decision.ToolCall.Name, "action": policyDecision.Action,
				"capability": descriptor.RequiredCapability, "risk_class": descriptor.RiskClass,
				"reason": policyDecision.Reason,
			}); err != nil {
				return err
			}
			if authorizeErr != nil {
				return authorizeErr
			}
			if policyDecision.Action == policy.ApprovalRequired {
				state.PendingTool = decision.ToolCall.Name
				state.PendingToolIdempotent = descriptor.Idempotent
				state.PendingApproval = true
				state.PendingArguments = append(json.RawMessage(nil), decision.ToolCall.Arguments...)
				state.PendingSummary = decision.Summary
				state.PendingTarget = approvalTarget(decision.ToolCall.Arguments)
				state.PendingRisk = policyDecision.Reason
				_, err := w.repo.CreateApproval(ctx, domain.Approval{
					TaskID: task.ID, ToolName: state.PendingTool, Arguments: state.PendingArguments,
					Target: state.PendingTarget, RiskExplanation: state.PendingRisk,
					ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
				}, state)
				if err != nil {
					return err
				}
				return ErrAwaitingApproval
			}
			if policyDecision.Action != policy.Allow {
				return tools.DecisionError(decision.ToolCall.Name, policyDecision)
			}
			state.PendingTool = decision.ToolCall.Name
			state.PendingToolIdempotent = descriptor.Idempotent
			state.PendingApproval = false
			state.PendingArguments = append(json.RawMessage(nil), decision.ToolCall.Arguments...)
			state.PendingSummary = decision.Summary
			state.PendingTarget = ""
			state.PendingRisk = ""
			if _, err := w.repo.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
				return err
			}
			if _, err := w.repo.RecordEvent(ctx, task.ID, "tool.started", &stage, "user", map[string]any{"step_id": step.ID, "tool": decision.ToolCall.Name, "summary": decision.Summary}); err != nil {
				return err
			}
			if err := w.ensureActive(ctx, task.ID); err != nil {
				return err
			}
			result, err = w.executeObservedTool(ctx, task.ID, &stage, role, decision.ToolCall.Name, decision.ToolCall.Arguments, approvalGrant, step.ID)
			if err != nil && decision.ToolCall.Name == "search" && recoverableSearchValidationMiss(err, state) {
				if eventErr := w.recordSearchValidationMiss(ctx, task.ID, &stage, step.ID, err); eventErr != nil {
					return eventErr
				}
				result, err = []research.SearchHit{}, nil
			}
			if err != nil {
				if errors.Is(err, tools.ErrInvalidArguments) {
					_, _ = w.repo.RecordEvent(ctx, task.ID, "policy.decision", &stage, "audit", map[string]any{
						"tool": decision.ToolCall.Name, "action": policy.Deny,
						"capability": descriptor.RequiredCapability, "risk_class": descriptor.RiskClass,
						"reason": "arguments failed execution-time revalidation",
					})
				}
				return err
			}
		}
		w.tools.RememberURLs(task.ID, result)
		if step.Kind == "fetch" {
			w.tools.RememberFetchedURLs(task.ID, result)
		}
		state.CompletedSteps = append(state.CompletedSteps, step.ID)
		state.ToolResults[step.ID] = result
		if step.Kind == "search" {
			state.SearchQueries++
		}
		if step.Kind == "fetch" {
			if resultMap, ok := result.(map[string]any); ok {
				if fetchedBytes, ok := resultMap["fetched_bytes"].(int64); ok {
					state.FetchedBytes += fetchedBytes
				}
			}
		}
		state.Summary = decision.Summary
		state.CurrentStep = ""
		state.PendingTool = ""
		state.PendingToolIdempotent = false
		state.PendingApproval = false
		state.PendingArguments = nil
		state.PendingSummary = ""
		state.PendingTarget = ""
		state.PendingRisk = ""
		if committer, ok := w.repo.(interface {
			CommitStepResult(context.Context, string, string, string, string, any, any) (domain.Checkpoint, error)
		}); ok {
			if _, err := committer.CommitStepResult(ctx, task.ID, step.ID, decision.ToolCall.Name, decision.Summary, result, state); err != nil {
				return err
			}
		} else {
			if _, err := w.repo.RecordEvent(ctx, task.ID, "tool.completed", &stage, "user", map[string]any{"step_id": step.ID, "tool": decision.ToolCall.Name, "result": result}); err != nil {
				return err
			}
			if err := w.repo.SetStepStatus(ctx, task.ID, step.ID, "completed"); err != nil {
				return err
			}
			if _, err := w.repo.SaveCheckpoint(ctx, task.ID, 1, state); err != nil {
				return err
			}
			if _, err := w.repo.RecordEvent(ctx, task.ID, "step.completed", &stage, "user", map[string]any{"step_id": step.ID, "summary": decision.Summary}); err != nil {
				return err
			}
		}
		if step.ID == initialSearchStepID {
			if err := w.retryInitialSearchIfEmpty(ctx, task.ID, &stage, step, &state); err != nil {
				return err
			}
		}
	}
	if err := w.ensureActive(ctx, task.ID); err != nil {
		return err
	}
	coverageMissing, err := w.validateExecutionCoverage(ctx, task.ID, plan, state)
	if err != nil {
		return err
	}
	if err := w.refreshSourceNotes(ctx, task, &state); err != nil {
		return err
	}
	if len(coverageMissing) > 0 {
		return verificationIncompleteError{Summary: "The backend could not verify that every planned step produced task-owned results.", Missing: coverageMissing}
	}
	requiredSources := minimumResearchFetches(task.Title + "\n" + goal)
	if evidenceCount := distinctResearchSourceCount(state.ToolResults); requiredSources > evidenceCount {
		return verificationIncompleteError{
			Summary: "Research evidence coverage across distinct source sites was insufficient.",
			Missing: []string{fmt.Sprintf("At least %d distinct fetched source site(s) are required; %d were collected.", requiredSources, evidenceCount)},
		}
	}
	verificationStage := domain.StageVerification
	if _, err := w.repo.TransitionTask(ctx, task.ID, domain.StatusVerifying, &verificationStage, "task.status_changed", "user", nil); err != nil {
		return err
	}
	verified, err := w.verify(ctx, task, goal, memoryContext, plan, state)
	if err != nil {
		return err
	}
	if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok && session.Policy.AdaptiveEnabled {
		state, err = w.currentCheckpoint(ctx, task.ID)
		if err != nil {
			return err
		}
		if err := w.refreshSourceNotes(ctx, task, &state); err != nil {
			return err
		}
	}
	if verified.Status == verificationOutcomeBlocked || !verified.Complete || len(verified.Missing) != 0 {
		return verificationIncompleteError{Summary: verified.Summary, Missing: verified.Missing}
	}
	if err := w.ensureActive(ctx, task.ID); err != nil {
		return err
	}
	evidenceIDs := collectEvidenceIDs(state.ToolResults)
	finalizationStage := domain.StageFinalization
	if _, err := w.repo.TransitionTask(ctx, task.ID, domain.StatusVerifying, &finalizationStage, "task.status_changed", "user", nil); err != nil {
		return err
	}
	messageID, err := newSynthesisID()
	if err != nil {
		return err
	}
	var answer string
	if session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession); ok && session.Policy.SectionedReportEnabled {
		answer, err = w.writeResearchReport(ctx, task, goal, &state, verified.Missing, verified.SupplementaryMissing)
	} else if verified.Status == verificationOutcomePartial {
		partial := newPartialSynthesisContext("Core request requirements are satisfied, but supplementary criteria remain unverified.", verified.SupplementaryMissing, state.SourceFailures)
		answer, err = w.synthesizePartialValidated(ctx, task, goal, memoryContext, state, verified.Summary, evidenceIDs, messageID, partial)
	} else {
		answer, err = w.synthesizeValidated(ctx, task, goal, memoryContext, state, verified.Summary, evidenceIDs, messageID)
	}
	if err != nil {
		return err
	}
	finalState := state
	finalState.Summary = verified.Summary
	if verified.Status == verificationOutcomePartial {
		finalState.Partial = true
		finalState.PartialOutcome = true
	}
	if len(state.SourceFailures) > 0 {
		finalState.Partial = true
		if _, err := w.repo.RecordEvent(ctx, task.ID, "research.warning", &finalizationStage, "user", map[string]any{
			"code": "sources_skipped", "partial": true, "source_failures": state.SourceFailures,
			"message": "Some sources could not be used. The answer is based only on the accepted evidence.",
		}); err != nil {
			return err
		}
	}
	_, err = w.repo.CompleteTask(ctx, task.ID, answer, finalState)
	return err
}

func formatMemoryContext(matches []domain.MemoryMatch) string {
	items := make([]map[string]string, 0, len(matches))
	for _, match := range matches {
		items = append(items, map[string]string{
			"id":   match.Memory.ID,
			"kind": match.Memory.Kind,
			"text": truncateRunes(match.Memory.Text, 150),
		})
	}
	return encodeJSON(items)
}

func approvalTarget(arguments json.RawMessage) string {
	var values map[string]json.RawMessage
	if json.Unmarshal(arguments, &values) != nil {
		return ""
	}
	for _, key := range []string{"url", "target", "destination", "path", "domain"} {
		var value string
		if raw, ok := values[key]; ok && json.Unmarshal(raw, &value) == nil && value != "" {
			return truncateRunes(value, 500)
		}
	}
	return ""
}

func (w *Worker) ensureActive(ctx context.Context, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	task, err := w.repo.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status == domain.StatusPaused || task.Status == domain.StatusCancelled {
		return ErrInterrupted
	}
	return nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func stagePointer(stage domain.Stage) *domain.Stage { return &stage }

var (
	errLLMUnavailable         = errors.New("llm unavailable")
	errInvalidModelOutput     = errors.New("invalid model output")
	errUnsupportedStep        = errors.New("unsupported step kind")
	errInputBudgetExceeded    = errors.New("context budget exceeded")
	errSearchBudgetExceeded   = errors.New("search query budget exhausted")
	errFetchBudgetExceeded    = errors.New("fetch byte budget exhausted")
	errFetchOutcomeUnknown    = errors.New("fetch outcome is unknown after interruption")
	errSearchUnavailable      = errors.New("web search is not configured")
	errResearchPlanIncomplete = errors.New("research plan is incomplete")
	errResearchPlanBudget     = errors.New("research plan exceeds configured step budget")
	errDuplicateSearchQuery   = errors.New("duplicate research query")
)

func publicError(code string) string {
	switch code {
	case "completion_blocked":
		return "Проверка не подтвердила обязательные критерии. Ограниченное продолжение исчерпано или не дало нового результата; источники и состояние сохранены."
	case "research_budget_exceeded":
		return "Исчерпан сохранённый бюджет исследования. Полученные сообщения и источники сохранены; новые вызовы модели или инструментов не выполняются."
	case "research_runtime_changed":
		return "Настройки runtime изменились и не позволяют безопасно применить сохранённый профиль этого исследования. Snapshot не изменён."
	case "run_deadline":
		return "Истёк сохранённый срок выполнения исследования. Частичный результат сохранён; повтор запускается только по вашей команде."
	case "internal_error":
		return "Обработка ответа прервана внутренней ошибкой. Сохранённые сообщения доступны; можно повторить запрос."
	case "model_timeout":
		return "Локальная модель не успела ответить в установленное время. Частичный ответ сохранён."
	case "model_output_truncated":
		return "Достигнут лимит длины ответа модели. Полученный текст сохранён; нажмите «Продолжить»."
	case "model_stream_interrupted":
		return "Соединение с локальной моделью прервалось до завершения ответа. Полученный текст сохранён."
	case "fetch_dns_failed":
		return "Не удалось разрешить DNS-имя источника. Адрес не загружен; при наличии разрешённых альтернатив исследование использует другой источник."
	case "search_provider_timeout":
		return "Истекло время сбора результатов поиска. Посмотрите результаты отдельных провайдеров и повторите запрос при необходимости."
	case "search_validation_timeout":
		return "Истекло время безопасной проверки найденных URL. Поиск выполнялся, но ни один источник не удалось разрешить для загрузки."
	case "search_no_usable_hits":
		return "Поиск вернул результаты, но доступных и разрешённых для загрузки URL не осталось. Попробуйте уточнить запрос или указать источник."
	case "extraction_empty":
		return "На странице не найдено содержательного текста; источник не принят как evidence."
	case "extraction_challenge":
		return "Страница требует captcha или проверки робота; она не принята как evidence."
	case "llm_unavailable":
		return "Локальная модель недоступна или вернула ошибку. Проверьте llama.cpp server и повторите задачу."
	case "invalid_model_output":
		return "Модель вернула структурированный ответ, который не прошёл проверку."
	case "verification_incomplete":
		return "Проверка нашла невыполненные критерии. Задача не помечена завершённой; см. список недостающего в событии ошибки."
	case "unsupported_step":
		return "План содержит действие, которое ещё не включено в эту версию приложения."
	case "context_budget_exceeded":
		return "Бюджета контекста недостаточно для задачи, схемы и результатов инструментов. Увеличьте LLM_CONTEXT_LENGTH и контекст llama.cpp server или сократите задачу и план. Проверка без результатов не выполняется."
	case "search_budget_exceeded":
		return "Исчерпан лимит поисковых запросов для этой задачи."
	case "search_unavailable":
		return "Поисковые провайдеры недоступны или не настроены. Проверьте диагностику провайдеров; наличие URL или API key ещё не означает, что поиск отвечает."
	case "search_no_results":
		return "Все поисковые направления и один расширенный запрос вернули пустые результаты. Уточните тему или добавьте публичные ссылки на источники."
	case "search_filtered_by_policy":
		return "Все результаты поиска исключены политикой разрешённых источников. Проверьте ограничения доменов для задачи."
	case "provider_blocked":
		return "Поисковый сервис заблокировал запрос или требует интерактивную проверку. Проверьте состояние сервиса поиска."
	case "provider_malformed":
		return "Поисковый сервис вернул некорректный ответ. Проверьте его состояние и повторите задачу."
	case "provider_misconfigured":
		return "Проверьте настройки и учётные данные поискового сервиса."
	case "provider_rate_limited":
		return "Поисковый сервис ограничил частоту запросов. Повторите задачу позже."
	case "search_response_too_big":
		return "Ответ поискового сервиса превысил установленный предел размера."
	case "research_plan_incomplete":
		return "План исследования не прошёл проверку: не удалось согласовать поиск, загрузку источников и зависимости шагов без потери исходных направлений исследования."
	case "research_plan_budget":
		return "Лимита MAX_STEPS недостаточно для требуемого числа источников. Увеличьте лимит или сузьте задачу."
	case "duplicate_search_query":
		return "Модель повторила уже использованный поисковый запрос. Исследование остановлено до нового отличающегося направления; сохранённое состояние можно повторить."
	case "fetch_budget_exceeded", "task_fetch_budget_exceeded":
		return "Исчерпан лимит загружаемых данных для этой задачи."
	case "source_response_too_large":
		return "Размер источника превышает допустимый лимит загрузки."
	case "pdf_source_too_large":
		return "Размер PDF превышает допустимый лимит загрузки PDF."
	case "pdf_extraction_input_too_large":
		return "Размер PDF превышает лимит входных данных обработчика."
	case "pdf_text_limit_exceeded":
		return "Извлечённый текст PDF превышает допустимый лимит."
	case "pdf_resource_exceeded":
		return "Обработка PDF превысила лимит ресурсов; частичный результат не принят."
	case "pdf_timeout":
		return "Истекло время обработки PDF; частичный результат не принят."
	case "pdf_malformed":
		return "PDF повреждён или имеет неподдерживаемую структуру."
	case "pdf_no_text":
		return "PDF не содержит доступного для извлечения текста."
	case "url_rejected_ssrf":
		return "Источник отклонён сетевой политикой безопасности."
	case "unsupported_content":
		return "Тип содержимого источника не поддерживается."
	case "fetch_timeout":
		return "Истекло время загрузки источника."
	case "source_unavailable", "fetch_request_failed":
		return "Источник недоступен; его содержимое не принято как evidence."
	case "fetch_source_unavailable":
		return "Выбранная страница недоступна. Повторите задачу: этот URL будет пропущен, а поиск выберет другой источник."
	case "fetch_outcome_unknown":
		return "Загрузка была прервана до сохранения результата; чтобы не нарушить лимит данных, начните новую задачу."
	case "task_timeout":
		return "Превышено время выполнения задачи."
	case "tool_denied":
		return "Запрошенный инструмент не зарегистрирован или запрещён для этой роли."
	case "approval_required":
		return "Инструмент требует подтверждения пользователя."
	case "approval_expired":
		return "Срок подтверждения истёк; действие не выполнено."
	case "unknown_research_target":
		return "URL не был указан в задаче или получен из разрешённого поиска, поэтому загрузка заблокирована."
	case "pdf_extractor_unavailable":
		return "Разрешённый политикой обработчик PDF недоступен; источник не был принят как evidence."
	case "pdf_page_limit_exceeded":
		return "PDF превышает настроенный лимит страниц и не был использован как evidence."
	case "pdf_encrypted":
		return "Зашифрованный PDF не поддерживается и не был использован как evidence."
	case "pdf_extraction_failed":
		return "Не удалось безопасно извлечь ограниченный текст из PDF; источник не был принят как evidence."
	case "invalid_tool_arguments":
		return "Аргументы инструмента не прошли backend-валидацию."
	case "invalid_tool_output":
		return "Результат инструмента не прошёл проверку выходной схемы."
	case "sandbox_unavailable":
		return "Rootless Docker sandbox недоступен; выполнение на хосте не выполнялось."
	case "failed_recovery_required":
		return "Результат изолированной команды неизвестен после остановки. Проверьте рабочую область и создайте новую задачу."
	default:
		return "Выполнение задачи завершилось ошибкой. Подробности доступны в локальном журнале."
	}
}
