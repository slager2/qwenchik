package keepawake

import (
	"context"
	"errors"
)

type PowerSource string

const (
	PowerUnknown PowerSource = "unknown"
	PowerAC      PowerSource = "AC"
	PowerBattery PowerSource = "battery"
)

type Status struct {
	Provider      string      `json:"provider"`
	Available     bool        `json:"available"`
	Active        bool        `json:"active"`
	PowerSource   PowerSource `json:"power_source"`
	FailureReason string      `json:"failure_reason,omitempty"`
}

type Provider interface {
	Acquire(context.Context, string) (func(), Status, error)
}

var ErrInhibitorUnavailable = errors.New("sleep inhibitor unavailable")

func NewProvider() Provider { return newPlatformProvider() }
