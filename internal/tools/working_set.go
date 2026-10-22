package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

type WorkingSetRepository interface {
	Evidence(context.Context, string) ([]domain.Evidence, error)
	Artifacts(context.Context, string) ([]domain.Artifact, error)
	ContextSnapshot(context.Context, string) (domain.ContextSnapshot, error)
	PatchWorkingSet(context.Context, string, map[string]string) (domain.ContextSnapshot, error)
}

type WorkingSetArtifactReader interface {
	ReadRange(context.Context, string, string, int64, int) ([]byte, error)
}

type WorkingSetTools struct {
	Repository WorkingSetRepository
	Artifacts  WorkingSetArtifactReader
}

func NewWorkingSetTools(repository WorkingSetRepository, artifacts WorkingSetArtifactReader) WorkingSetTools {
	return WorkingSetTools{Repository: repository, Artifacts: artifacts}
}

type workingSetArgs struct {
	Query       string            `json:"query"`
	EvidenceIDs []string          `json:"evidence_ids"`
	ArtifactID  string            `json:"artifact_id"`
	Offset      int64             `json:"offset"`
	Limit       int               `json:"limit"`
	MaxResults  int               `json:"max_results"`
	MaxBytes    int               `json:"max_bytes"`
	Quote       string            `json:"quote"`
	Updates     map[string]string `json:"updates"`
}

type workingSetRecord struct {
	EvidenceID string            `json:"evidence_id"`
	Excerpt    string            `json:"excerpt"`
	Score      float64           `json:"score"`
	Kind       string            `json:"kind"`
	Status     string            `json:"status"`
	Locator    string            `json:"locator"`
	QuoteSpan  *domain.QuoteSpan `json:"quote_span,omitempty"`
}

func RegisterWorkingSetTools(registry *Registry, cfg *WorkingSetTools) error {
	if registry == nil || cfg == nil || cfg.Repository == nil || cfg.Artifacts == nil {
		return errors.New("working set tools require a repository and artifact reader")
	}
	runtime := *cfg
	ids := `{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":128}}`
	type registration struct {
		name, description, capability, args, output string
		execute                                     func(context.Context, workingSetArgs) (any, error)
	}
	registrations := []registration{
		{"evidence_query", "Search current task evidence using deterministic lexical word coverage.", "task.evidence.read", `{"query":{"type":"string","minLength":1,"maxLength":1000},"evidence_ids":` + ids + `,"max_results":{"type":"integer","minimum":1,"maximum":8}}`, workingSetRecordsSchema, runtime.query},
		{"artifact_read", "Read a bounded range of an artifact owned by the current task.", "task.artifact.read", `{"artifact_id":{"type":"string","minLength":1,"maxLength":128},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":2048}}`, `{"type":"object","properties":{"artifact_id":{"type":"string"},"offset":{"type":"integer"},"next_offset":{"type":"integer"},"content":{"type":"string"},"eof":{"type":"boolean"}},"required":["artifact_id","offset","next_offset","content","eof"],"additionalProperties":false}`, runtime.readArtifact},
		{"fact_list", "List extractive source observations, never verified truth.", "task.evidence.read", `{"evidence_ids":` + ids + `,"max_results":{"type":"integer","minimum":1,"maximum":8}}`, workingSetRecordsSchema, runtime.facts},
		{"citation_check", "Check task evidence membership and optional exact quote; semantic support remains unknown.", "task.evidence.read", `{"evidence_ids":` + ids + `,"quote":{"type":"string","minLength":1,"maxLength":2000}}`, `{"type":"array","maxItems":16,"items":{"type":"object","properties":{"evidence_id":{"type":"string"},"citation_support":{"type":"string","enum":["unknown"]},"quote_exact":{"type":"boolean"}},"required":["evidence_id","citation_support"],"additionalProperties":false}}`, runtime.citations},
		{"context_select", "Select a bounded durable context snapshot, preserving all protected fields.", "task.context.read", `{"max_bytes":{"type":"integer","minimum":1,"maximum":32768}}`, workingSetContextSchema, runtime.selectContext},
		{"working_set_update", "Set bounded task-local working notes; protected state cannot be edited.", "task.working_set.write", `{"updates":{"type":"object","minProperties":1,"maxProperties":16,"additionalProperties":{"type":"string","maxLength":2000}}}`, `{"type":"object","properties":{"operation_id":{"type":"string"},"updated_keys":{"type":"array","maxItems":16,"items":{"type":"string"}}},"required":["operation_id","updated_keys"],"additionalProperties":false}`, runtime.update},
	}
	for _, tool := range registrations {
		if registry.Has(tool.name) {
			return fmt.Errorf("tool %q already registered", tool.name)
		}
	}
	for _, tool := range registrations {
		required := map[string][]string{"evidence_query": {"query"}, "artifact_read": {"artifact_id"}, "citation_check": {"evidence_ids"}, "working_set_update": {"updates"}}[tool.name]
		if required == nil {
			required = []string{}
		}
		requiredJSON, _ := json.Marshal(required)
		schema := json.RawMessage(`{"type":"object","properties":` + tool.args + `,"required":` + string(requiredJSON) + `,"additionalProperties":false}`)
		name, execute := tool.name, tool.execute
		parse := func(ctx context.Context, raw json.RawMessage) (workingSetArgs, error) {
			var args workingSetArgs
			if err := ctx.Err(); err != nil {
				return args, err
			}
			if taskIDFromContext(ctx) == "" {
				return args, ErrUnknownTarget
			}
			if len(raw) > 16<<10 {
				return args, ErrInvalidArguments
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return args, err
			}
			if name == "evidence_query" && len(workingSetWords(args.Query)) == 0 {
				return args, ErrInvalidArguments
			}
			if name == "working_set_update" {
				var object struct {
					Updates map[string]json.RawMessage `json:"updates"`
				}
				if err := json.Unmarshal(raw, &object); err != nil {
					return args, err
				}
				for _, value := range object.Updates {
					if len(value) == 0 || value[0] != '"' {
						return args, ErrInvalidArguments
					}
				}
				return args, validateWorkingSetUpdates(args.Updates)
			}
			return args, nil
		}
		limit := 32 << 10
		if name == "artifact_read" || name == "working_set_update" || name == "citation_check" {
			limit = 16 << 10
		}
		if err := registry.RegisterValidated(Descriptor{
			Name: name, Description: tool.description, ArgumentsSchema: schema, OutputSchema: json.RawMessage(tool.output),
			RequiredCapability: tool.capability, RiskClass: "read", TimeoutSeconds: 5, Idempotent: true,
			MaxOutputBytes: limit, AllowedRoles: []string{"researcher", "analyst"},
		}, func(ctx context.Context, raw json.RawMessage) error { _, err := parse(ctx, raw); return err }, func(ctx context.Context, raw json.RawMessage) (any, error) {
			args, err := parse(ctx, raw)
			if err != nil {
				return nil, err
			}
			return execute(ctx, args)
		}); err != nil {
			return err
		}
	}
	return nil
}

