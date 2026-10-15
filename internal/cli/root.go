package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/slager2/qwenchik/internal/api"
	"github.com/slager2/qwenchik/internal/app"
	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm/openai"
	"github.com/slager2/qwenchik/internal/orchestrator"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/sandbox"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
	"github.com/slager2/qwenchik/internal/workerlock"
	"github.com/spf13/cobra"
)

type output struct {
	json bool
	out  io.Writer
	err  io.Writer
}

type dependencies struct {
	cfg    config.Config
	store  *sqlite.Store
	app    *app.Service
	worker *orchestrator.Worker
}

func NewRoot() *cobra.Command {
	var jsonMode bool
	root := &cobra.Command{
		Use:           "qagent",
		Short:         "Local Qwen task agent",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&jsonMode, "json", false, "write machine-readable JSON")
	root.AddCommand(newServeCommand())
	root.AddCommand(newDoctorCommand(&jsonMode))
	root.AddCommand(newTaskCommand(&jsonMode))
	root.AddCommand(newRunCommand(&jsonMode))
	root.AddCommand(newEventsCommand(&jsonMode))
	root.AddCommand(newTaskDataCommand("checkpoints", &jsonMode))
	root.AddCommand(newTaskDataCommand("artifacts", &jsonMode))
	root.AddCommand(newTaskDataCommand("evidence", &jsonMode))
	root.AddCommand(newSimpleCommand("providers", "Show configured provider availability", &jsonMode))
	root.AddCommand(newSimpleCommand("config", "Show non-secret configuration", &jsonMode))
	return root
}

func newTaskDataCommand(name string, jsonMode *bool) *cobra.Command {
	return &cobra.Command{
		Use:   name + " TASK_ID",
		Short: "List task " + name,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			var value any
			switch name {
			case "checkpoints":
				value, err = deps.app.Checkpoints(cmd.Context(), args[0])
			case "artifacts":
				value, err = deps.app.Artifacts(cmd.Context(), args[0])
			case "evidence":
				value, err = deps.app.Evidence(cmd.Context(), args[0])
			}
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, value)
			return nil
		},
	}
}

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the local HTTP API and sequential worker",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context())
		},
	}
}

func newDoctorCommand(jsonMode *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local runtime, storage, and model availability",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			checks := doctor(cmd.Context(), cfg)
			status := "ok"
			for _, check := range checks {
				if check.Status == "error" {
					status = "error"
				}
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{"status": status, "checks": checks})
			if status == "error" {
				return errors.New("doctor found required checks in error state")
			}
			return nil
		},
	}
}

func newTaskCommand(jsonMode *bool) *cobra.Command {
	task := &cobra.Command{Use: "task", Short: "Manage durable tasks"}
	var title string
	create := &cobra.Command{
		Use:   "create TASK",
		Short: "Create an inbox task",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			item, err := deps.app.CreateTask(cmd.Context(), title, strings.Join(args, " "), "")
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, item)
			return nil
		},
	}
	create.Flags().StringVar(&title, "title", "", "short task title")
	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			items, err := deps.app.ListTasks(cmd.Context())
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, items)
			return nil
		},
	}
	show := &cobra.Command{
		Use:   "show TASK_ID",
		Short: "Show one task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			item, err := deps.app.GetTask(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, item)
			return nil
		},
	}
	queue := &cobra.Command{
		Use:   "run TASK_ID",
		Short: "Queue a task for the running qagent serve worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			item, err := deps.app.QueueTask(cmd.Context(), args[0], "")
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, item)
			return nil
		},
	}
	pause := taskMutation("pause", jsonMode, func(service *app.Service, ctx context.Context, id string) (domain.Task, error) {
		return service.PauseTask(ctx, id)
	})
	resume := taskMutation("resume", jsonMode, func(service *app.Service, ctx context.Context, id string) (domain.Task, error) {
		return service.ResumeTask(ctx, id, "")
	})
	cancel := taskMutation("cancel", jsonMode, func(service *app.Service, ctx context.Context, id string) (domain.Task, error) {
		return service.CancelTask(ctx, id)
	})
	task.AddCommand(create, list, show, queue, pause, resume, cancel)
	return task
}

