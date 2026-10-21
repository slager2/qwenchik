package domain

import (
	"context"
	"time"
)

type ExecutionLease struct {
	Owner     string    `json:"owner"`
	Epoch     int64     `json:"epoch"`
	ExpiresAt time.Time `json:"expires_at"`
}

type executionLeaseKey struct{}

func WithExecutionLease(ctx context.Context, lease ExecutionLease) context.Context {
	return context.WithValue(ctx, executionLeaseKey{}, lease)
}

func ExecutionLeaseFromContext(ctx context.Context) (ExecutionLease, bool) {
	lease, ok := ctx.Value(executionLeaseKey{}).(ExecutionLease)
	return lease, ok
}