const workingSetRecordsSchema = `{"type":"array","maxItems":8,"items":{"type":"object","properties":{"evidence_id":{"type":"string"},"excerpt":{"type":"string","maxLength":2000},"score":{"type":"number"},"kind":{"type":"string","enum":["extractive_observation"]},"status":{"type":"string","enum":["source_claim"]},"locator":{"type":"string","enum":["unknown","exact"]},"quote_span":{"type":"object","properties":{"coordinate_space":{"type":"string"},"start":{"type":"integer"},"end":{"type":"integer"},"page":{"type":"integer"},"text":{"type":"string","maxLength":2000}},"required":["coordinate_space","start","end","text"],"additionalProperties":false}},"required":["evidence_id","excerpt","score","kind","status","locator"],"additionalProperties":false}}`

const workingSetContextSchema = `{"type":"object","properties":{"version":{"type":"integer"},"id":{"type":"string"},"number":{"type":"integer"},"hash":{"type":"string"},"goal":{"type":"string"},"constraints":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"done_criteria":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"completed_steps":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"accepted_evidence_ids":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"open_questions":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"contradictions":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"failed_attempts":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"budget_remaining":{"anyOf":[{"type":"object","additionalProperties":{"type":"integer"}},{"type":"null"}]},"next_action":{"type":"string"},"summary":{"type":"string"},"file_changes":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]},"working_set":{"anyOf":[{"type":"object","additionalProperties":{"type":"string"}},{"type":"null"}]}},"required":["version","id","number","hash","goal","constraints","done_criteria","completed_steps","accepted_evidence_ids","open_questions","contradictions","failed_attempts","budget_remaining","next_action","summary","file_changes","working_set"],"additionalProperties":false}`

func (w WorkingSetTools) evidence(ctx context.Context, ids []string) ([]domain.Evidence, error) {
	taskID := taskIDFromContext(ctx)
	if taskID == "" {
		return nil, ErrUnknownTarget
	}
	items, err := w.Repository.Evidence(ctx, taskID)
	if err != nil {
		return nil, err
	}
	owned := make(map[string]domain.Evidence)
	for _, item := range items {
		if item.TaskID == taskID && item.ID != "" && item.HTTPStatus >= 200 && item.HTTPStatus < 300 && item.TextArtifactID != "" && item.ExtractionMethod != "" && len(item.QualityFlags) == 0 {
			owned[item.ID] = item
		}
	}
	result := make([]domain.Evidence, 0, len(owned))
	if len(ids) != 0 {
		seen := map[string]bool{}
		for _, id := range ids {
			item, ok := owned[id]
			if !ok {
				return nil, ErrUnknownTarget
			}
			if !seen[id] {
				result = append(result, item)
				seen[id] = true
			}
		}
	} else {
		for _, item := range owned {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > 500 {
		return nil, errors.New("task evidence exceeds bounded query corpus")
	}
	return result, nil
}

func workingSetWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		words[word] = true
	}
	return words
}

