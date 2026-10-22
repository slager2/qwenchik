package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type researchSessionConfig struct {
	profile       string
	policy        domain.ResearchPolicy
	budget        domain.ResearchBudget
	modelSnapshot domain.ModelProfileSnapshot
	goal          string
}

func standardResearchSessionDefaults() (domain.ResearchPolicy, domain.ResearchBudget, domain.ModelProfileSnapshot) {
	policy := domain.DefaultResearchPolicy()
	model := domain.ModelProfileSnapshot{
		Version:         1,
		Model:           "local-qwen",
		ContextLength:   131072,
		MaxOutputTokens: 4096,
		Temperature:     0.2,
		TopP:            0.8,
		StopSequences:   []string{},
		PromptVersion:   "local-research-v1",
		SchemaVersion:   1,
	}
	return policy, policy.Budget, model
}

func researchConfigFromTurn(in domain.SubmitTurn) researchSessionConfig {
	policy, budget, model := standardResearchSessionDefaults()
	if in.ResearchPolicy != nil {
		policy = *in.ResearchPolicy
	}
	if in.ResearchBudget != nil {
		budget = *in.ResearchBudget
		if in.ResearchPolicy == nil {
			policy.Budget = budget
		}
	} else if policy.Budget != (domain.ResearchBudget{}) {
		budget = policy.Budget
	}
	profile := strings.TrimSpace(in.ResearchProfile)
	if profile == "" {
		profile = strings.TrimSpace(policy.Profile)
	}
	if profile == "" {
		profile = domain.ResearchProfileStandard
	}
	if policy.Budget == (domain.ResearchBudget{}) {
		policy.Budget = budget
	}
	if in.ResearchPolicy == nil || strings.TrimSpace(policy.Profile) == "" {
		policy.Profile = profile
	}
	if in.ModelSnapshot != nil {
		model = *in.ModelSnapshot
	}
	if model.StopSequences != nil {
		model.StopSequences = append([]string{}, model.StopSequences...)
	}
	return researchSessionConfig{profile: profile, policy: policy, budget: budget, modelSnapshot: model, goal: in.Content}
}

func researchConfigFromSession(session domain.ResearchSession) researchSessionConfig {
	model := session.ModelSnapshot
	if model.StopSequences != nil {
		model.StopSequences = append([]string{}, model.StopSequences...)
	}
	return researchSessionConfig{
		profile:       session.Profile,
		policy:        session.Policy,
		budget:        session.Budget,
		modelSnapshot: model,
		goal:          session.Goal,
	}
}

