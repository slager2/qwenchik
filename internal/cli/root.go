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
	"github.com/slager2/qwenchik/internal/llm"
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
	root.AddCommand(newApprovalDecisionCommand("approve", true, &jsonMode))
	root.AddCommand(newApprovalDecisionCommand("reject", false, &jsonMode))
	root.AddCommand(newPendingApprovalsCommand(&jsonMode))
	root.AddCommand(newMemoriesCommand(&jsonMode))
	root.AddCommand(newMemoryCommand(&jsonMode))
	root.AddCommand(newTaskDataCommand("checkpoints", &jsonMode))
	root.AddCommand(newTaskDataCommand("artifacts", &jsonMode))
	root.AddCommand(newTaskDataCommand("evidence", &jsonMode))
	root.AddCommand(newSimpleCommand("providers", "Show configured provider availability", &jsonMode))
	root.AddCommand(newSimpleCommand("config", "Show non-secret configuration", &jsonMode))
	return root
}

func newMemoriesCommand(jsonMode *bool) *cobra.Command {
	var status, scope string
	command := &cobra.Command{
		Use:   "memories",
		Short: "List locally stored memories",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			items, err := deps.app.Memories(cmd.Context(), domain.MemoryStatus(status), scope)
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, items)
			return nil
		},
	}
	command.Flags().StringVar(&status, "status", "", "filter by candidate, accepted, stale, superseded, or rejected")
	command.Flags().StringVar(&scope, "scope", "", "filter by global or task scope")
	return command
}

func newMemoryCommand(jsonMode *bool) *cobra.Command {
	command := &cobra.Command{Use: "memory", Short: "Create, review, search, and edit durable memories"}
	command.AddCommand(newMemoryAddCommand(jsonMode))
	command.AddCommand(newMemoryEditCommand(jsonMode))
	command.AddCommand(newMemorySearchCommand(jsonMode))
	for _, decision := range []struct {
		name   string
		status domain.MemoryStatus
	}{
		{name: "accept", status: domain.MemoryAccepted},
		{name: "reject", status: domain.MemoryRejected},
		{name: "stale", status: domain.MemoryStale},
		{name: "supersede", status: domain.MemorySuperseded},
	} {
		decision := decision
		command.AddCommand(&cobra.Command{
			Use:   decision.name + " MEMORY_ID",
			Short: strings.ToUpper(decision.name[:1]) + decision.name[1:] + " a memory record",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				deps, err := openDependencies(cmd.Context())
				if err != nil {
					return err
				}
				defer deps.store.Close()
				memory, err := deps.app.SetMemoryStatus(cmd.Context(), args[0], decision.status)
				if err != nil {
					return err
				}
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, memory)
				return nil
			},
		})
	}
	return command
}

func newMemoryAddCommand(jsonMode *bool) *cobra.Command {
	var kind, scope, taskID, evidenceID string
	command := &cobra.Command{
		Use:   "add TEXT",
		Short: "Add a candidate memory; accept it explicitly before retrieval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			memory, err := deps.app.CreateMemory(cmd.Context(), domain.Memory{
				Kind: kind, Text: args[0], Scope: scope, SourceTaskID: taskID, SourceEvidenceID: evidenceID,
			})
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, memory)
			return nil
		},
	}
	command.Flags().StringVar(&kind, "kind", "fact", "memory kind, such as preference, fact, workflow, or constraint")
	command.Flags().StringVar(&scope, "scope", "global", "global or task")
	command.Flags().StringVar(&taskID, "task-id", "", "source task ID (required for task scope)")
	command.Flags().StringVar(&evidenceID, "evidence-id", "", "optional source evidence ID from the source task")
	return command
}

func newMemoryEditCommand(jsonMode *bool) *cobra.Command {
	var kind, text, metadataJSON string
	command := &cobra.Command{
		Use:   "edit MEMORY_ID",
		Short: "Edit a user-editable memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			var metadata map[string]any
			if metadataJSON != "" {
				if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
					return fmt.Errorf("metadata must be valid JSON object: %w", err)
				}
			} else {
				current, err := deps.app.Memory(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				metadata = current.Metadata
			}
			memory, err := deps.app.UpdateMemory(cmd.Context(), args[0], kind, text, metadata)
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, memory)
			return nil
		},
	}
	command.Flags().StringVar(&kind, "kind", "", "memory kind")
	command.Flags().StringVar(&text, "text", "", "replacement memory text")
	command.Flags().StringVar(&metadataJSON, "metadata", "", "replacement metadata JSON object (defaults to current metadata)")
	_ = command.MarkFlagRequired("kind")
	_ = command.MarkFlagRequired("text")
	return command
}

func newMemorySearchCommand(jsonMode *bool) *cobra.Command {
	var taskID string
	var limit int
	command := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search accepted memories with bounded lexical retrieval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			matches, err := deps.app.SearchMemories(cmd.Context(), args[0], taskID, limit)
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, matches)
			return nil
		},
	}
	command.Flags().StringVar(&taskID, "task-id", "", "include task-scoped memories for this task")
	command.Flags().IntVar(&limit, "limit", 5, "maximum matches (1 to 20)")
	return command
}

