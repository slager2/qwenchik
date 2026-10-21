//go:build !linux

package keepawake

import "context"

type unavailableProvider struct{}

func newPlatformProvider() Provider { return unavailableProvider{} }

func (unavailableProvider) Acquire(ctx context.Context, _ string) (func(), Status, error) {
	s := Status{Provider: "unavailable", PowerSource: PowerUnknown, FailureReason: "sleep inhibitor is unavailable on this platform"}
	if err := ctx.Err(); err != nil {
		return func() {}, s, err
	}
	return func() {}, s, ErrInhibitorUnavailable
}
