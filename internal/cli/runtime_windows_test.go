package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRuntimeCommandsAreUnsupportedBeforeSideEffects(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "user-config")
	path := t.TempDir()
	t.Setenv("APPDATA", configDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("PATH", path)

	for _, args := range [][]string{
		{"runtime", "install"},
		{"runtime", "start"},
		{"runtime", "status"},
	} {
		root := NewRoot()
		root.SetArgs(args)
		err := root.Execute()
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsupported on windows") {
			t.Errorf("%v error = %v, want unsupported on windows", args, err)
		}
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("runtime install created the user config directory: %v", err)
	}
}
