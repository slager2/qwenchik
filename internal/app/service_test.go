package app

import (
	"context"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

type taskCaptureRepository struct {
	Repository
	task    domain.Task
	message domain.Message
}

func (r *taskCaptureRepository) CreateTask(_ context.Context, task domain.Task, _ string) (domain.Task, error) {
	r.task = task
	return task, nil
}

func (r *taskCaptureRepository) GetTask(_ context.Context, id string) (domain.Task, error) {
	return domain.Task{ID: id}, nil
}

func (r *taskCaptureRepository) AddMessage(_ context.Context, message domain.Message, _ string) (domain.Message, error) {
	r.message = message
	return message, nil
}

func TestCreateTaskAcceptsContextSizedDescription(t *testing.T) {
	description := strings.Repeat("research evidence ", 2000)
	repository := &taskCaptureRepository{}
	created, err := New(repository).CreateTask(context.Background(), "Large research request", description, "")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(description)
	if created.Description != want || repository.task.Description != want {
		t.Fatal("large task description was not preserved")
	}
}

func TestAddUserMessageAcceptsContextSizedContent(t *testing.T) {
	content := strings.Repeat("additional research context ", 1500)
	repository := &taskCaptureRepository{}
	message, err := New(repository).AddUserMessage(context.Background(), "task-large-message", content, "")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(content)
	if message.Content != want || repository.message.Content != want {
		t.Fatal("large user message was not preserved")
	}
}