func taskMutation(name string, jsonMode *bool, mutate func(*app.Service, context.Context, string) (domain.Task, error)) *cobra.Command {
	return &cobra.Command{
		Use:   name + " TASK_ID",
		Short: strings.ToUpper(name[:1]) + name[1:] + " a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			item, err := mutate(deps.app, cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, item)
			return nil
		},
	}
}

func newRunCommand(jsonMode *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "run TASK",
		Short: "Create and queue a task for the running server worker",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			item, err := deps.app.CreateTask(cmd.Context(), "", strings.Join(args, " "), "")
			if err != nil {
				return err
			}
			item, err = deps.app.QueueTask(cmd.Context(), item.ID, "")
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, item)
			return nil
		},
	}
}

func newEventsCommand(jsonMode *bool) *cobra.Command {
	command := &cobra.Command{
		Use:   "events TASK_ID",
		Short: "Read durable events; follow polls SQLite until interrupted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			follow, _ := cmd.Flags().GetBool("follow")
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			var after int64
			for {
				events, err := deps.app.Events(cmd.Context(), args[0], after, 200)
				if err != nil {
					return err
				}
				for _, event := range events {
					after = event.Seq
					if event.Visibility == "user" {
						printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, event)
					}
				}
				if !follow {
					return nil
				}
				task, err := deps.app.GetTask(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if task.Status.Terminal() {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(500 * time.Millisecond):
				}
			}
		},
	}
	command.Flags().Bool("follow", false, "follow events until the task finishes or Ctrl-C")
	return command
}

func newSimpleCommand(name, short string, jsonMode *bool) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			switch name {
			case "providers":
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{
					"llm":     map[string]any{"type": "local-openai-compatible", "base_url": cfg.LLMBaseURL, "model": cfg.LLMModel},
					"search":  map[string]any{"status": providerStatus(cfg.SearXNGURL), "type": "searxng", "base_url": cfg.SearXNGURL},
					"sandbox": map[string]any{"runtime": cfg.SandboxRuntime, "image": cfg.SandboxImage},
				})
			case "config":
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{
					"app_bind_addr": cfg.BindAddr, "database_path": cfg.DatabasePath, "artifacts_path": cfg.ArtifactsPath,
					"llm_base_url": cfg.LLMBaseURL, "llm_model": cfg.LLMModel, "searxng_url": cfg.SearXNGURL,
					"workspace_root": cfg.WorkspaceRoot, "sandbox_runtime": cfg.SandboxRuntime,
					"sandbox_image": cfg.SandboxImage, "sandbox_network": cfg.SandboxNetwork,
					"llm_context_length": cfg.LLMContextLength, "llm_max_output_tokens": cfg.LLMMaxOutputTokens,
					"llm_max_concurrency": cfg.LLMMaxConcurrency, "allowed_domains": cfg.AllowedDomains,
				})
			}
			return nil
		},
	}
}

