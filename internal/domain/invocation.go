package domain

import (
	"encoding/json"
	"time"
)

type Invocation struct {
	ID             string          `json:"id"`
	TaskID         string          `json:"task_id"`
	StepID         string          `json:"step_id,omitempty"`
	Role           string          `json:"role"`
	Kind           string          `json:"kind"`
	OperationID    string          `json:"operation_id"`
	InputHash      string          `json:"input_hash"`
	IdempotencyKey string          `json:"idempotency_key"`
	Status         string          `json:"status"`
	ErrorCode      string          `json:"error_code,omitempty"`
	OutputHash     string          `json:"output_hash,omitempty"`
	Attempt        int             `json:"attempt"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
}
