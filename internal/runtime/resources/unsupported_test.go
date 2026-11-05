package resources

import (
	"runtime"
	"testing"
)

// skipOnWindows skips tests of behaviour that is unsupported on Windows by
// design: host telemetry fixtures model Linux /proc and /sys collection. Tests of the safe "unavailable" path still run there.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unsupported on Windows: host telemetry fixtures model Linux /proc and /sys collection")
	}
}
