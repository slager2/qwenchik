package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/spf13/cobra"
)

var runtimeUnitNames = []string{"qwenchik-model.service", "qwenchik-app.service"}

type runtimeOptions struct {
	ModelBin  string
	ModelPath string
	Device    string
	Context   int
	Workspace string
	AppBin    string
	Model     string
}

func newRuntimeCommand() *cobra.Command {
	command := &cobra.Command{Use: "runtime", Short: "Install and manage persistent login-user services"}
	options := runtimeOptions{}
	var enable bool
	install := &cobra.Command{
		Use: "install", Short: "Install model and app user units without starting them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if options.Context-cfg.LLMMaxOutputTokens < 2304 {
				return errors.New("runtime context is too small for LLM_MAX_OUTPUT_TOKENS and prompt reserves")
			}
			options.Model = cfg.LLMModel
			options.Workspace, err = os.Getwd()
			if err != nil {
				return err
			}
			options.AppBin, err = os.Executable()
			if err != nil {
				return err
			}
			if strings.Contains(filepath.ToSlash(options.AppBin), "/go-build") && strings.Contains(filepath.ToSlash(options.AppBin), "/exe/") {
				return errors.New("runtime installation requires a persistent qagent binary; run make build and ./qagent runtime install instead of go run")
			}
			configDir, err := os.UserConfigDir()
			if err != nil {
				return err
			}
			if err := installRuntime(configDir, options); err != nil {
				return err
			}
			if enable {
				if err := runtimeSystemctl(cmd, "daemon-reload"); err != nil {
					return err
				}
				if err := runtimeSystemctl(cmd, append([]string{"enable"}, runtimeUnitNames...)...); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Persistent user units installed; services have not been started.")
			return err
		},
	}
	install.Flags().StringVar(&options.ModelBin, "model-bin", "", "Path to the Vulkan llama-server executable (required)")
	install.Flags().StringVar(&options.ModelPath, "model-path", "", "Path to the readable GGUF model (required)")
	install.Flags().StringVar(&options.Device, "device", "Vulkan1", "llama-server GPU device")
	install.Flags().IntVar(&options.Context, "context", 32768, "Model and app context length")
	install.Flags().BoolVar(&enable, "enable", false, "Reload units and enable login autostart without starting services")
	_ = install.MarkFlagRequired("model-bin")
	_ = install.MarkFlagRequired("model-path")
	command.AddCommand(install)
	for _, action := range []string{"start", "status"} {
		action := action
		command.AddCommand(&cobra.Command{
			Use: action, Short: action + " model and app user services", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if action == "start" {
					if err := runtimeSystemctl(cmd, "daemon-reload"); err != nil {
						return err
					}
				}
				return runtimeSystemctl(cmd, append([]string{action, "--no-pager"}, runtimeUnitNames...)...)
			},
		})
	}
	return command
}

func runtimeSystemctl(cmd *cobra.Command, args ...string) error {
	process := exec.CommandContext(cmd.Context(), "systemctl", append([]string{"--user"}, args...)...)
	process.Stdin = cmd.InOrStdin()
	process.Stdout = cmd.OutOrStdout()
	process.Stderr = cmd.ErrOrStderr()
	if err := process.Run(); err != nil {
		return fmt.Errorf("systemctl --user %s: %w", args[0], err)
	}
	return nil
}