func insertResearchSession(ctx context.Context, tx *sql.Tx, session domain.ResearchSession) error {
	if err := validateResearchSession(session); err != nil {
		return err
	}
	policy, err := json.Marshal(session.Policy)
	if err != nil {
		return err
	}
	budget, err := json.Marshal(session.Budget)
	if err != nil {
		return err
	}
	model, err := json.Marshal(session.ModelSnapshot)
	if err != nil {
		return err
	}
	counters, err := json.Marshal(session.Counters)
	if err != nil {
		return err
	}
	for _, snapshot := range [][]byte{policy, budget, model, counters} {
		if len(snapshot) > 64<<10 {
			return errors.New("research snapshot exceeds 65536 bytes")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_sessions(
		id,task_id,conversation_id,goal,status,profile,policy_json,budget_json,model_snapshot_json,counters_json,
		current_round,current_phase,plan_version,stop_reason,started_at,deadline_at,heartbeat_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		session.ID, session.TaskID, session.ConversationID, session.Goal, session.Status, session.Profile,
		string(policy), string(budget), string(model), string(counters), session.CurrentRound, session.CurrentPhase,
		session.PlanVersion, session.StopReason, nullableTime(session.StartedAt), stamp(session.DeadlineAt),
		nullableTime(session.HeartbeatAt), stamp(session.CreatedAt), stamp(session.UpdatedAt))
	return err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return stamp(*value)
}

func researchSession(ctx context.Context, q conversationQuery, taskID string) (domain.ResearchSession, error) {
	var session domain.ResearchSession
	var policy, budget, model, counters string
	var started, heartbeat sql.NullString
	var deadline, created, updated string
	err := q.QueryRowContext(ctx, `SELECT id,task_id,conversation_id,goal,status,profile,policy_json,budget_json,model_snapshot_json,counters_json,
		current_round,current_phase,plan_version,stop_reason,started_at,deadline_at,heartbeat_at,created_at,updated_at
		FROM research_sessions WHERE task_id=?`, taskID).Scan(
		&session.ID, &session.TaskID, &session.ConversationID, &session.Goal, &session.Status, &session.Profile,
		&policy, &budget, &model, &counters, &session.CurrentRound, &session.CurrentPhase, &session.PlanVersion,
		&session.StopReason, &started, &deadline, &heartbeat, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ResearchSession{}, ErrNotFound
	}
	if err != nil {
		return domain.ResearchSession{}, err
	}
	for _, item := range []struct {
		name   string
		value  string
		target any
	}{
		{"policy", policy, &session.Policy},
		{"budget", budget, &session.Budget},
		{"model snapshot", model, &session.ModelSnapshot},
		{"counters", counters, &session.Counters},
	} {
		if err := decodeResearchJSON(item.value, item.target); err != nil {
			return domain.ResearchSession{}, fmt.Errorf("decode research session %s: %w", item.name, err)
		}
	}
	for _, item := range []struct {
		value  string
		target *time.Time
	}{{deadline, &session.DeadlineAt}, {created, &session.CreatedAt}, {updated, &session.UpdatedAt}} {
		value, err := time.Parse(time.RFC3339Nano, item.value)
		if err != nil {
			return domain.ResearchSession{}, fmt.Errorf("decode research session timestamp: %w", err)
		}
		*item.target = value
	}
	for _, item := range []struct {
		value  sql.NullString
		target **time.Time
	}{{started, &session.StartedAt}, {heartbeat, &session.HeartbeatAt}} {
		if item.value.Valid {
			value, err := time.Parse(time.RFC3339Nano, item.value.String)
			if err != nil {
				return domain.ResearchSession{}, fmt.Errorf("decode research session timestamp: %w", err)
			}
			*item.target = &value
		}
	}
	if err := validateResearchSession(session); err != nil {
		return domain.ResearchSession{}, err
	}
	return session, nil
}

func decodeResearchJSON(value string, target any) error {
	trimmed := strings.TrimSpace(value)
	if len(value) > 64<<10 || len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("research snapshot must be a bounded JSON object")
	}
	if err := validateResearchJSONKeys(json.NewDecoder(strings.NewReader(value)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("research snapshot must contain one JSON object")
	}
	return nil
}

func validateResearchJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("research snapshot JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("research snapshot fields must not be null")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	keys := make(map[string]struct{})
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid research snapshot JSON key")
			}
			if _, duplicate := keys[name]; duplicate {
				return errors.New("duplicate research snapshot JSON key")
			}
			keys[name] = struct{}{}
			if name == "stop_sequences" {
				var sequences []string
				if err := decoder.Decode(&sequences); err != nil {
					return err
				}
				continue
			}
		}
		if err := validateResearchJSONKeys(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func validateResearchSession(session domain.ResearchSession) error {
	policy, budget := session.Policy, session.Budget
	invalid := func(field string) error { return fmt.Errorf("invalid research session %s", field) }
	if session.ID == "" || session.TaskID == "" || session.ConversationID == "" || len(session.Goal) > 512<<10 {
		return invalid("identity or goal")
	}
	switch session.Profile {
	case domain.ResearchProfileQuick, domain.ResearchProfileStandard, domain.ResearchProfileDeepLaptop, domain.ResearchProfileLongLaptop, domain.ResearchProfileOvernight:
	default:
		return invalid("profile")
	}
	if policy.Version != 1 || policy.Profile != session.Profile || policy.Budget != budget {
		return invalid("snapshot version or profile")
	}
	if budget.WallTimeSeconds <= 0 || budget.WallTimeSeconds > math.MaxInt64/int64(time.Second) || budget.MaxLLMCalls > int(^uint(0)>>1)/2 {
		return invalid("wall time or HTTP attempt budget")
	}
	for _, value := range []int{budget.MaxRounds, budget.MaxSteps, budget.MaxSearchQueries, budget.MaxFetches, budget.MaxLLMCalls, budget.MaxEvidence,
		budget.MaxQuestionsCreated, budget.MaxQuestionsAccepted, budget.MaxStagnantRounds, budget.MaxCitationSources, budget.MaxCitationClaims} {
		if value < 0 {
			return invalid("budget")
		}
	}
	if policy.TaskFetchBudgetBytes <= 0 || policy.SourceFetchBytes <= 0 || policy.PDFBytes <= 0 || policy.SourceFetchBytes > policy.TaskFetchBudgetBytes || policy.PDFBytes > policy.TaskFetchBudgetBytes ||
		policy.PDFPages <= 0 || policy.PDFTextRunes <= 0 || policy.PDFDownloadTimeoutSec <= 0 || policy.PDFExtractTimeoutSec <= 0 || policy.PDFMemoryBytes <= 0 || policy.PDFTempBytes <= 0 ||
		policy.PDFPIDLimit <= 0 || math.IsNaN(policy.PDFCPULimit) || math.IsInf(policy.PDFCPULimit, 0) || policy.PDFCPULimit <= 0 {
		return invalid("fetch policy")
	}
	if err := validateModelProfileSnapshot(session.ModelSnapshot); err != nil {
		return err
	}
	if !domain.TaskStatus(session.Status).Valid() || session.CurrentRound < 0 || session.PlanVersion < 0 || session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() || session.DeadlineAt.IsZero() {
		return invalid("state")
	}
	c := session.Counters
	for _, value := range []int{c.LLMCalls, c.HTTPAttempts, c.SearchQueries, c.Fetches, c.Evidence, c.QuestionsCreated, c.QuestionsAccepted, c.Rounds, c.StagnantRounds} {
		if value < 0 {
			return invalid("counters")
		}
	}
	if c.FetchedBytes < 0 || c.LLMCalls > budget.MaxLLMCalls || c.HTTPAttempts > budget.MaxLLMCalls*2 {
		return invalid("counters")
	}
	return nil
}

func researchDeadlineSeconds(requested int, wallTime int64) int64 {
	if requested > 0 {
		return min(int64(requested), wallTime)
	}
	return wallTime
}

func (s *Store) SubmitTurnWithResearchDefaults(ctx context.Context, in domain.SubmitTurn, defaults domain.ResearchSession, key string) (domain.SubmitResponse, error) {
	in.ResearchProfile = defaults.Profile
	in.ResearchPolicy = &defaults.Policy
	in.ResearchBudget = &defaults.Budget
	in.ModelSnapshot = &defaults.ModelSnapshot
	return s.SubmitTurn(ctx, in, key)
}

func (s *Store) GetResearchSession(ctx context.Context, taskID string) (domain.ResearchSession, error) {
	return researchSession(ctx, s.db, taskID)
}

func (s *Store) ReserveResearchLLMCall(ctx context.Context, taskID string) (domain.ResearchCounters, error) {
	return s.reserveResearchOperation(ctx, taskID, "llm", newID())
}

func (s *Store) ReserveResearchHTTPAttempt(ctx context.Context, taskID string) (domain.ResearchCounters, error) {
	return s.reserveResearchOperation(ctx, taskID, "http", newID())
}

func (s *Store) ReserveResearchOperation(ctx context.Context, taskID, kind, operationID string) (domain.ResearchCounters, error) {
	if kind == "fetch_url" {
		kind = "fetch"
	}
	if kind != "search" && kind != "fetch" {
		return domain.ResearchCounters{}, errors.New("research operation must be search or fetch")
	}
	if operationID == "" || len(operationID) > 512 || strings.TrimSpace(operationID) != operationID {
		return domain.ResearchCounters{}, errors.New("research operation ID must contain 1 to 512 bounded characters")
	}
	return s.reserveResearchOperation(ctx, taskID, kind, operationID)
}

func (s *Store) reserveResearchOperation(ctx context.Context, taskID, kind, operationID string) (domain.ResearchCounters, error) {
	if err := ctx.Err(); err != nil {
		return domain.ResearchCounters{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ResearchCounters{}, err
	}
	defer tx.Rollback()
	counters, err := reserveResearchOperationTx(ctx, tx, taskID, kind, operationID)
	if err != nil {
		return counters, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ResearchCounters{}, err
	}
	return counters, nil
}

func reserveResearchOperationTx(ctx context.Context, tx *sql.Tx, taskID, kind, operationID string) (domain.ResearchCounters, error) {
	session, err := researchSession(ctx, tx, taskID)
	if err != nil {
		return domain.ResearchCounters{}, err
	}
	if err := researchReservationAllowed(ctx, tx, session); err != nil {
		return session.Counters, err
	}
	identity, _ := json.Marshal([]string{"research.operation_reserved", taskID, operationID})
	digest := sha256.Sum256(identity)
	eventID := "research-reservation-" + hex.EncodeToString(digest[:])
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE id=? AND task_id=? AND type='research.operation_reserved'`, eventID, taskID).Scan(&prior)
	if err == nil {
		var reservation struct {
			Kind        string `json:"kind"`
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal([]byte(prior), &reservation); err != nil {
			return domain.ResearchCounters{}, err
		}
		if reservation.Kind != kind || reservation.OperationID != operationID {
			return session.Counters, ErrConflict
		}
		if err := ctx.Err(); err != nil {
			return session.Counters, err
		}
		if !time.Now().Before(session.DeadlineAt) {
			return session.Counters, context.DeadlineExceeded
		}
		return session.Counters, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ResearchCounters{}, err
	}
	counters := session.Counters
	switch kind {
	case "llm":
		if counters.LLMCalls >= session.Budget.MaxLLMCalls || counters.HTTPAttempts >= session.Budget.MaxLLMCalls*2 {
			return counters, ErrResearchBudgetExceeded
		}
		counters.LLMCalls++
		counters.HTTPAttempts++
	case "http":
		if counters.HTTPAttempts >= session.Budget.MaxLLMCalls*2 {
			return counters, ErrResearchBudgetExceeded
		}
		counters.HTTPAttempts++
	case "search":
		if counters.SearchQueries >= session.Budget.MaxSearchQueries {
			return counters, ErrResearchBudgetExceeded
		}
		counters.SearchQueries++
	case "fetch":
		if counters.Fetches >= session.Budget.MaxFetches {
			return counters, ErrResearchBudgetExceeded
		}
		if counters.FetchedBytes >= session.Policy.TaskFetchBudgetBytes {
			return counters, ErrResearchBudgetExceeded
		}
		if _, err := researchEvidenceCapacity(ctx, tx, session); err != nil {
			return counters, err
		}
		counters.Fetches++
	}
	if err := ctx.Err(); err != nil {
		return session.Counters, err
	}
	if !time.Now().Before(session.DeadlineAt) {
		return session.Counters, context.DeadlineExceeded
	}
	if err := writeResearchCounters(ctx, tx, taskID, counters); err != nil {
		return domain.ResearchCounters{}, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE task_id=?`, taskID).Scan(&seq); err != nil {
		return domain.ResearchCounters{}, err
	}
	payload, err := json.Marshal(map[string]any{"research_schema_version": 1, "session_id": session.ID, "kind": kind, "operation_id": operationID, "counters": counters})
	if err != nil {
		return domain.ResearchCounters{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id,task_id,seq,type,schema_version,created_at,visibility,payload_json) VALUES(?,?,?,'research.operation_reserved',1,?,'internal',?)`,
		eventID, taskID, seq, stamp(time.Now().UTC()), string(payload))
	if err != nil {
		return domain.ResearchCounters{}, err
	}
	return counters, nil
}

func researchReservationAllowed(ctx context.Context, tx *sql.Tx, session domain.ResearchSession) error {
	if err := fenceExecution(ctx, tx, session.TaskID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var status domain.TaskStatus
	var cancelled bool
	if err := tx.QueryRowContext(ctx, `SELECT status,cancel_requested FROM tasks WHERE id=?`, session.TaskID).Scan(&status, &cancelled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if cancelled || status.Terminal() || domain.TaskStatus(session.Status).Terminal() {
		return fmt.Errorf("%w: research run is cancelled or terminal", ErrConflict)
	}
	if status != domain.StatusPlanning && status != domain.StatusRunning && status != domain.StatusVerifying {
		return fmt.Errorf("%w: research run is not executing", ErrConflict)
	}
	if !time.Now().Before(session.DeadlineAt) {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func researchEvidenceCapacity(ctx context.Context, tx *sql.Tx, session domain.ResearchSession) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE task_id=?`, session.TaskID).Scan(&count); err != nil {
		return 0, err
	}
	count = max(count, session.Counters.Evidence)
	if count >= session.Budget.MaxEvidence {
		return count, ErrResearchBudgetExceeded
	}
	return count, nil
}

func writeResearchCounters(ctx context.Context, tx *sql.Tx, taskID string, counters domain.ResearchCounters) error {
	encoded, err := json.Marshal(counters)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE research_sessions SET counters_json=?,updated_at=? WHERE task_id=?`, string(encoded), stamp(time.Now().UTC()), taskID)
	return err
}

func (s *Store) SyncResearchCheckpoint(ctx context.Context, taskID string, state any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	session, err := researchSession(ctx, tx, taskID)
	if err != nil {
		return err
	}
	planVersion := max(1, session.PlanVersion)
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if value, ok := checkpointInteger(checkpointObject(encoded), "plan_version"); ok && value <= int64(int(^uint(0)>>1)) {
		planVersion = max(planVersion, int(value))
	}
	checkpointSeq, err := appendEvent(ctx, tx, taskID, "checkpoint.saved", nil, "internal", map[string]any{"plan_version": planVersion})
	if err != nil {
		return err
	}
	if err := insertCheckpoint(ctx, tx, taskID, planVersion, checkpointSeq, state); err != nil {
		return err
	}
	return tx.Commit()
}

func prepareResearchCheckpoint(ctx context.Context, tx *sql.Tx, taskID string, planVersion int, state any) ([]byte, int, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, 0, err
	}
	session, err := researchSession(ctx, tx, taskID)
	if errors.Is(err, ErrNotFound) {
		encoded, err := prepareChatCheckpoint(ctx, tx, taskID, encoded)
		return encoded, planVersion, err
	}
	if err != nil {
		return nil, 0, err
	}
	values := make(map[string]json.RawMessage)
	if !bytes.Equal(encoded, []byte("null")) {
		if err := json.Unmarshal(encoded, &values); err != nil || values == nil {
			return nil, 0, errors.New("research checkpoint state must be a JSON object")
		}
	}
	if err := validateResearchCheckpointEnvelope(values, session.ID); err != nil {
		return nil, 0, err
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM checkpoints WHERE task_id=? ORDER BY last_event_seq DESC,rowid DESC LIMIT 1`, taskID).Scan(&prior)
	if err == nil {
		var previous map[string]json.RawMessage
		if err := json.Unmarshal([]byte(prior), &previous); err != nil {
			return nil, 0, fmt.Errorf("decode prior research checkpoint: %w", err)
		}
		if err := validateResearchCheckpointEnvelope(previous, session.ID); err != nil {
			return nil, 0, err
		}
		for key, value := range previous {
			if researchWorkerCheckpointField(key) {
				continue
			}
			if current, exists := values[key]; exists {
				if key == "pending_invocation" {
					continue
				}
				values[key] = mergeResearchFutureField(value, current)
			} else {
				values[key] = value
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	if err := reconcileResearchCounters(ctx, tx, &session, values); err != nil {
		return nil, 0, err
	}
	session.PlanVersion = max(1, session.PlanVersion, planVersion)
	if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, taskID).Scan(&session.Status); err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_sessions SET current_round=?,plan_version=?,status=?,updated_at=? WHERE task_id=?`,
		session.CurrentRound, session.PlanVersion, session.Status, stamp(time.Now().UTC()), taskID); err != nil {
		return nil, 0, err
	}
	for key, value := range map[string]any{
		"research_schema_version": 1, "session_id": session.ID, "profile": session.Profile, "goal": session.Goal,
		"policy": session.Policy, "budget": session.Budget, "model_snapshot": session.ModelSnapshot,
		"counters": session.Counters, "current_round": session.CurrentRound, "current_phase": session.CurrentPhase,
		"plan_version": session.PlanVersion, "stop_reason": session.StopReason, "deadline_at": session.DeadlineAt,
		"search_queries": session.Counters.SearchQueries, "fetched_bytes": session.Counters.FetchedBytes,
		"llm_calls": session.Counters.LLMCalls, "http_attempts": session.Counters.HTTPAttempts,
		"fetches": session.Counters.Fetches, "evidence": session.Counters.Evidence,
		"questions_created": session.Counters.QuestionsCreated, "questions_accepted": session.Counters.QuestionsAccepted,
		"rounds": session.Counters.Rounds, "stagnant_rounds": session.Counters.StagnantRounds,
	} {
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return nil, 0, err
		}
		values[key] = encodedValue
	}
	encoded, err = json.Marshal(values)
	return encoded, session.PlanVersion, err
}

func validateResearchCheckpointEnvelope(values map[string]json.RawMessage, sessionID string) error {
	if _, present := values["research_schema_version"]; present {
		version, valid := checkpointInteger(values, "research_schema_version")
		if !valid || version != 1 {
			return errors.New("unsupported research checkpoint schema version")
		}
	}
	if raw, present := values["session_id"]; present {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil || id != sessionID {
			return fmt.Errorf("%w: research checkpoint session mismatch", ErrConflict)
		}
	}
	if raw, present := values["counters"]; present {
		var counters domain.ResearchCounters
		if err := decodeResearchJSON(string(raw), &counters); err != nil {
			return fmt.Errorf("invalid research checkpoint counters: %w", err)
		}
		for _, value := range []int{counters.LLMCalls, counters.HTTPAttempts, counters.SearchQueries, counters.Fetches, counters.Evidence,
			counters.QuestionsCreated, counters.QuestionsAccepted, counters.Rounds, counters.StagnantRounds} {
			if value < 0 {
				return errors.New("negative research checkpoint counter")
			}
		}
		if counters.FetchedBytes < 0 {
			return errors.New("negative research checkpoint byte count")
		}
	}
	return nil
}

func mergeResearchFutureField(prior, current json.RawMessage) json.RawMessage {
	previous, values := checkpointObject(prior), checkpointObject(current)
	if previous == nil || values == nil {
		return current
	}
	for key, value := range previous {
		if next, exists := values[key]; exists {
			values[key] = mergeResearchFutureField(value, next)
		} else {
			values[key] = value
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return current
	}
	return encoded
}

func researchWorkerCheckpointField(key string) bool {
	switch key {
	case "completion", "plan_attempt", "attempt_start_seq", "historical_observations", "lifetime_search_query_history", "plan_version", "search_queries", "search_query_history", "search_query_by_step",
		"fetched_bytes", "source_failures", "current_candidate", "partial", "pending_tool", "pending_tool_idempotent", "pending_approval", "pending_arguments", "pending_summary",
		"pending_target", "pending_risk", "failed_fetch_urls", "rejected_fetch_urls", "current_step", "completed_steps", "skipped_steps", "tool_results", "summary":
		return true
	default:
		return false
	}
}

func reconcileResearchCounters(ctx context.Context, tx *sql.Tx, session *domain.ResearchSession, values map[string]json.RawMessage) error {
	events, err := researchEventCounters(ctx, tx, session.TaskID)
	if err != nil {
		return err
	}
	c := &session.Counters
	c.LLMCalls = max(c.LLMCalls, events.LLMCalls)
	c.HTTPAttempts = max(c.HTTPAttempts, events.HTTPAttempts)
	c.SearchQueries = max(c.SearchQueries, events.SearchQueries)
	c.Fetches = max(c.Fetches, events.Fetches)
	c.FetchedBytes = max(c.FetchedBytes, events.FetchedBytes)
	if value, ok := checkpointInteger(values, "search_queries"); ok && value <= int64(int(^uint(0)>>1)) {
		c.SearchQueries = max(c.SearchQueries, int(value))
	}
	if value, ok := checkpointInteger(values, "fetched_bytes"); ok {
		c.FetchedBytes = max(c.FetchedBytes, value)
	}
	var evidenceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE task_id=?`, session.TaskID).Scan(&evidenceCount); err != nil {
		return err
	}
	c.Evidence = max(c.Evidence, evidenceCount)
	completed := checkpointStringSet(values["completed_steps"])
	toolResults := checkpointObject(values["tool_results"])
	completedFetches := make(map[string]struct{})
	for stepID := range completed {
		if isFetchToolResult(toolResults[stepID]) {
			completedFetches[stepID] = struct{}{}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT step_id,status FROM task_steps WHERE task_id=? AND kind='fetch'`, session.TaskID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var stepID, status string
		if err := rows.Scan(&stepID, &status); err != nil {
			_ = rows.Close()
			return err
		}
		if _, ok := completed[stepID]; ok || status == "completed" {
			completedFetches[stepID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	c.Fetches = max(c.Fetches, len(completedFetches))
	if created, accepted, ok := checkpointQuestionState(values["questions"]); ok {
		c.QuestionsCreated = max(c.QuestionsCreated, created)
		c.QuestionsAccepted = max(c.QuestionsAccepted, accepted)
	}
	if value, ok := checkpointInteger(values, "current_round"); ok && value <= int64(session.Budget.MaxRounds) {
		session.CurrentRound = max(session.CurrentRound, int(value))
	}
	c.Rounds = max(c.Rounds, session.CurrentRound)
	if err := validateResearchSession(*session); err != nil {
		return err
	}
	return writeResearchCounters(ctx, tx, session.TaskID, *c)
}

func researchEventCounters(ctx context.Context, tx *sql.Tx, taskID string) (domain.ResearchCounters, error) {
	var counters domain.ResearchCounters
	rows, err := tx.QueryContext(ctx, `SELECT seq,type,payload_json FROM events WHERE task_id=? AND type IN ('research.operation_reserved','tool.started','tool.completed','tool.failed') ORDER BY seq`, taskID)
	if err != nil {
		return counters, err
	}
	defer rows.Close()
	searches, fetches := map[string]struct{}{}, map[string]struct{}{}
	bytesByAttempt := make(map[string]int64)
	for rows.Next() {
		var seq int64
		var eventType, payload string
		if err := rows.Scan(&seq, &eventType, &payload); err != nil {
			return counters, err
		}
		values := checkpointObject(json.RawMessage(payload))
		if eventType == "research.operation_reserved" {
			switch checkpointString(values, "kind") {
			case "llm":
				counters.LLMCalls++
				counters.HTTPAttempts++
			case "http":
				counters.HTTPAttempts++
			case "search":
				counters.SearchQueries++
			case "fetch":
				counters.Fetches++
			}
			continue
		}
		tool := checkpointString(values, "tool")
		key := checkpointString(values, "operation_id")
		if key == "" {
			key = checkpointString(values, "attempt_id")
		}
		result := checkpointObject(values["result"])
		if key == "" && tool == "fetch_url" {
			key = checkpointString(result, "evidence_id")
		}
		if key == "" && checkpointString(values, "step_id") != "" {
			attempt, _ := checkpointInteger(values, "plan_attempt")
			key = fmt.Sprintf("%d:%s:%s", attempt, checkpointString(values, "step_id"), checkpointString(values, "target_url"))
		}
		if key == "" {
			key = fmt.Sprintf("event:%d", seq)
		}
		if tool == "search" && eventType == "tool.started" {
			searches[key] = struct{}{}
		}
		if tool != "fetch_url" {
			continue
		}
		if eventType == "tool.started" {
			fetches[key] = struct{}{}
		}
		if eventType == "tool.completed" || eventType == "tool.failed" {
			value, ok := checkpointInteger(values, "fetched_bytes")
			if eventType == "tool.completed" {
				value, ok = checkpointInteger(result, "fetched_bytes")
			}
			if ok {
				byteKey := checkpointString(values, "attempt_id")
				if byteKey == "" {
					byteKey = checkpointString(result, "evidence_id")
				}
				if byteKey == "" {
					byteKey = key
				}
				bytesByAttempt[byteKey] = max(bytesByAttempt[byteKey], value)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return counters, err
	}
	counters.SearchQueries = max(counters.SearchQueries, len(searches))
	counters.Fetches = max(counters.Fetches, len(fetches))
	for _, value := range bytesByAttempt {
		if value > math.MaxInt64-counters.FetchedBytes {
			return counters, errors.New("research fetched byte count overflow")
		}
		counters.FetchedBytes += value
	}
	return counters, nil
}

func checkpointString(values map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(values[key], &value)
	return value
}

func checkpointInteger(values map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := values[key]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] < '0' || bytes.TrimSpace(raw)[0] > '9' {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func checkpointStringSet(raw json.RawMessage) map[string]struct{} {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func checkpointObject(raw json.RawMessage) map[string]json.RawMessage {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func isFetchToolResult(raw json.RawMessage) bool {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return false
	}
	if evidenceID, ok := result["evidence_id"]; ok {
		var value string
		return json.Unmarshal(evidenceID, &value) == nil && value != ""
	}
	return false
}

func checkpointQuestionState(raw json.RawMessage) (int, int, bool) {
	var questions []json.RawMessage
	if json.Unmarshal(raw, &questions) != nil {
		return 0, 0, false
	}
	accepted := 0
	for _, rawQuestion := range questions {
		var question map[string]json.RawMessage
		if json.Unmarshal(rawQuestion, &question) != nil {
			continue
		}
		var status string
		if json.Unmarshal(question["status"], &status) == nil && status == "accepted" {
			accepted++
			continue
		}
		var isAccepted bool
		if json.Unmarshal(question["accepted"], &isAccepted) == nil && isAccepted {
			accepted++
		}
	}
	return len(questions), accepted, true
}

func updateResearchSessionStatus(ctx context.Context, tx *sql.Tx, taskID string, status domain.TaskStatus, phase string, terminal bool, stopReason string, now time.Time) error {
	if terminal {
		_, err := tx.ExecContext(ctx, `UPDATE research_sessions SET status=?,current_phase=?,stop_reason=CASE WHEN ?<>'' THEN ? WHEN stop_reason<>'' THEN stop_reason ELSE ? END,updated_at=? WHERE task_id=?`,
			status, phase, stopReason, stopReason, status, stamp(now), taskID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE research_sessions SET status=?,current_phase=CASE WHEN ?<>'' THEN ? ELSE current_phase END,updated_at=? WHERE task_id=?`, status, phase, phase, stamp(now), taskID)
	return err
}

func researchTerminalStopReason(status domain.TaskStatus, payload map[string]any) string {
	for _, key := range []string{"stop_reason", "reason", "error_code"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Store) SetResearchPhase(ctx context.Context, taskID, phase, stop string) error {
	if strings.TrimSpace(phase) == "" || len(phase) > 64 || len(stop) > 256 {
		return errors.New("invalid research phase or stop reason")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceExecution(ctx, tx, taskID); err != nil {
		return err
	}
	session, err := researchSession(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var status domain.TaskStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status.Terminal() {
		if session.CurrentPhase == phase && session.StopReason == stop {
			return nil
		}
		return ErrConflict
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE research_sessions SET status=?,current_phase=?,stop_reason=?,updated_at=? WHERE task_id=?`, status, phase, stop, stamp(now), taskID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
