package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/slager2/qwenchik/internal/orchestrator"
	"github.com/slager2/qwenchik/internal/policy"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/keepawake"
	"github.com/slager2/qwenchik/internal/runtime/notifications"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/runtime/resources"
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
	root.AddCommand(newRuntimeCommand())
	root.AddCommand(newDoctorCommand(&jsonMode))
	root.AddCommand(newSearchSmokeCommand(&jsonMode))
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
			report := doctor(cmd.Context(), cfg)
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, report)
			if report.Status == "error" {
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
	command := &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			switch name {
			case "providers":
				live, _ := cmd.Flags().GetBool("live")
				if live {
					request, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, "http://"+cfg.BindAddr+"/api/providers", nil)
					if err != nil {
						return errors.New("local provider diagnostics request could not be created")
					}
					client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
					response, err := client.Do(request)
					if err != nil {
						return errors.New("qagent serve is unavailable; cannot read observed provider health")
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusOK {
						return errors.New("local provider diagnostics returned a non-success response")
					}
					var payload map[string]any
					if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil {
						return errors.New("local provider diagnostics returned malformed JSON")
					}
					printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, payload)
					return nil
				}
				searchMode := "none"
				searchType := "none"
				searchStatus := "disabled"
				ddgStatus := "disabled"
				if cfg.SearchEnableDDG {
					searchMode, searchType, searchStatus, ddgStatus = "duckduckgo", "duckduckgo", "configured", "configured"
				}
				if cfg.SearXNGURL != "" || cfg.TavilyAPIKey != "" {
					searchMode = "composite"
					searchType = "composite"
					searchStatus = "configured"
				}
				pdfStatus := "pure_go_fallback"
				if cfg.PDFExtractorImage != "" {
					pdfStatus = "isolated_image_configured"
				}
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{
					"llm": map[string]any{"type": "local-openai-compatible", "base_url": cfg.LLMBaseURL, "model": cfg.LLMModel},
					"search": map[string]any{
						"status":   searchStatus,
						"type":     searchType,
						"mode":     searchMode,
						"base_url": cfg.SearXNGURL,
						"providers": map[string]any{
							"searxng":    map[string]any{"status": providerStatus(cfg.SearXNGURL), "base_url": cfg.SearXNGURL},
							"tavily":     map[string]string{"status": providerStatusFromBool(cfg.TavilyAPIKey != "")},
							"duckduckgo": map[string]any{"status": ddgStatus, "enabled": cfg.SearchEnableDDG, "fallback": true, "health": "not_checked", "endpoint": "https://html.duckduckgo.com/html/"},
						},
					},
					"sandbox":       map[string]any{"runtime": cfg.SandboxRuntime, "image": cfg.SandboxImage},
					"pdf_extractor": map[string]any{"status": pdfStatus, "image": cfg.PDFExtractorImage},
				})
			case "config":
				printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{
					"app_bind_addr": cfg.BindAddr, "database_path": cfg.DatabasePath, "artifacts_path": cfg.ArtifactsPath,
					"llm_base_url": cfg.LLMBaseURL, "llm_model": cfg.LLMModel, "searxng_url": cfg.SearXNGURL,
					"tavily_configured":      cfg.TavilyAPIKey != "",
					"search_enable_ddg":      cfg.SearchEnableDDG,
					"search_provider_policy": cfg.SearchProviderPolicy,
					"workspace_root":         cfg.WorkspaceRoot, "sandbox_runtime": cfg.SandboxRuntime,
					"sandbox_image": cfg.SandboxImage, "sandbox_network": cfg.SandboxNetwork,
					"pdf_extractor_image": cfg.PDFExtractorImage,
					"llm_context_length":  cfg.LLMContextLength, "llm_max_output_tokens": cfg.LLMMaxOutputTokens,
					"llm_max_concurrency": cfg.LLMMaxConcurrency, "allowed_domains": cfg.AllowedDomains,
					"research_default_profile":  cfg.ResearchDefaultProfile,
					"research_adaptive_enabled": cfg.ResearchAdaptiveEnabled,
					"research_profiles":         cfg.ResearchPolicies(),
				})
			}
			return nil
		},
	}
	if name == "providers" {
		command.Flags().Bool("live", false, "read last observed provider outcomes from the running local API")
	}
	return command
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
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("worker identity could not be generated")
	}
	if err := deps.store.EnableWorkerLeases("serve-"+hex.EncodeToString(nonce[:]), time.Duration(cfg.WorkerLeaseSeconds)*time.Second); err != nil {
		return err
	}
	resourceProvider, err := resources.NewHostProvider(cfg.LLMBaseURL, cfg.ResourcePolicy)
	if err != nil {
		return err
	}
	resourceMonitor, err := resources.NewMonitor(resourceProvider, cfg.ResourcePolicy)
	if err != nil {
		return err
	}
	configuredContext := cfg.LLMContextLength
	cfg.LLMContextLength = effectiveModelContext(parent, cfg)
	if cfg.LLMContextLength != configuredContext {
		logger.Warn("LLM context capped to server-reported runtime capacity", "configured_context", configuredContext, "effective_context", cfg.LLMContextLength)
	}
	if cfg.LLMContextLength-cfg.LLMMaxOutputTokens < 2304 {
		return errors.New("server-reported model context is too small for output and prompt reserves; lower LLM_MAX_OUTPUT_TOKENS")
	}
	model := configuredModelClient(cfg)
	registry := tools.NewRegistry(policy.NewWorkingSetEvaluator())
	searchProvider, err := configuredSearchProvider(cfg)
	if err != nil {
		return err
	}
	artifactStore, err := artifacts.New(cfg.ArtifactsPath, cfg.ArtifactMaxBytes)
	if err != nil {
		return err
	}
	sandboxRuntime, err := sandbox.New(cfg.SandboxImage, cfg.WorkspaceRoot, nil, sandbox.Limits{OutputBytes: 64 << 10, MaxFileCount: 100, MaxFileBytes: cfg.SandboxMaxFileBytes})
	if err != nil {
		return err
	}
	pdfExtractor, err := pdfextract.NewWithPolicy(cfg.PDFExtractorImage, nil, cfg.PDFPolicy)
	if err != nil {
		return err
	}
	fetcher, err := research.NewSafeFetcherWithFetchPolicy(cfg.FetchPolicy(), nil, cfg.AllowedDomains)
	if err != nil {
		return err
	}
	htmlExtractor, err := research.NewHTMLExtractorWithPDFPolicy(100_000, pdfExtractor, cfg.PDFPolicy)
	if err != nil {
		return err
	}
	if err := tools.RegisterResearchTools(registry, tools.ResearchTools{
		SearchProvider: searchProvider,
		Fetcher:        fetcher,
		Extractor:      htmlExtractor, Artifacts: artifactStore,
		FetchPolicy: cfg.FetchPolicy(), PDFPolicy: cfg.PDFPolicy,
		Repository: deps.store, MaxFetchBytes: cfg.MaxFetchBytes, AllowedDomains: cfg.AllowedDomains,
	}); err != nil {
		return err
	}
	workingSetTools := tools.NewWorkingSetTools(deps.store, artifactStore)
	if err := tools.RegisterWorkingSetTools(registry, &workingSetTools); err != nil {
		return err
	}
	if err := tools.RegisterSandboxTools(registry, tools.SandboxTools{Runtime: sandboxRuntime, Artifacts: artifactStore, Repository: deps.store}); err != nil {
		return err
	}
	runtimeResearchPolicy, err := cfg.ResearchProfile(cfg.ResearchDefaultProfile)
	if err != nil {
		return err
	}
	bundle := orchestrator.ResearchAgentBundle(cfg.LLMModel, registry)
	activeBundle, err := deps.store.EnsureBaselineBundle(parent, bundle)
	if err != nil {
		return err
	}
	deps.worker = orchestrator.New(deps.store, model, registry, orchestrator.Options{
		OnBundleChanged:   func(active domain.AgentBundle) { setResearchRunDefaultsForServe(deps.app, cfg, &active) },
		RuntimeBundleHash: bundle.BundleHash,
		ResourceMonitor:   resourceMonitor, ResourcePolicy: cfg.ResourcePolicy,
		KeepAwake: cfg.KeepAwake, SleepProvider: keepawake.NewProvider(),
		MaxSteps: cfg.MaxSteps, MaxSearchQueries: cfg.MaxSearchQueries, MaxFetchBytes: cfg.MaxFetchBytes,
		TaskTimeout:     time.Duration(cfg.TaskTimeoutSeconds) * time.Second,
		MaxOutputTokens: cfg.LLMMaxOutputTokens, MaxInputBytes: maxInputBytes(cfg),
		Temperature: cfg.LLMTemperature, TopP: cfg.LLMTopP, Logger: logger,
		RuntimeResearchPolicy: &runtimeResearchPolicy, RuntimeModelContext: cfg.LLMContextLength,
		UseWorkerLeases: true,
	})
	apiServer := api.New(deps.app, deps.worker, deps.store.Ping, cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.SearXNGURL, logger)
	apiServer.SetLLMModel(cfg.LLMModel)
	apiServer.SetLLMClient(model)
	apiServer.SetWorkerHeartbeat(deps.worker.Heartbeat)
	apiServer.SetRuntimeStatus(deps.worker.RuntimeStatus)
	apiServer.SetResearchPolicy(cfg.ResearchLimits(), pdfExtractor.Check)
	setResearchRunDefaultsForServe(deps.app, cfg, &activeBundle)
	apiServer.SetTavilyConfigured(cfg.TavilyAPIKey != "")
	apiServer.SetDDGEnabled(cfg.SearchEnableDDG)
	if diagnostic, ok := searchProvider.(interface {
		HealthSnapshot() []research.ProviderOutcome
	}); ok {
		apiServer.SetSearchDiagnostics(diagnostic.HealthSnapshot)
	}
	apiServer.SetArtifactReader(artifactStore)
	sandboxStatus := sandboxRuntime.Doctor(parent)
	apiServer.SetSandboxStatus(sandboxStatus.Available, sandboxStatus.Message)
	handler := apiServer.Handler()
	server := &http.Server{Addr: cfg.BindAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	notificationSender, err := notifications.NewSender(cfg.NotificationChannel, cfg.NtfyURL, cfg.NtfyToken)
	if err != nil {
		return err
	}
	notificationsDone := make(chan struct{})
	go func() {
		defer close(notificationsDone)
		notifications.Run(ctx, deps.store, notificationSender, cfg.NotificationChannel)
	}()
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
	select {
	case <-notificationsDone:
	case <-shutdownCtx.Done():
		return errors.New("notification worker did not stop before shutdown deadline")
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func setResearchRunDefaultsForServe(service *app.Service, cfg config.Config, bundles ...*domain.AgentBundle) {
	model := cfg.ResearchModelSnapshot()
	if len(bundles) > 0 {
		model.Bundle = bundles[0]
	}
	service.SetResearchRunDefaults(cfg.ResearchDefaultProfile, cfg.ResearchPolicies(), model)
	service.SetKeepAwakeDefault(cfg.KeepAwake)
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
	providers := make([]research.SearchProvider, 0, 3)
	var searxng, tavily, ddg research.SearchProvider
	if cfg.SearXNGURL != "" {
		provider, err := research.NewSearXNG(cfg.SearXNGURL, nil)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
		searxng = provider
	}
	if cfg.TavilyAPIKey != "" {
		provider, err := research.NewTavily(cfg.TavilyAPIKey, nil)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
		tavily = provider
	}
	if cfg.SearchEnableDDG {
		ddg = research.NewDuckDuckGo()
		providers = append(providers, ddg)
	}
	if len(providers) == 0 {
		return nil, nil
	}
	switch cfg.SearchProviderPolicy {
	case "", "local_primary":
		return research.NewLocalPrimarySearch(searxng, tavily, ddg), nil
	case "parallel":
		return research.NewCompositeSearch(providers...), nil
	default:
		return nil, errors.New("SEARCH_PROVIDER_POLICY must be local_primary or parallel")
	}
}

func maxInputBytes(cfg config.Config) int {
	const (
		reservedPromptTokens   = int64(2048)
		estimatedBytesPerToken = int64(4)
	)
	availableTokens := int64(cfg.LLMContextLength) - int64(cfg.LLMMaxOutputTokens) - reservedPromptTokens
	if availableTokens < 1 {
		return 1
	}
	maxInt := int64(^uint(0) >> 1)
	if availableTokens > maxInt/estimatedBytesPerToken {
		return int(maxInt)
	}
	return int(availableTokens * estimatedBytesPerToken)
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
	Status   string                     `json:"status"`
	Message  string                     `json:"message"`
	Outcomes []research.ProviderOutcome `json:"outcomes,omitempty"`
}

func doctor(ctx context.Context, cfg config.Config) doctorReport {
	if ctx.Err() != nil {
		return unavailableAppDoctor()
	}
	checks := map[string]doctorCheck{
		"go":   {Status: "ok", Message: "qagent binary is running on Go"},
		"bind": {Status: "ok", Message: "application bind address is loopback-only"},
	}
	appCtx, appCancel := context.WithTimeout(ctx, 2*time.Second)
	appResponding := false
	appRequest, appErr := http.NewRequestWithContext(appCtx, http.MethodGet, "http://"+cfg.BindAddr+"/api/health", nil)
	if appErr == nil {
		response, requestErr := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(appRequest)
		if requestErr == nil {
			appResponding = true
			var health struct {
				Worker string `json:"worker"`
			}
			_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&health)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				checks["app"] = doctorCheck{Status: "ok", Message: "qagent HTTP API is responding"}
				checks["worker"] = doctorCheck{Status: "warning", Message: "Worker state is not reported by the running server"}
				if health.Worker == "attached" {
					checks["worker"] = doctorCheck{Status: "ok", Message: "Server worker is attached"}
				}
				if health.Worker == "absent" {
					checks["worker"] = doctorCheck{Status: "error", Message: "Server has no worker; queued tasks cannot execute"}
				}
			} else {
				checks["app"] = doctorCheck{Status: "warning", Message: "qagent API returned a non-success health response"}
			}
		} else {
			checks["app"] = doctorCheck{Status: "warning", Message: "qagent serve is not responding; queued tasks need a running server worker"}
			checks["worker"] = doctorCheck{Status: "warning", Message: "Worker readiness is unknown because qagent serve is not responding"}
		}
	}
	appCancel()
	if appResponding {
		return runningAppDoctor(ctx, cfg)
	}
	if ctx.Err() != nil {
		return unavailableAppDoctor()
	}
	if api.FrontendAvailable() {
		checks["frontend"] = doctorCheck{Status: "ok", Message: "embedded frontend assets are available"}
	} else {
		checks["frontend"] = doctorCheck{Status: "error", Message: "embedded frontend assets are missing; run make build"}
	}
	sandboxRuntime, sandboxErr := sandbox.New(cfg.SandboxImage, cfg.WorkspaceRoot, nil, sandbox.Limits{OutputBytes: 64 << 10, MaxFileCount: 100, MaxFileBytes: cfg.SandboxMaxFileBytes})
	if sandboxErr != nil {
		checks["sandbox"] = doctorCheck{Status: "error", Message: "sandbox configuration is invalid"}
	} else if status := sandboxRuntime.Doctor(ctx); status.Available {
		checks["sandbox"] = doctorCheck{Status: "ok", Message: status.Message}
	} else {
		checks["sandbox"] = doctorCheck{Status: "warning", Message: status.Message}
	}
	pdfRuntime := map[string]any{"isolated_available": false, "isolated_status": "unavailable", "fallback_allowed": cfg.PDFPolicy.AllowUnsandboxedFallback}
	pdfExtractor, pdfErr := pdfextract.NewWithPolicy(cfg.PDFExtractorImage, nil, cfg.PDFPolicy)
	if pdfErr != nil {
		checks["pdf_extractor"] = doctorCheck{Status: "error", Message: "PDF extractor image reference is invalid"}
	} else {
		pdfCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := pdfExtractor.Check(pdfCtx)
		cancel()
		if err != nil {
			message := "isolated PDF extractor is unavailable; unsandboxed fallback is disabled by policy; PDF sources will be skipped"
			if cfg.PDFPolicy.AllowUnsandboxedFallback {
				message = "isolated PDF extractor is unavailable; explicitly allowed pure-Go fallback has only a soft memory target, not OS isolation"
			}
			checks["pdf_extractor"] = doctorCheck{Status: "warning", Message: message}
		} else {
			pdfRuntime["isolated_available"], pdfRuntime["isolated_status"] = true, "available"
			checks["pdf_extractor"] = doctorCheck{Status: "ok", Message: "rootless Docker and local PDF extractor image are available"}
		}
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
	checks["search"] = doctorCheck{Status: "warning", Message: "External search health is not checked; Tavily checks are opt-in and may be billable"}
	if cfg.SearXNGURL == "" && cfg.TavilyAPIKey == "" && !cfg.SearchEnableDDG {
		checks["search"] = doctorCheck{Status: "warning", Message: "Web search is disabled; configure SearXNG/Tavily or explicitly enable SEARCH_ENABLE_DDG"}
	} else if cfg.SearXNGURL != "" {
		provider, providerErr := research.NewSearXNG(cfg.SearXNGURL, nil)
		if providerErr != nil {
			checks["search"] = doctorCheck{Status: "warning", Message: "SearXNG configuration is invalid"}
		} else {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			report, probeErr := provider.SearchWithDiagnostics(probeCtx, research.SearchOptions{Query: "qagent-doctor", MaxResults: 1, SafeSearch: true})
			cancel()
			check := doctorCheck{Status: "warning", Message: "SearXNG is degraded or unavailable; inspect provider outcomes", Outcomes: report.Outcomes}
			if probeErr == nil && len(report.Hits) > 0 {
				check.Status, check.Message = "ok", "SearXNG returned usable search results"
			}
			checks["search"] = check
		}
	}
	checks["llm"] = doctorModel(ctx, cfg, appResponding)
	limits := safeDoctorLimits(cfg.ResearchLimits())
	limits["configured"] = true
	return makeDoctorReport(checks, limits, pdfRuntime)
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
