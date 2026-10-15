package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/slager2/qwenchik/internal/domain"
)

func (s *Store) CreateMemory(ctx context.Context, memory domain.Memory) (domain.Memory, error) {
	memory.ID = newID()
	memory.Status = domain.MemoryCandidate
	memory.Text = strings.TrimSpace(memory.Text)
	if memory.Scope == "" {
		memory.Scope = "global"
	}
	if memory.RetentionPolicy == "" {
		memory.RetentionPolicy = "until_deleted"
	}
	if memory.Metadata == nil {
		memory.Metadata = map[string]any{}
	}
	memory.UserEditable = true
	if err := memory.Validate(); err != nil {
		return domain.Memory{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, err
	}
	defer tx.Rollback()
	if memory.SourceTaskID != "" {
		if _, err := getTask(ctx, tx, memory.SourceTaskID); err != nil {
			return domain.Memory{}, err
		}
	}
	if memory.SourceEvidenceID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM evidence WHERE id=? AND task_id=?`, memory.SourceEvidenceID, memory.SourceTaskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return domain.Memory{}, ErrNotFound
		} else if err != nil {
			return domain.Memory{}, err
		}
	}
	now := time.Now().UTC()
	memory.CreatedAt = now
	memory.UpdatedAt = now
	metadata, err := json.Marshal(memory.Metadata)
	if err != nil {
		return domain.Memory{}, fmt.Errorf("encode memory metadata: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memories(id,kind,status,text,metadata_json,scope,confidence,source_evidence_id,source_task_id,user_editable,retention_policy,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		memory.ID, memory.Kind, memory.Status, memory.Text, string(metadata), memory.Scope, nullableFloat(memory.Confidence), nullableString(memory.SourceEvidenceID), nullableString(memory.SourceTaskID), boolInt(memory.UserEditable), memory.RetentionPolicy, stamp(now), stamp(now))
	if err != nil {
		return domain.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, err
	}
	return memory, nil
}

func (s *Store) Memory(ctx context.Context, id string) (domain.Memory, error) {
	memory, err := scanMemory(s.db.QueryRowContext(ctx, memorySelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Memory{}, ErrNotFound
	}
	return memory, err
}

func (s *Store) ListMemories(ctx context.Context, status domain.MemoryStatus, scope string, limit int) ([]domain.Memory, error) {
	if status != "" && status != domain.MemoryCandidate && status != domain.MemoryAccepted && status != domain.MemoryStale && status != domain.MemorySuperseded && status != domain.MemoryRejected {
		return nil, fmt.Errorf("%w: invalid memory status %q", domain.ErrInvalidMemory, status)
	}
	if scope != "" && scope != "global" && scope != "task" {
		return nil, fmt.Errorf("%w: invalid memory scope %q", domain.ErrInvalidMemory, scope)
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	query := memorySelect
	args := make([]any, 0, 3)
	conditions := make([]string, 0, 2)
	if status != "" {
		conditions = append(conditions, "status=?")
		args = append(args, status)
	}
	if scope != "" {
		conditions = append(conditions, "scope=?")
		args = append(args, scope)
	}
	if len(conditions) != 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)
	return queryMemories(ctx, s.db, query, args...)
}

func (s *Store) UpdateMemory(ctx context.Context, id, kind, text string, metadata map[string]any) (domain.Memory, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, err
	}
	defer tx.Rollback()
	memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Memory{}, ErrNotFound
	}
	if err != nil {
		return domain.Memory{}, err
	}
	if !memory.UserEditable {
		return domain.Memory{}, fmt.Errorf("%w: memory is not user-editable", ErrConflict)
	}
	memory.Kind = kind
	memory.Text = strings.TrimSpace(text)
	memory.Metadata = metadata
	if memory.Metadata == nil {
		memory.Metadata = map[string]any{}
	}
	if err := memory.Validate(); err != nil {
		return domain.Memory{}, err
	}
	encoded, err := json.Marshal(memory.Metadata)
	if err != nil {
		return domain.Memory{}, err
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE memories SET kind=?,text=?,metadata_json=?,updated_at=? WHERE id=? AND user_editable=1`, memory.Kind, memory.Text, string(encoded), stamp(now), id)
	if err != nil {
		return domain.Memory{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.Memory{}, err
	}
	if count != 1 {
		return domain.Memory{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, err
	}
	return s.Memory(ctx, id)
}

func (s *Store) SetMemoryStatus(ctx context.Context, id string, status domain.MemoryStatus) (domain.Memory, error) {
	if status != domain.MemoryAccepted && status != domain.MemoryStale && status != domain.MemorySuperseded && status != domain.MemoryRejected {
		return domain.Memory{}, fmt.Errorf("%w: invalid memory decision %q", domain.ErrInvalidMemory, status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, err
	}
	defer tx.Rollback()
	memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Memory{}, ErrNotFound
	}
	if err != nil {
		return domain.Memory{}, err
	}
	if memory.Status == status {
		return memory, nil
	}
	allowed := false
	switch memory.Status {
	case domain.MemoryCandidate:
		allowed = status == domain.MemoryAccepted || status == domain.MemoryStale || status == domain.MemorySuperseded || status == domain.MemoryRejected
	case domain.MemoryAccepted:
		allowed = status == domain.MemoryStale || status == domain.MemorySuperseded || status == domain.MemoryRejected
	case domain.MemoryStale:
		allowed = status == domain.MemoryAccepted || status == domain.MemorySuperseded || status == domain.MemoryRejected
	}
	if !allowed {
		return domain.Memory{}, fmt.Errorf("%w: cannot change memory status from %s to %s", ErrConflict, memory.Status, status)
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE memories SET status=?,updated_at=? WHERE id=? AND status=?`, status, stamp(now), id, memory.Status)
	if err != nil {
		return domain.Memory{}, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return domain.Memory{}, err
	} else if count != 1 {
		return domain.Memory{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, err
	}
	return s.Memory(ctx, id)
}

func (s *Store) SearchMemories(ctx context.Context, query, taskID string, limit int) ([]domain.MemoryMatch, error) {
	if len(query) > 20000 {
		return nil, fmt.Errorf("%w: memory query exceeds 20000 bytes", domain.ErrInvalidMemory)
	}
	tokens := memoryTokens(query)
	if len(tokens) == 0 {
		return []domain.MemoryMatch{}, nil
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, memorySelect+` WHERE status='accepted' AND (scope='global' OR (scope='task' AND source_task_id=?)) ORDER BY updated_at DESC LIMIT 2000`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []domain.MemoryMatch
	normalizedQuery := strings.ToLower(strings.TrimSpace(query))
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		text := strings.ToLower(memory.Text)
		score := 0
		for _, token := range tokens {
			if strings.Contains(text, token) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		if len(normalizedQuery) >= 3 && strings.Contains(text, normalizedQuery) {
			score += 2
		}
		if memory.Confidence != nil && *memory.Confidence >= 0.8 {
			score++
		}
		matches = append(matches, domain.MemoryMatch{Memory: memory, Relevance: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Relevance == matches[j].Relevance {
			return matches[i].Memory.UpdatedAt.After(matches[j].Memory.UpdatedAt)
		}
		return matches[i].Relevance > matches[j].Relevance
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	if matches == nil {
		matches = []domain.MemoryMatch{}
	}
	return matches, nil
}

const memorySelect = `SELECT id,kind,status,text,metadata_json,scope,confidence,source_evidence_id,source_task_id,user_editable,retention_policy,created_at,updated_at FROM memories`

type memoryScanner interface {
	Scan(...any) error
}

func scanMemory(scanner memoryScanner) (domain.Memory, error) {
	var memory domain.Memory
	var metadata string
	var confidence sql.NullFloat64
	var sourceEvidenceID, sourceTaskID sql.NullString
	var userEditable int
	var createdAt, updatedAt string
	if err := scanner.Scan(&memory.ID, &memory.Kind, &memory.Status, &memory.Text, &metadata, &memory.Scope, &confidence, &sourceEvidenceID, &sourceTaskID, &userEditable, &memory.RetentionPolicy, &createdAt, &updatedAt); err != nil {
		return domain.Memory{}, err
	}
	if err := json.Unmarshal([]byte(metadata), &memory.Metadata); err != nil {
		return domain.Memory{}, err
	}
	if memory.Metadata == nil {
		memory.Metadata = map[string]any{}
	}
	if confidence.Valid {
		memory.Confidence = &confidence.Float64
	}
	if sourceEvidenceID.Valid {
		memory.SourceEvidenceID = sourceEvidenceID.String
	}
	if sourceTaskID.Valid {
		memory.SourceTaskID = sourceTaskID.String
	}
	memory.UserEditable = userEditable == 1
	memory.CreatedAt = parseTime(createdAt)
	memory.UpdatedAt = parseTime(updatedAt)
	return memory, nil
}

func queryMemories(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]domain.Memory, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, memory)
	}
	return items, rows.Err()
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func memoryTokens(query string) []string {
	seen := make(map[string]struct{})
	tokens := make([]string, 0, 16)
	for _, token := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len([]rune(token)) < 2 {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
		if len(tokens) == 32 {
			break
		}
	}
	return tokens
}