func workingSetExcerpt(text string) string {
	runes := []rune(text)
	if len(runes) > 2000 {
		runes = runes[:2000]
	}
	return string(runes)
}

func boundedWorkingSetRecords(records []workingSetRecord) ([]workingSetRecord, error) {
	for {
		encoded, err := json.Marshal(records)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= 32<<10 {
			return records, nil
		}
		records = records[:len(records)-1]
	}
}

func (w WorkingSetTools) query(ctx context.Context, args workingSetArgs) (any, error) {
	items, err := w.evidence(ctx, args.EvidenceIDs)
	if err != nil {
		return nil, err
	}
	query := workingSetWords(args.Query)
	result := []workingSetRecord{}
	for _, item := range items {
		if len(item.Excerpt) > 64<<10 || len(item.Title) > 4096 {
			return nil, errors.New("evidence exceeds lexical input limit")
		}
		words := workingSetWords(item.Title + " " + item.Excerpt)
		matches := 0
		for word := range query {
			if words[word] {
				matches++
			}
		}
		if matches == 0 {
			continue
		}
		result = append(result, workingSetRecord{EvidenceID: item.ID, Excerpt: workingSetExcerpt(item.Excerpt), Score: float64(matches) / float64(len(query)), Kind: "extractive_observation", Status: "source_claim", Locator: "unknown"})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return result[i].EvidenceID < result[j].EvidenceID
	})
	limit := args.MaxResults
	if limit == 0 {
		limit = 8
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return boundedWorkingSetRecords(result)
}

func (w WorkingSetTools) artifact(ctx context.Context, artifactID string) (domain.Artifact, error) {
	taskID := taskIDFromContext(ctx)
	if taskID == "" {
		return domain.Artifact{}, ErrUnknownTarget
	}
	items, err := w.Repository.Artifacts(ctx, taskID)
	if err != nil {
		return domain.Artifact{}, err
	}
	for _, item := range items {
		if item.TaskID == taskID && item.ID == artifactID {
			return item, nil
		}
	}
	return domain.Artifact{}, ErrUnknownTarget
}

func (w WorkingSetTools) readArtifact(ctx context.Context, args workingSetArgs) (any, error) {
	metadata, err := w.artifact(ctx, args.ArtifactID)
	if err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit == 0 {
		limit = 2048
	}
	if args.Offset > metadata.SizeBytes {
		return nil, ErrInvalidArguments
	}
	content, err := w.Artifacts.ReadRange(ctx, taskIDFromContext(ctx), metadata.ID, args.Offset, limit)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(content) {
		return nil, errors.New("artifact range is not UTF-8 text; choose a character-aligned range")
	}
	next := args.Offset + int64(len(content))
	return map[string]any{"artifact_id": metadata.ID, "offset": args.Offset, "next_offset": next, "content": string(content), "eof": next >= metadata.SizeBytes}, nil
}

func (w WorkingSetTools) text(ctx context.Context, item domain.Evidence) (string, error) {
	metadata, err := w.artifact(ctx, item.TextArtifactID)
	if err != nil {
		return "", err
	}
	if metadata.SizeBytes < 0 || metadata.SizeBytes > 64<<10 {
		return "", errors.New("text artifact exceeds verification limit")
	}
	content, err := w.Artifacts.ReadRange(ctx, taskIDFromContext(ctx), item.TextArtifactID, 0, 64<<10)
	if err != nil {
		return "", err
	}
	if int64(len(content)) != metadata.SizeBytes || !utf8.Valid(content) {
		return "", errors.New("text artifact is incomplete or invalid UTF-8")
	}
	return string(content), nil
}