func newPendingApprovalsCommand(jsonMode *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "approvals",
		Short: "List pending action approvals",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			items, err := deps.app.PendingApprovals(cmd.Context())
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, items)
			return nil
		},
	}
}

func newApprovalDecisionCommand(name string, approve bool, jsonMode *bool) *cobra.Command {
	var idempotencyKey string
	command := &cobra.Command{
		Use:   name + " APPROVAL_ID",
		Short: strings.ToUpper(name[:1]) + name[1:] + " a pending dangerous action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := openDependencies(cmd.Context())
			if err != nil {
				return err
			}
			defer deps.store.Close()
			approval, err := deps.app.DecideApproval(cmd.Context(), args[0], approve, idempotencyKey)
			if err != nil {
				return err
			}
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, approval)
			return nil
		},
	}
	command.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "idempotency key for the decision")
	return command
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
				searchMode := "none"
				if cfg.SearXNGURL != "" {
					searchMode = "searxng"
				}
				if cfg.TavilyAPIKey != "" {
					searchMode = "tavily"
					if cfg.SearXNGURL != "" {
						searchMode = "composite"
					}
				}
				searchStatus := providerStatus(cfg.SearXNGURL)
				if cfg.TavilyAPIKey != "" {
					searchStatus = "configured"
				}
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{
					"llm": map[string]any{"type": "local-openai-compatible", "base_url": cfg.LLMBaseURL, "model": cfg.LLMModel},
					"search": map[string]any{
						"status":   searchStatus,
						"type":     "searxng",
						"mode":     searchMode,
						"base_url": cfg.SearXNGURL,
						"providers": map[string]any{
							"searxng": map[string]any{"status": providerStatus(cfg.SearXNGURL), "base_url": cfg.SearXNGURL},
							"tavily":  map[string]string{"status": providerStatusFromBool(cfg.TavilyAPIKey != "")},
						},
					},
					"sandbox": map[string]any{"runtime": cfg.SandboxRuntime, "image": cfg.SandboxImage},
				})
			case "config":
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{
					"app_bind_addr": cfg.BindAddr, "database_path": cfg.DatabasePath, "artifacts_path": cfg.ArtifactsPath,
					"llm_base_url": cfg.LLMBaseURL, "llm_model": cfg.LLMModel, "searxng_url": cfg.SearXNGURL,
					"tavily_configured": cfg.TavilyAPIKey != "",
					"workspace_root":    cfg.WorkspaceRoot, "sandbox_runtime": cfg.SandboxRuntime,
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
	searchProvider, err := configuredSearchProvider(cfg)
	if err != nil {
		return err
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
	apiServer.SetLLMModel(cfg.LLMModel)
	apiServer.SetTavilyConfigured(cfg.TavilyAPIKey != "")
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

func providerStatusFromBool(configured bool) string {
	if configured {
		return "configured"
	}
	return "disabled"
}

func configuredSearchProvider(cfg config.Config) (research.SearchProvider, error) {
	providers := make([]research.SearchProvider, 0, 2)
	if cfg.SearXNGURL != "" {
		provider, err := research.NewSearXNG(cfg.SearXNGURL, nil)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	if cfg.TavilyAPIKey != "" {
		provider, err := research.NewTavily(cfg.TavilyAPIKey, nil)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return nil, nil
	}
	return research.NewCompositeSearch(providers...), nil
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
		if cfg.TavilyAPIKey != "" {
			checks["search"] = doctorCheck{Status: "warning", Message: "Tavily is configured; external API health is checked during research"}
		} else {
			checks["search"] = doctorCheck{Status: "warning", Message: "No search provider is configured; web discovery is disabled"}
		}
	} else {
		searchProvider, err := research.NewSearXNG(cfg.SearXNGURL, nil)
		if err != nil {
			checks["search"] = doctorCheck{Status: "warning", Message: "Configured SearXNG endpoint is invalid"}
		} else {
			_, requestErr := searchProvider.Search(ctx, research.SearchOptions{Query: "qagent-doctor", MaxResults: 1, SafeSearch: true})
			if requestErr != nil {
				if cfg.TavilyAPIKey != "" {
					checks["search"] = doctorCheck{Status: "warning", Message: "SearXNG is unavailable; Tavily fallback is configured but not health-checked"}
				} else {
					checks["search"] = doctorCheck{Status: "warning", Message: "Configured SearXNG endpoint is unavailable"}
				}
			} else {
				message := "SearXNG search endpoint returned valid JSON"
				if cfg.TavilyAPIKey != "" {
					message += "; Tavily fallback is configured"
				}
				checks["search"] = doctorCheck{Status: "ok", Message: message}
			}
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
			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			probeClient := openai.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, 30*time.Second, 24, 0, 1, cfg.LLMEnableThinking)
			probeErr := llm.ProbeStructuredOutput(probeCtx, probeClient)
			cancel()
			if probeErr != nil {
				checks["llm"] = doctorCheck{Status: "error", Message: "local model failed the bounded strict JSON Schema smoke request"}
			} else {
				checks["llm"] = doctorCheck{Status: "ok", Message: "local model passed the bounded strict JSON Schema smoke request"}
			}
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