func renderRuntimeUnits(options runtimeOptions) (map[string][]byte, error) {
	for name, value := range map[string]string{
		"model executable": options.ModelBin, "model path": options.ModelPath,
		"device": options.Device, "workspace": options.Workspace,
		"app executable": options.AppBin, "model alias": options.Model,
	} {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("invalid %s: must be nonempty and contain no newline or NUL", name)
		}
	}
	for _, path := range []string{options.ModelBin, options.ModelPath, options.Workspace, options.AppBin} {
		if !filepath.IsAbs(path) {
			return nil, errors.New("runtime paths must be absolute")
		}
	}
	if options.Context <= 0 {
		return nil, errors.New("runtime context must be positive")
	}
	quote := func(value string) string {
		return strings.ReplaceAll(strconv.Quote(value), "%", "%%")
	}
	execLine := func(args ...string) string {
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = quote(arg)
		}
		return ":" + strings.Join(quoted, " ")
	}
	context := strconv.Itoa(options.Context)
	workingDirectory := strings.ReplaceAll(options.Workspace, "%", "%%") + "/."
	model := "[Unit]\nDescription=Qwenchik local Vulkan model\nStartLimitIntervalSec=0\n\n[Service]\nType=exec\nExecStart=" + execLine(
		options.ModelBin, "-m", options.ModelPath, "--device", options.Device, "-ngl", "99",
		"--host", "127.0.0.1", "--port", "8000", "--alias", options.Model,
		"--jinja", "--parallel", "1", "-c", context, "-fa", "on", "-ctk", "q8_0", "-ctv", "q8_0",
		"--reasoning", "off", "--no-webui",
	) + "\nRestart=on-failure\nRestartSec=5s\nTimeoutStopSec=30s\n\n[Install]\nWantedBy=default.target\n"
	app := "[Unit]\nDescription=Qwenchik API and worker\nWants=qwenchik-model.service\nAfter=qwenchik-model.service\nStartLimitIntervalSec=0\n\n[Service]\nType=exec\nWorkingDirectory=" + workingDirectory + "\nEnvironment=\"PATH=/usr/local/bin:/usr/bin:/bin\"\nEnvironment=\"LLM_CONTEXT_LENGTH=" + context + "\"\nEnvironment=\"SEARCH_ENABLE_DDG=false\"\nExecStartPre=" + execLine(
		"/usr/bin/curl", "--noproxy", "*", "--fail", "--silent", "--output", "/dev/null",
		"--connect-timeout", "2", "--max-time", "3", "--retry", "90", "--retry-all-errors",
		"--retry-delay", "2", "--retry-max-time", "180", "http://127.0.0.1:8000/health",
	) + "\nExecStart=" + execLine(options.AppBin, "serve") + "\nRestart=on-failure\nRestartSec=5s\nTimeoutStartSec=240s\nTimeoutStopSec=30s\n\n[Install]\nWantedBy=default.target\n"
	return map[string][]byte{runtimeUnitNames[0]: []byte(model), runtimeUnitNames[1]: []byte(app)}, nil
}

func installRuntime(configDir string, options runtimeOptions) error {
	for _, path := range []*string{&options.ModelBin, &options.ModelPath, &options.Workspace, &options.AppBin} {
		if *path == "" || strings.ContainsAny(*path, "\r\n\x00") {
			return errors.New("runtime paths must be nonempty and contain no newline or NUL")
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		*path = absolute
	}
	units, err := renderRuntimeUnits(options)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		path       string
		executable bool
	}{{options.ModelBin, true}, {options.ModelPath, false}, {options.AppBin, true}} {
		file, err := os.Open(item.path)
		if err != nil {
			return fmt.Errorf("open runtime file: %w", err)
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || (item.executable && info.Mode().Perm()&0111 == 0) {
			return fmt.Errorf("runtime file must be regular and executable when required: %s", item.path)
		}
	}
	info, err := os.Stat(options.Workspace)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("runtime workspace must be a directory")
	}
	dir := filepath.Join(configDir, "systemd", "user")
	var missing []string
	for _, name := range runtimeUnitNames {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace existing user unit: %s", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(contents, units[name]) {
			return fmt.Errorf("existing user unit differs; review and move it aside before installing: %s", path)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	staged := make(map[string]string)
	defer func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}()
	for _, name := range missing {
		file, err := os.CreateTemp(dir, ".qwenchik-unit-*")
		if err != nil {
			return err
		}
		staged[name] = file.Name()
		_, writeErr := file.Write(units[name])
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
	}
	var installed []string
	for _, name := range missing {
		path := filepath.Join(dir, name)
		if err := os.Link(staged[name], path); err != nil {
			for _, created := range installed {
				_ = os.Remove(created)
			}
			return fmt.Errorf("install user unit without replacing existing files: %w", err)
		}
		installed = append(installed, path)
	}
	return nil
}
