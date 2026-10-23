//go:build !windows

package resources

import "context"

func newWindowsTelemetrySource() windowsTelemetrySource {
	return windowsTelemetrySource{}
}

func platformTelemetryRunner() func(context.Context, string, ...string) ([]byte, error) {
	return runBounded
}
