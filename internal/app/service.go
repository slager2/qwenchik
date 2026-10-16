package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

const maxTaskTextBytes = 512 << 10

type Repository interface {
	CreateTask(context.Context, domain.Task, string) (domain.Task, error)
	GetTask(context.Context, string) (domain.Task, error)
	ListTasks(context.Context, int) ([]domain.Task, error)
	TransitionTask(context.Context, string, domain.TaskStatus, *domain.Stage, string, string, map[string]any) (domain.Task, error)
	QueueTask(context.Context, string, string) (domain.Task, error)
	AddMessage(context.Context, domain.Message, string) (domain.Message, error)
	Messages(context.Context, string) ([]domain.Message, error)
	EventsAfter(context.Context, string, int64, int) ([]domain.Event, error)
	RecordEvent(context.Context, string, string, *domain.Stage, string, map[string]any) (domain.Event, error)
	SavePlan(context.Context, string, domain.Plan, any) (int64, error)
	SaveCheckpoint(context.Context, string, int, any) (domain.Checkpoint, error)
	Checkpoints(context.Context, string) ([]domain.Checkpoint, error)
	LatestCheckpoint(context.Context, string) (domain.Checkpoint, error)
	SetStepStatus(context.Context, string, string, string) error
	Steps(context.Context, string) ([]domain.TaskStep, error)
	Artifacts(context.Context, string) ([]domain.Artifact, error)
	Evidence(context.Context, string) ([]domain.Evidence, error)
	CreateApproval(context.Context, domain.Approval, any) (domain.Approval, error)
	Approval(context.Context, string) (domain.Approval, error)
	ApprovalsForTask(context.Context, string) ([]domain.Approval, error)
	PendingApprovals(context.Context) ([]domain.Approval, error)
	DecideApproval(context.Context, string, bool, string, string) (domain.Approval, error)
	ConsumeApproval(context.Context, string, string, json.RawMessage) (string, bool, error)
	CreateMemory(context.Context, domain.Memory) (domain.Memory, error)
	Memory(context.Context, string) (domain.Memory, error)
	ListMemories(context.Context, domain.MemoryStatus, string, int) ([]domain.Memory, error)
	UpdateMemory(context.Context, string, string, string, map[string]any) (domain.Memory, error)
	SetMemoryStatus(context.Context, string, domain.MemoryStatus) (domain.Memory, error)
	SearchMemories(context.Context, string, string, int) ([]domain.MemoryMatch, error)
	CompleteTask(context.Context, string, string, any) (domain.Task, error)
	ClaimNext(context.Context) (domain.Task, bool, error)
	RecoverInterrupted(context.Context) (int64, error)
}

type Service struct {
	repo Repository
}

func New(repo Repository) *Service { return &Service{repo: repo} }

func (s *Service) CreateTask(ctx context.Context, title, description, idempotencyKey string) (domain.Task, error) {
	description = strings.TrimSpace(description)
	if description == "" || len(description) > maxTaskTextBytes {
		return domain.Task{}, errors.New("description must contain 1 to 524288 bytes")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.SplitN(description, "\n", 2)[0]
		if len(title) > 120 {
			title = title[:117] + "..."
		}
	}
	if len(title) > 200 {
		return domain.Task{}, errors.New("title must not exceed 200 characters")
	}
	return s.repo.CreateTask(ctx, domain.Task{ID: newID(), Title: title, Description: description}, idempotencyKey)
}

func (s *Service) GetTask(ctx context.Context, id string) (domain.Task, error) {
	return s.repo.GetTask(ctx, id)
}

func (s *Service) ListTasks(ctx context.Context) ([]domain.Task, error) {
	return s.repo.ListTasks(ctx, 200)
}

func (s *Service) QueueTask(ctx context.Context, id, idempotencyKey string) (domain.Task, error) {
	return s.repo.QueueTask(ctx, id, idempotencyKey)
}

func (s *Service) AddUserMessage(ctx context.Context, taskID, content, idempotencyKey string) (domain.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > maxTaskTextBytes {
		return domain.Message{}, errors.New("message must contain 1 to 524288 bytes")
	}
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return domain.Message{}, err
	}
	return s.repo.AddMessage(ctx, domain.Message{ID: newID(), TaskID: taskID, Role: "user", Content: content}, idempotencyKey)
}

func (s *Service) Messages(ctx context.Context, taskID string) ([]domain.Message, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.Messages(ctx, taskID)
}

func (s *Service) QueueMessageTask(ctx context.Context, taskID, idempotencyKey string) (domain.Task, error) {
	message, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if message.Status == domain.StatusInbox || message.Status == domain.StatusPaused || message.Status == domain.StatusFailed {
		return s.repo.QueueTask(ctx, taskID, idempotencyKey)
	}
	return message, nil
}

func (s *Service) PauseTask(ctx context.Context, id string) (domain.Task, error) {
	return s.repo.TransitionTask(ctx, id, domain.StatusPaused, nil, "task.status_changed", "user", nil)
}