func (w WorkingSetTools) facts(ctx context.Context, args workingSetArgs) (any, error) {
	items, err := w.evidence(ctx, args.EvidenceIDs)
	if err != nil {
		return nil, err
	}
	result := []workingSetRecord{}
	limit := args.MaxResults
	if limit == 0 {
		limit = 8
	}
	remaining := 64 << 10
	for _, item := range items {
		if len(result) == limit {
			break
		}
		metadata, err := w.artifact(ctx, item.TextArtifactID)
		if err != nil {
			return nil, err
		}
		if metadata.SizeBytes < 0 || metadata.SizeBytes > int64(remaining) {
			continue
		}
		remaining -= int(metadata.SizeBytes)
		text, err := w.text(ctx, item)
		if err != nil {
			return nil, err
		}
		runes := []rune(text)
		for _, span := range item.QuoteSpans {
			if len(result) == limit {
				break
			}
			if span.Text == "" || utf8.RuneCountInString(span.Text) > 2000 || !strings.Contains(text, span.Text) {
				continue
			}
			record := workingSetRecord{EvidenceID: item.ID, Excerpt: span.Text, Kind: "extractive_observation", Status: "source_claim", Locator: "unknown"}
			if span.CoordinateSpace == "extracted_text_runes" && span.Start >= 0 && span.End >= span.Start && span.End <= len(runes) && string(runes[span.Start:span.End]) == span.Text {
				copy := span
				record.Locator, record.QuoteSpan = "exact", &copy
			}
			result = append(result, record)
		}
		if len(item.QuoteSpans) == 0 && item.Excerpt != "" && strings.Contains(text, item.Excerpt) && len(result) < limit {
			result = append(result, workingSetRecord{EvidenceID: item.ID, Excerpt: workingSetExcerpt(item.Excerpt), Kind: "extractive_observation", Status: "source_claim", Locator: "unknown"})
		}
	}
	return boundedWorkingSetRecords(result)
}

func (w WorkingSetTools) citations(ctx context.Context, args workingSetArgs) (any, error) {
	if len(args.EvidenceIDs) == 0 {
		return []map[string]any{}, nil
	}
	items, err := w.evidence(ctx, args.EvidenceIDs)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	remaining := int64(64 << 10)
	for _, item := range items {
		record := map[string]any{"evidence_id": item.ID, "citation_support": "unknown"}
		if args.Quote != "" {
			metadata, err := w.artifact(ctx, item.TextArtifactID)
			if err != nil {
				return nil, err
			}
			if metadata.SizeBytes < 0 || metadata.SizeBytes > remaining {
				return nil, errors.New("quote verification exceeds artifact read limit")
			}
			remaining -= metadata.SizeBytes
			text, err := w.text(ctx, item)
			if err != nil {
				return nil, err
			}
			if !strings.Contains(text, args.Quote) {
				return nil, errors.New("quote is not an exact substring of cited evidence")
			}
			record["quote_exact"] = true
		}
		result = append(result, record)
	}
	return result, nil
}

func (w WorkingSetTools) selectContext(ctx context.Context, args workingSetArgs) (any, error) {
	snapshot, err := w.Repository.ContextSnapshot(ctx, taskIDFromContext(ctx))
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	limit := args.MaxBytes
	if limit == 0 {
		limit = 12000
	}
	if len(encoded) > limit {
		return nil, errors.New("durable context cannot fit without truncating protected state")
	}
	return snapshot, nil
}

func validateWorkingSetUpdates(updates map[string]string) error {
	if len(updates) == 0 || len(updates) > 16 {
		return ErrInvalidArguments
	}
	for key, value := range updates {
		if len(key) < 1 || len(key) > 64 || key[0] < 'a' || key[0] > 'z' || utf8.RuneCountInString(value) > 1024 || len(value) > 2000 || !utf8.ValidString(value) {
			return ErrInvalidArguments
		}
		for _, r := range key {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return ErrInvalidArguments
			}
		}
		normalized := strings.ReplaceAll(key, "_", "")
		for _, reserved := range []string{"goal", "constraint", "donecriteria", "budget", "evidence", "pending", "task", "conversation", "artifact", "policy", "permission", "capability", "status", "checkpoint", "snapshot", "hash", "version", "counter", "deadline", "plan", "approval", "completion", "source", "protected", "completedsteps", "openquestions", "contradictions", "failedattempts", "nextaction", "summary", "filechanges", "workingset", "session", "number"} {
			if strings.Contains(normalized, reserved) {
				return ErrInvalidArguments
			}
		}
		if key == "id" {
			return ErrInvalidArguments
		}
	}
	encoded, _ := json.Marshal(updates)
	if len(encoded) > 12000 {
		return ErrInvalidArguments
	}
	return nil
}

func (w WorkingSetTools) update(ctx context.Context, args workingSetArgs) (any, error) {
	if err := validateWorkingSetUpdates(args.Updates); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(args.Updates)
	hash := sha256.Sum256(append([]byte(taskIDFromContext(ctx)+"\x00"), encoded...))
	if _, err := w.Repository.PatchWorkingSet(ctx, taskIDFromContext(ctx), args.Updates); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(args.Updates))
	for key := range args.Updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return map[string]any{"operation_id": hex.EncodeToString(hash[:]), "updated_keys": keys}, nil
}
