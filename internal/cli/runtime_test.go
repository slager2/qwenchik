package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeFixture(t *testing.T) runtimeOptions {
	t.Helper()
	dir := t.TempDir()
	options := runtimeOptions{
		ModelBin: filepath.Join(dir, "model server"), ModelPath: filepath.Join(dir, "model weights.gguf"),
		AppBin: filepath.Join(dir, "qagent"), Workspace: dir,
		Device: "Vulkan1", Context: 32768, Model: "local-qwen",
	}
	for _, path := range []string{options.ModelBin, options.AppBin} {
		if err := os.WriteFile(path, []byte("executable fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(options.ModelPath, []byte("model fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return options
}

func TestRuntimeRenderingEscapesSystemdInputsAndKeepsSecretsOut(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "runtime-test-secret")
	options := runtimeOptions{
		ModelBin: "/models/bin %i/$SERVER/llama\"server", ModelPath: "/models/a \\ b %n $WEIGHTS.gguf",
		AppBin: "/workspace with spaces/%h/$APP/qagent", Workspace: "/workspace with spaces/%h/$APP",
		Device: "Vulkan2", Context: 16384, Model: "custom \"alias\" %n $ALIAS",
	}
	units, err := renderRuntimeUnits(options)
	if err != nil {
		t.Fatal(err)
	}
	model := string(units[runtimeUnitNames[0]])
	app := string(units[runtimeUnitNames[1]])
	for _, want := range []string{
		`ExecStart=:"/models/bin %%i/$SERVER/llama\"server"`,
		`"/models/a \\ b %%n $WEIGHTS.gguf"`,
		`"--alias" "custom \"alias\" %%n $ALIAS"`,
		`"--device" "Vulkan2"`, `"-ngl" "99"`, `"-c" "16384"`,
		`"--host" "127.0.0.1" "--port" "8000"`,
		`"--parallel" "1"`, `"-ctk" "q8_0" "-ctv" "q8_0"`,
	} {
		if !strings.Contains(model, want) {
			t.Errorf("model unit missing %q: %s", want, model)
		}
	}
	for _, want := range []string{
		"WorkingDirectory=/workspace with spaces/%%h/$APP/.\n",
		`ExecStart=:"/workspace with spaces/%%h/$APP/qagent" "serve"`,
		`Environment="LLM_CONTEXT_LENGTH=16384"`, `Environment="SEARCH_ENABLE_DDG=false"`,
		`Environment="PATH=/usr/local/bin:/usr/bin:/bin"`,
		"Wants=qwenchik-model.service", "After=qwenchik-model.service",
		`ExecStartPre=:"/usr/bin/curl"`, `"--retry-max-time" "180"`,
		`"http://127.0.0.1:8000/health"`, "TimeoutStartSec=240s",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app unit missing %q: %s", want, app)
		}
	}
	for name, contents := range units {
		for _, forbidden := range []string{"runtime-test-secret", "EnvironmentFile=", "Requires=", ".env", "/bin/sh", "TAVILY_API_KEY"} {
			if bytes.Contains(contents, []byte(forbidden)) {
				t.Errorf("%s contains forbidden %q", name, forbidden)
			}
		}
		for _, want := range []string{"Restart=on-failure", "WantedBy=default.target"} {
			if !bytes.Contains(contents, []byte(want)) {
				t.Errorf("%s missing %q", name, want)
			}
		}
	}
}

func TestRuntimeRenderingRejectsInjectionAndInvalidValues(t *testing.T) {
	base := runtimeFixture(t)
	for _, field := range []string{"bin", "weights", "app", "workspace", "device", "alias"} {
		for _, invalid := range []string{"", "bad\nExecStart=evil", "bad\rvalue", "bad\x00value"} {
			options := base
			switch field {
			case "bin":
				options.ModelBin = invalid
			case "weights":
				options.ModelPath = invalid
			case "app":
				options.AppBin = invalid
			case "workspace":
				options.Workspace = invalid
			case "device":
				options.Device = invalid
			case "alias":
				options.Model = invalid
			}
			if _, err := renderRuntimeUnits(options); err == nil {
				t.Errorf("accepted invalid %s %q", field, invalid)
			}
		}
	}
	base.Context = 0
	if _, err := renderRuntimeUnits(base); err == nil {
		t.Fatal("accepted zero context")
	}
	base.Context = 32768
	base.ModelBin = "relative-server"
	if _, err := renderRuntimeUnits(base); err == nil {
		t.Fatal("accepted relative executable")
	}
}

func TestRuntimeInstallIsPersistentPrivateAndIdempotent(t *testing.T) {
	options := runtimeFixture(t)
	configDir := filepath.Join(t.TempDir(), "custom config % with spaces")
	if err := installRuntime(configDir, options); err != nil {
		t.Fatal(err)
	}
	want, err := renderRuntimeUnits(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range runtimeUnitNames {
		path := filepath.Join(configDir, "systemd", "user", name)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want[name]) {
			t.Fatalf("installed %s: err=%v contents=%s", name, err, got)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if before.Mode().Perm() != 0600 {
			t.Fatalf("unit permissions = %o", before.Mode().Perm())
		}
		if err := installRuntime(configDir, options); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before, after) {
			t.Fatal("idempotent install replaced the existing unit")
		}
	}
	entries, err := os.ReadDir(filepath.Join(configDir, "systemd", "user"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected staged files: %v, %v", entries, err)
	}
}

func TestRuntimeInstallConflictsDoNotPartiallyWrite(t *testing.T) {
	for _, existing := range runtimeUnitNames {
		t.Run(existing, func(t *testing.T) {
			options := runtimeFixture(t)
			configDir := t.TempDir()
			dir := filepath.Join(configDir, "systemd", "user")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, existing)
			original := []byte("unfamiliar user unit\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := installRuntime(configDir, options); err == nil {
				t.Fatal("expected conflict")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("partial install: %v, %v", entries, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("existing unit changed")
			}
		})
	}
}

func TestRuntimeInstallRejectsInvalidFilesBeforeWriting(t *testing.T) {
	for _, failure := range []string{"missing-bin", "non-executable", "missing-model", "model-directory", "empty-path", "symlink-unit"} {
		t.Run(failure, func(t *testing.T) {
			options := runtimeFixture(t)
			configDir := t.TempDir()
			switch failure {
			case "missing-bin":
				options.ModelBin += "missing"
			case "non-executable":
				if err := os.Chmod(options.ModelBin, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-model":
				options.ModelPath += "missing"
			case "model-directory":
				options.ModelPath = options.Workspace
			case "empty-path":
				options.ModelPath = ""
			case "symlink-unit":
				dir := filepath.Join(configDir, "systemd", "user")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(options.ModelPath, filepath.Join(dir, runtimeUnitNames[1])); err != nil {
					t.Fatal(err)
				}
			}
			if err := installRuntime(configDir, options); err == nil {
				t.Fatal("expected validation failure")
			}
			if _, err := os.Lstat(filepath.Join(configDir, "systemd", "user", runtimeUnitNames[0])); !os.IsNotExist(err) {
				t.Fatalf("model unit was written: %v", err)
			}
		})
	}
}

func TestRuntimeInstallFlags(t *testing.T) {
	root := NewRoot()
	command, _, err := root.Find([]string{"runtime", "install"})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"device": "Vulkan1", "context": "32768", "enable": "false"} {
		if flag := command.Flags().Lookup(name); flag == nil || flag.DefValue != want {
			t.Fatalf("unexpected %s flag: %v", name, flag)
		}
	}
	root.SetArgs([]string{"runtime", "install"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "required flag") {
		t.Fatalf("required paths were not enforced: %v", err)
	}
}

func TestRuntimeInstallRejectsContextBelowOutputReserveBeforeWriting(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("LLM_MAX_OUTPUT_TOKENS", "4096")
	root := NewRoot()
	root.SetArgs([]string{"runtime", "install", "--model-bin", "/missing/llama-server", "--model-path", "/missing/model.gguf", "--context", "4096"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "too small") {
		t.Fatalf("runtime accepted incompatible context/output budgets: %v", err)
	}
	entries, err := os.ReadDir(configDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed context validation wrote units: entries=%v err=%v", entries, err)
	}
}

func TestRuntimeInstallCommandUsesUserConfigDirWithoutSystemctl(t *testing.T) {
	options := runtimeFixture(t)
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LLM_MODEL", "runtime-test-alias")
	t.Setenv("TAVILY_API_KEY", "runtime-command-secret")
	root := NewRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"runtime", "install", "--model-bin", options.ModelBin, "--model-path", options.ModelPath})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range runtimeUnitNames {
		contents, err := os.ReadFile(filepath.Join(configDir, "systemd", "user", name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, []byte("runtime-command-secret")) {
			t.Fatal("credential copied into a unit")
		}
		if name == runtimeUnitNames[0] && !bytes.Contains(contents, []byte(`"--alias" "runtime-test-alias"`)) {
			t.Fatal("configured model alias missing")
		}
	}
	if strings.Contains(output.String(), "runtime-command-secret") {
		t.Fatal("credential exposed in command output")
	}
}
