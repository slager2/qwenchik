package sandbox

import (
	"runtime"
	"testing"
)

// skipOnWindows skips tests of behaviour that is unsupported on Windows by
// design: the rootless Docker sandbox is unsupported on Windows. Tests of the safe "unavailable" path still run there.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unsupported on Windows: the rootless Docker sandbox is unsupported on Windows")
	}
}
