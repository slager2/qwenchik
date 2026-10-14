package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

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
	if description == "" || len(description) > 20000 {
		return domain.Task{}, errors.New("description must contain 1 to 20000 characters")
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
	if content == "" || len(content) > 20000 {
		return domain.Message{}, errors.New("message must contain 1 to 20000 characters")
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