func serve(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	deps, err := openDependenciesWithConfig(parent, cfg)
	if err != nil {
		return err
	}
	defer deps.store.Close()
	lock, err := workerlock.Acquire(cfg.DatabasePath + ".worker.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	model := openai.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, time.Duration(cfg.LLMRequestTimeoutSecs)*time.Second, cfg.LLMMaxOutputTokens, cfg.LLMTemperature, cfg.LLMTopP, cfg.LLMEnableThinking)
	registry := tools.NewRegistry()
	var searchProvider research.SearchProvider
	if cfg.SearXNGURL != "" {
		searchProvider, err = research.NewSearXNG(cfg.SearXNGURL, nil)
		if err != nil {
			return err
		}
	}
	artifactStore, err := artifacts.New(cfg.ArtifactsPath, cfg.MaxFetchBytes)
	if err != nil {
		return err
	}
	sandboxRuntime, err := sandbox.New(cfg.SandboxImage, cfg.WorkspaceRoot, nil, sandbox.Limits{OutputBytes: 64 << 10, MaxFileCount: 100, MaxFileBytes: cfg.MaxFetchBytes})
	if err != nil {
		return err
	}
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{
		SearchProvider: searchProvider,
		Fetcher:        research.NewSafeFetcherWithPolicy(cfg.MaxFetchBytes, 20*time.Second, nil, cfg.AllowedDomains),
		Extractor:      research.NewHTMLExtractor(100_000), Artifacts: artifactStore,
		Repository: deps.store, MaxFetchBytes: cfg.MaxFetchBytes, AllowedDomains: cfg.AllowedDomains,
	}); err != nil {
		return err
	}
	if err := tools.RegisterSandboxTools(registry, tools.SandboxTools{Runtime: sandboxRuntime, Artifacts: artifactStore, Repository: deps.store}); err != nil {
		return err
	}
	deps.worker = orchestrator.New(deps.store, model, registry, orchestrator.Options{
		MaxSteps: cfg.MaxSteps, MaxSearchQueries: cfg.MaxSearchQueries, MaxFetchBytes: cfg.MaxFetchBytes,
		TaskTimeout:     time.Duration(cfg.TaskTimeoutSeconds) * time.Second,
		MaxOutputTokens: cfg.LLMMaxOutputTokens, MaxInputBytes: maxInputBytes(cfg),
		Temperature: cfg.LLMTemperature, TopP: cfg.LLMTopP, Logger: logger,
	})
	apiServer := api.New(deps.app, deps.worker, deps.store.Ping, cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.SearXNGURL, logger)
	apiServer.SetArtifactReader(artifactStore)
	sandboxStatus := sandboxRuntime.Doctor(parent)
	apiServer.SetSandboxStatus(sandboxStatus.Available, sandboxStatus.Message)
	handler := apiServer.Handler()
	server := &http.Server{Addr: cfg.BindAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan error, 1)
	go func() { workerDone <- deps.worker.Run(ctx) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	logger.Info("qagent server started", "bind_addr", cfg.BindAddr, "database_path", cfg.DatabasePath, "llm_base_url", cfg.LLMBaseURL, "model", cfg.LLMModel)
	var serveErr error
	workerExited := false
	select {
	case <-ctx.Done():
	case err := <-serverDone:
		serveErr = err
	case err := <-workerDone:
		workerExited = true
		if err != nil {
			serveErr = fmt.Errorf("worker stopped: %w", err)
		} else if ctx.Err() == nil {
			serveErr = errors.New("worker stopped unexpectedly")
		}
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	if !workerExited {
		select {
		case <-workerDone:
		case <-shutdownCtx.Done():
			return errors.New("worker did not stop before shutdown deadline")
		}
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func providerStatus(baseURL string) string {
	if baseURL == "" {
		return "disabled"
	}
	return "configured"
}

func maxInputBytes(cfg config.Config) int {
	budget := (cfg.LLMContextLength - cfg.LLMMaxOutputTokens - 2048) * 2
	if budget > 20000 {
		return 20000
	}
	if budget < 1 {
		return 1
	}
	return budget
}

func openDependencies(ctx context.Context) (*dependencies, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return openDependenciesWithConfig(ctx, cfg)
}

func openDependenciesWithConfig(ctx context.Context, cfg config.Config) (*dependencies, error) {
	store, err := sqlite.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.ArtifactsPath, 0o700); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	return &dependencies{cfg: cfg, store: store, app: app.New(store)}, nil
}

type doctorCheck struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func doctor(ctx context.Context, cfg config.Config) map[string]doctorCheck {
	checks := map[string]doctorCheck{
		"go":   {Status: "ok", Message: "qagent binary is running on Go"},
		"bind": {Status: "ok", Message: "application bind address is loopback-only"},
	}
	if api.FrontendAvailable() {
		checks["frontend"] = doctorCheck{Status: "ok", Message: "embedded frontend assets are available"}
	} else {
		checks["frontend"] = doctorCheck{Status: "error", Message: "embedded frontend assets are missing; run make build"}
	}
	sandboxRuntime, sandboxErr := sandbox.New(cfg.SandboxImage, cfg.WorkspaceRoot, nil, sandbox.Limits{OutputBytes: 64 << 10, MaxFileCount: 100, MaxFileBytes: cfg.MaxFetchBytes})
	if sandboxErr != nil {
		checks["sandbox"] = doctorCheck{Status: "error", Message: "sandbox configuration is invalid"}
	} else if status := sandboxRuntime.Doctor(ctx); status.Available {
		checks["sandbox"] = doctorCheck{Status: "ok", Message: status.Message}
	} else {
		checks["sandbox"] = doctorCheck{Status: "warning", Message: status.Message}
	}
	store, err := sqlite.Open(ctx, cfg.DatabasePath)
	if err != nil {
		checks["database"] = doctorCheck{Status: "error", Message: "SQLite could not be opened"}
	} else {
		if err := store.Ping(ctx); err != nil {
			checks["database"] = doctorCheck{Status: "error", Message: "SQLite ping failed"}
		} else {
			checks["database"] = doctorCheck{Status: "ok", Message: "SQLite is writable"}
		}
		_ = store.Close()
	}
	if err := os.MkdirAll(cfg.ArtifactsPath, 0o700); err != nil {
		checks["artifacts"] = doctorCheck{Status: "error", Message: "artifact directory is not writable"}
	} else if file, err := os.CreateTemp(cfg.ArtifactsPath, ".doctor-*"); err != nil {
		checks["artifacts"] = doctorCheck{Status: "error", Message: "artifact directory is not writable"}
	} else {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		checks["artifacts"] = doctorCheck{Status: "ok", Message: "artifact directory is writable"}
	}
	if cfg.SearXNGURL == "" {
		checks["search"] = doctorCheck{Status: "warning", Message: "SearXNG is not configured; web discovery is disabled"}
	} else if request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.SearXNGURL, "/")+"/search?q=qagent-doctor&format=json&safesearch=1", nil); err != nil {
		checks["search"] = doctorCheck{Status: "warning", Message: "SearXNG health request is invalid"}
	} else if response, err := (&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(request); err != nil {
		checks["search"] = doctorCheck{Status: "warning", Message: "Configured SearXNG endpoint is unavailable"}
	} else {
		_ = response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			checks["search"] = doctorCheck{Status: "ok", Message: "SearXNG search endpoint responded"}
		} else {
			checks["search"] = doctorCheck{Status: "warning", Message: fmt.Sprintf("SearXNG returned HTTP %d", response.StatusCode)}
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.LLMBaseURL, "/")+"/models", nil)
	if err == nil && cfg.LLMAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	}
	if err != nil {
		checks["llm"] = doctorCheck{Status: "error", Message: "local LLM health request is invalid"}
	} else if response, err := (&http.Client{
		Timeout:       3 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}).Do(request); err != nil {
		checks["llm"] = doctorCheck{Status: "error", Message: "local OpenAI-compatible LLM endpoint is unavailable"}
	} else {
		_ = response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			checks["llm"] = doctorCheck{Status: "ok", Message: "local model endpoint responded"}
		} else {
			checks["llm"] = doctorCheck{Status: "error", Message: fmt.Sprintf("local LLM returned HTTP %d", response.StatusCode)}
		}
	}
	return checks
}

func printValue(out output, value any) {
	if out.json {
		_ = json.NewEncoder(out.out).Encode(value)
		return
	}
	switch item := value.(type) {
	case domain.Task:
		fmt.Fprintf(out.out, "%s\t%s\t%s\n", item.ID, item.Status, item.Title)
	case []domain.Task:
		for _, task := range item {
			fmt.Fprintf(out.out, "%s\t%s\t%s\n", task.ID, task.Status, task.Title)
		}
	case domain.Event:
		fmt.Fprintf(out.out, "%d\t%s\t%s\n", item.Seq, item.Type, item.CreatedAt.Format(time.RFC3339))
	default:
		_ = json.NewEncoder(out.out).Encode(value)
	}
}

func Execute(ctx context.Context, args []string) error {
	root := NewRoot()
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}