func (s *Service) ResumeTask(ctx context.Context, id, idempotencyKey string) (domain.Task, error) {
	task, err := s.repo.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status == domain.StatusQueued || task.Status == domain.StatusPlanning || task.Status == domain.StatusRunning {
		return task, nil
	}
	if task.Status != domain.StatusPaused {
		return domain.Task{}, fmt.Errorf("%w: only paused tasks can be resumed", sqlite.ErrConflict)
	}
	return s.repo.QueueTask(ctx, id, idempotencyKey)
}

func (s *Service) CancelTask(ctx context.Context, id string) (domain.Task, error) {
	return s.repo.TransitionTask(ctx, id, domain.StatusCancelled, nil, "task.cancelled", "user", nil)
}

func (s *Service) Events(ctx context.Context, taskID string, after int64, limit int) ([]domain.Event, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.EventsAfter(ctx, taskID, after, limit)
}

func (s *Service) Checkpoints(ctx context.Context, taskID string) ([]domain.Checkpoint, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.Checkpoints(ctx, taskID)
}

func (s *Service) Steps(ctx context.Context, taskID string) ([]domain.TaskStep, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.Steps(ctx, taskID)
}

func (s *Service) Artifacts(ctx context.Context, taskID string) ([]domain.Artifact, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.Artifacts(ctx, taskID)
}

func (s *Service) Evidence(ctx context.Context, taskID string) ([]domain.Evidence, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.Evidence(ctx, taskID)
}

func (s *Service) Approval(ctx context.Context, id string) (domain.Approval, error) {
	return s.repo.Approval(ctx, id)
}

func (s *Service) ApprovalsForTask(ctx context.Context, taskID string) ([]domain.Approval, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.ApprovalsForTask(ctx, taskID)
}

func (s *Service) PendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	return s.repo.PendingApprovals(ctx)
}

func (s *Service) DecideApproval(ctx context.Context, approvalID string, approve bool, idempotencyKey string) (domain.Approval, error) {
	if idempotencyKey != "" && len(idempotencyKey) > 200 {
		return domain.Approval{}, errors.New("idempotency key must not exceed 200 characters")
	}
	return s.repo.DecideApproval(ctx, approvalID, approve, "local_user", idempotencyKey)
}

func (s *Service) CreateMemory(ctx context.Context, memory domain.Memory) (domain.Memory, error) {
	memory.Text = strings.TrimSpace(memory.Text)
	memory.Status = domain.MemoryCandidate
	memory.UserEditable = true
	if memory.Scope == "" {
		memory.Scope = "global"
	}
	if memory.RetentionPolicy == "" {
		memory.RetentionPolicy = "until_deleted"
	}
	if memory.Metadata == nil {
		memory.Metadata = map[string]any{}
	}
	if err := memory.Validate(); err != nil {
		return domain.Memory{}, err
	}
	return s.repo.CreateMemory(ctx, memory)
}

func (s *Service) Memory(ctx context.Context, id string) (domain.Memory, error) {
	return s.repo.Memory(ctx, id)
}

func (s *Service) Memories(ctx context.Context, status domain.MemoryStatus, scope string) ([]domain.Memory, error) {
	return s.repo.ListMemories(ctx, status, scope, 200)
}

func (s *Service) UpdateMemory(ctx context.Context, id, kind, text string, metadata map[string]any) (domain.Memory, error) {
	text = strings.TrimSpace(text)
	if len(text) > 4000 {
		return domain.Memory{}, errors.New("memory text must not exceed 4000 bytes")
	}
	return s.repo.UpdateMemory(ctx, id, kind, text, metadata)
}

func (s *Service) SetMemoryStatus(ctx context.Context, id string, status domain.MemoryStatus) (domain.Memory, error) {
	if status != domain.MemoryAccepted && status != domain.MemoryStale && status != domain.MemorySuperseded && status != domain.MemoryRejected {
		return domain.Memory{}, fmt.Errorf("%w: invalid memory decision %q", domain.ErrInvalidMemory, status)
	}
	return s.repo.SetMemoryStatus(ctx, id, status)
}

func (s *Service) SearchMemories(ctx context.Context, query, taskID string, limit int) ([]domain.MemoryMatch, error) {
	query = strings.TrimSpace(query)
	if taskID != "" {
		if _, err := s.repo.GetTask(ctx, taskID); err != nil {
			return nil, err
		}
	}
	if query == "" {
		return []domain.MemoryMatch{}, nil
	}
	if len(query) > 20000 {
		return nil, fmt.Errorf("%w: memory query exceeds 20000 bytes", domain.ErrInvalidMemory)
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	return s.repo.SearchMemories(ctx, query, taskID, limit)
}

func (s *Service) AddAssistantMessage(ctx context.Context, taskID, content string) (domain.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 100000 {
		return domain.Message{}, errors.New("assistant message must contain 1 to 100000 characters")
	}
	return s.repo.AddMessage(ctx, domain.Message{ID: newID(), TaskID: taskID, Role: "assistant", Content: content}, "")
}

func (s *Service) CompleteTask(ctx context.Context, taskID, answer string, checkpoint any) (domain.Task, error) {
	if strings.TrimSpace(answer) == "" || len(answer) > 100000 {
		return domain.Task{}, errors.New("final answer must contain 1 to 100000 characters")
	}
	return s.repo.CompleteTask(ctx, taskID, answer, checkpoint)
}

func newID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic("cryptographic random source unavailable: " + err.Error())
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
}
