package api

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
)

const maxRequestBytes = 1 << 20

//go:embed web/dist
var frontendAssets embed.FS

func FrontendAvailable() bool {
	_, err := frontendAssets.Open("web/dist/index.html")
	return err == nil
}

type TaskService interface {
	CreateTask(context.Context, string, string, string) (domain.Task, error)
	GetTask(context.Context, string) (domain.Task, error)
	ListTasks(context.Context) ([]domain.Task, error)
	QueueTask(context.Context, string, string) (domain.Task, error)
	AddUserMessage(context.Context, string, string, string) (domain.Message, error)
	Messages(context.Context, string) ([]domain.Message, error)
	PauseTask(context.Context, string) (domain.Task, error)
	ResumeTask(context.Context, string, string) (domain.Task, error)
	CancelTask(context.Context, string) (domain.Task, error)
	Events(context.Context, string, int64, int) ([]domain.Event, error)
	Checkpoints(context.Context, string) ([]domain.Checkpoint, error)
	Steps(context.Context, string) ([]domain.TaskStep, error)
	Artifacts(context.Context, string) ([]domain.Artifact, error)
	Evidence(context.Context, string) ([]domain.Evidence, error)
	Approval(context.Context, string) (domain.Approval, error)
	ApprovalsForTask(context.Context, string) ([]domain.Approval, error)
	PendingApprovals(context.Context) ([]domain.Approval, error)
	DecideApproval(context.Context, string, bool, string) (domain.Approval, error)
	CreateMemory(context.Context, domain.Memory) (domain.Memory, error)
	Memory(context.Context, string) (domain.Memory, error)
	Memories(context.Context, domain.MemoryStatus, string) ([]domain.Memory, error)
	UpdateMemory(context.Context, string, string, string, map[string]any) (domain.Memory, error)
	SetMemoryStatus(context.Context, string, domain.MemoryStatus) (domain.Memory, error)
	SearchMemories(context.Context, string, string, int) ([]domain.MemoryMatch, error)
}

type WorkerControl interface {
	Wake()
	Interrupt(string)
}

type ArtifactReader interface {
	Read(context.Context, string, string) ([]byte, error)
}

type Server struct {
	service           TaskService
	worker            WorkerControl
	dbPing            func(context.Context) error
	llmURL            string
	llmKey            string
	llmModel          string
	searchURL         string
	tavilyConfigured  bool
	ddgDisabled       bool
	searchDiagnostics func() []research.ProviderOutcome
	artifactReader    ArtifactReader
	sandboxAvailable  bool
	sandboxMessage    string
	logger            *slog.Logger
	client            *http.Client
	llmProbeMu        sync.Mutex
	llmProbeAt        time.Time
	llmProbeError     error
	llmClient         llm.Client
	healthMu          sync.RWMutex
	workerHeartbeat   func(context.Context) (time.Time, error)
	runtimeStatus     func() map[string]any
	telemetryMu       sync.RWMutex
	runTelemetry      func(runID string) (domain.Telemetry, uint64, bool)
	researchPolicy    map[string]any
	pdfCheck          func(context.Context) error
}

func New(service TaskService, worker WorkerControl, dbPing func(context.Context) error, llmURL, llmKey, searchURL string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{service: service, worker: worker, dbPing: dbPing, llmURL: strings.TrimRight(llmURL, "/"), llmKey: llmKey, llmModel: "local-qwen", searchURL: strings.TrimRight(searchURL, "/"), logger: logger, client: &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.conversationRoutes(mux)
	assets, err := fs.Sub(frontendAssets, "web/dist")
	if err == nil {
		fileServer := http.FileServer(http.FS(assets))
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			fileServer.ServeHTTP(w, r)
		}))
	}
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/doctor", s.doctor)
	mux.HandleFunc("GET /api/providers", s.providers)
	mux.HandleFunc("GET /api/approvals", s.pendingApprovals)
	mux.HandleFunc("GET /api/approvals/{approvalID}", s.getApproval)
	mux.HandleFunc("POST /api/approvals/{approvalID}/approve", s.approve)
	mux.HandleFunc("POST /api/approvals/{approvalID}/reject", s.reject)
	mux.HandleFunc("GET /api/memories", s.memories)
	mux.HandleFunc("POST /api/memories", s.createMemory)
	mux.HandleFunc("GET /api/memories/search", s.searchMemories)
	mux.HandleFunc("GET /api/memories/{memoryID}", s.getMemory)
	mux.HandleFunc("PUT /api/memories/{memoryID}", s.updateMemory)
	mux.HandleFunc("POST /api/memories/{memoryID}/accept", s.acceptMemory)
	mux.HandleFunc("POST /api/memories/{memoryID}/reject", s.rejectMemory)
	mux.HandleFunc("POST /api/memories/{memoryID}/stale", s.staleMemory)
	mux.HandleFunc("POST /api/memories/{memoryID}/supersede", s.supersedeMemory)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("GET /api/tasks/{taskID}", s.getTask)
	mux.HandleFunc("POST /api/tasks/{taskID}/messages", s.addMessage)
	mux.HandleFunc("GET /api/tasks/{taskID}/messages", s.messages)
	mux.HandleFunc("POST /api/tasks/{taskID}/run", s.runTask)
	mux.HandleFunc("POST /api/tasks/{taskID}/pause", s.pauseTask)
	mux.HandleFunc("POST /api/tasks/{taskID}/resume", s.resumeTask)
	mux.HandleFunc("POST /api/tasks/{taskID}/cancel", s.cancelTask)
	mux.HandleFunc("GET /api/tasks/{taskID}/events", s.events)
	mux.HandleFunc("GET /api/tasks/{taskID}/events/stream", s.streamEvents)
	mux.HandleFunc("GET /api/tasks/{taskID}/checkpoints", s.checkpoints)
	mux.HandleFunc("GET /api/tasks/{taskID}/steps", s.steps)
	mux.HandleFunc("GET /api/tasks/{taskID}/artifacts", s.artifacts)
	mux.HandleFunc("GET /api/tasks/{taskID}/artifacts/{artifactID}", s.artifactContent)
	mux.HandleFunc("GET /api/tasks/{taskID}/evidence", s.evidence)
	mux.HandleFunc("GET /api/tasks/{taskID}/approvals", s.taskApprovals)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("HTTP handler panic", "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, "internal_error", "Внутренняя ошибка сервера.", false)
			}
		}()
		if !loopbackHostHeader(r.Host) {
			writeError(w, http.StatusForbidden, "invalid_host", "Запросы принимаются только с loopback Host.", false)
			return
		}
		if !safeMutationOrigin(r) {
			writeError(w, http.StatusForbidden, "invalid_origin", "Изменения разрешены только из локального интерфейса.", false)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) SetArtifactReader(reader ArtifactReader) { s.artifactReader = reader }

func (s *Server) SetLLMModel(model string) {
	if strings.TrimSpace(model) != "" {
		s.llmModel = strings.TrimSpace(model)
	}
}

func (s *Server) SetLLMClient(client llm.Client) {
	s.llmProbeMu.Lock()
	defer s.llmProbeMu.Unlock()
	s.llmClient = client
	s.llmProbeAt = time.Time{}
	s.llmProbeError = nil
}

func (s *Server) SetTavilyConfigured(configured bool) {
	s.tavilyConfigured = configured
}

func (s *Server) SetSearchDiagnostics(snapshot func() []research.ProviderOutcome) {
	s.searchDiagnostics = snapshot
}

func (s *Server) SetDDGEnabled(enabled bool) {
	s.ddgDisabled = !enabled
}

func (s *Server) SetSandboxStatus(available bool, message string) {
	s.sandboxAvailable = available
	s.sandboxMessage = message
}

func loopbackHostHeader(value string) bool {
	host := value
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное тело запроса.", false)
		return
	}
	task, err := s.service.CreateTask(r.Context(), request.Title, request.Description, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/tasks/"+task.ID)
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.service.ListTasks(r.Context())
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if tasks == nil {
		tasks = []domain.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.service.GetTask(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.service.QueueTask(r.Context(), r.PathValue("taskID"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	s.worker.Wake()
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) addMessage(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	task, err := s.service.GetTask(r.Context(), taskID)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if task.Status != domain.StatusInbox && task.Status != domain.StatusQueued && task.Status != domain.StatusPaused && task.Status != domain.StatusFailed {
		writeError(w, http.StatusConflict, "task_not_accepting_messages", "Нельзя добавить сообщение во время выполнения или после завершения задачи.", false)
		return
	}
	var request struct {
		Content string `json:"content"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное тело запроса.", false)
		return
	}
	message, err := s.service.AddUserMessage(r.Context(), taskID, request.Content, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if task.Status == domain.StatusInbox || task.Status == domain.StatusPaused || task.Status == domain.StatusFailed {
		if _, err := s.service.QueueTask(r.Context(), taskID, ""); err != nil {
			s.writeServiceError(w, err)
			return
		}
	}
	s.worker.Wake()
	writeJSON(w, http.StatusAccepted, message)
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Messages(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Message{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) pauseTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	task, err := s.service.PauseTask(r.Context(), taskID)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	s.worker.Interrupt(taskID)
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) resumeTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	task, err := s.service.ResumeTask(r.Context(), taskID, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	s.worker.Wake()
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	task, err := s.service.CancelTask(r.Context(), taskID)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	s.worker.Interrupt(taskID)
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	after, err := parseAfter(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Параметр after должен быть неотрицательным целым числом.", false)
		return
	}
	result := make([]domain.Event, 0)
	for page := 0; page < 20; page++ {
		events, err := s.service.Events(r.Context(), r.PathValue("taskID"), after, 500)
		if err != nil {
			s.writeServiceError(w, err)
			return
		}
		for _, event := range events {
			if event.Seq > after {
				after = event.Seq
			}
			if publicEvent(event) {
				projected, err := s.projectPublicEvent(r.Context(), event)
				if err != nil {
					s.writeServiceError(w, err)
					return
				}
				result = append(result, projected)
				if len(result) >= 500 {
					writeJSON(w, http.StatusOK, result)
					return
				}
			}
		}
		if len(events) < 500 {
			writeJSON(w, http.StatusOK, result)
			return
		}
	}
	if len(result) == 0 {
		writeError(w, 409, "event_scan_limit", "История требует повторной синхронизации.", true)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if _, err := s.service.GetTask(r.Context(), taskID); err != nil {
		s.writeServiceError(w, err)
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	after, err := parseAfter(cursor)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Заголовок Last-Event-ID должен быть неотрицательным целым числом.", false)
		return
	}
	var snapshot map[string]any
	resync := false
	var lastTelemetry domain.Telemetry
	var lastRevision uint64
	haveTelemetry := false
	var lastWarnings []string
	if service, ok := s.service.(runRuntimeService); ok {
		snapshot, err = service.RunRuntime(r.Context(), taskID)
		if err != nil {
			s.writeServiceError(w, err)
			return
		}
		if after > 0 {
			probe, probeErr := s.service.Events(r.Context(), taskID, after-1, 1)
			if probeErr != nil {
				s.writeServiceError(w, probeErr)
				return
			}
			resync = len(probe) == 0 || probe[0].Seq != after
		}
		snapshot["runtime"] = s.runtimeHealth()
		lastWarnings = safeRuntimeWarningSet(snapshot["runtime"].(map[string]any))
		if telemetry, revision, available := s.addRunTelemetry(snapshot, taskID); available {
			lastTelemetry, lastRevision, haveTelemetry = telemetry, revision, true
		}
	} else {
		lastWarnings = safeRuntimeWarningSet(s.runtimeHealth())
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "SSE не поддерживается этим HTTP writer.", false)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "retry: 1000\n\n")
	if snapshot != nil {
		encoded, _ := json.Marshal(snapshot)
		if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", encoded); err != nil {
			return
		}
		if resync {
			if _, err := fmt.Fprintf(w, "id: %v\nevent: resync\ndata: {\"reason\":\"cursor_gap\",\"cursor\":%v}\n\n", snapshot["cursor"], snapshot["cursor"]); err != nil {
				return
			}
			if cursor, ok := snapshot["cursor"].(int64); ok {
				after = cursor
			}
		}
	}
	flusher.Flush()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		events, err := s.service.Events(r.Context(), taskID, after, 200)
		if err != nil {
			s.logger.Warn("SSE event poll failed", "task_id", taskID, "error", err)
			return
		}
		for _, event := range events {
			if after > 0 && event.Seq > after+1 {
				if service, ok := s.service.(runRuntimeService); ok {
					current, err := service.RunRuntime(r.Context(), taskID)
					if err != nil {
						return
					}
					runtime := s.runtimeHealth()
					current["runtime"] = runtime
					lastWarnings = safeRuntimeWarningSet(runtime)
					if telemetry, revision, available := s.addRunTelemetry(current, taskID); available {
						lastTelemetry, lastRevision, haveTelemetry = telemetry, revision, true
					}
					encoded, _ := json.Marshal(current)
					if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\nid: %v\nevent: resync\ndata: {\"reason\":\"cursor_gap\",\"cursor\":%v}\n\n", encoded, current["cursor"], current["cursor"]); err != nil {
						return
					}
					if cursor, ok := current["cursor"].(int64); ok {
						after = cursor
					}
					flusher.Flush()
					break
				}
			}
			if event.Seq > after {
				after = event.Seq
			}
			if !publicEvent(event) {
				continue
			}
			projected, err := s.projectPublicEvent(r.Context(), event)
			if err != nil {
				s.logger.Warn("SSE event projection failed", "task_id", taskID)
				return
			}
			encoded, err := json.Marshal(projected)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, encoded); err != nil {
				return
			}
			if alias := lifecycleEventAlias(event); alias != "" {
				aliasData, _ := json.Marshal(map[string]string{"status": lifecycleAliasStatus(alias)})
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, alias, aliasData); err != nil {
					return
				}
			}
			flusher.Flush()
		}
		if telemetry, revision, available := s.runTelemetrySnapshot(taskID); available {
			if !haveTelemetry || !telemetrySystemSampleTimestamp(telemetry).Equal(telemetrySystemSampleTimestamp(lastTelemetry)) {
				if err := writeSSEData(w, "runtime_snapshot", map[string]any{"telemetry": telemetry, "telemetry_revision": revision}); err != nil {
					return
				}
				flusher.Flush()
			}
			if !haveTelemetry || revision != lastRevision && !reflect.DeepEqual(telemetry.Inference, lastTelemetry.Inference) {
				if err := writeSSEData(w, "inference_progress", map[string]any{"telemetry_revision": revision, "inference": telemetry.Inference}); err != nil {
					return
				}
				flusher.Flush()
			}
			lastTelemetry, lastRevision, haveTelemetry = telemetry, revision, true
		}
		runtime := s.runtimeHealth()
		warnings := safeRuntimeWarningSet(runtime)
		if !sameStringSet(lastWarnings, warnings) {
			if err := writeSSEData(w, "resource_warning", map[string]any{"warnings": warnings}); err != nil {
				return
			}
			flusher.Flush()
			lastWarnings = warnings
		}
		if len(events) == 0 {
			task, err := s.service.GetTask(r.Context(), taskID)
			if err != nil || task.Status.Terminal() {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) checkpoints(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Checkpoints(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Checkpoint{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) steps(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Steps(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.TaskStep{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Artifacts(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Artifact{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request) {
	taskID, artifactID := r.PathValue("taskID"), r.PathValue("artifactID")
	if s.artifactReader == nil {
		writeError(w, http.StatusServiceUnavailable, "artifact_store_unavailable", "Локальное хранилище артефактов недоступно.", true)
		return
	}
	items, err := s.service.Artifacts(r.Context(), taskID)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	var artifact *domain.Artifact
	for i := range items {
		if items[i].ID == artifactID {
			artifact = &items[i]
			break
		}
	}
	if artifact == nil {
		writeError(w, http.StatusNotFound, "not_found", "Артефакт не найден.", false)
		return
	}
	content, err := s.artifactReader.Read(r.Context(), taskID, artifactID)
	if err != nil {
		s.logger.Warn("artifact read failed", "task_id", taskID, "artifact_id", artifactID, "error", err)
		writeError(w, http.StatusNotFound, "not_found", "Артефакт не найден в локальном хранилище.", false)
		return
	}
	hash := sha256.Sum256(content)
	if hex.EncodeToString(hash[:]) != artifact.SHA256 {
		s.logger.Error("artifact content hash mismatch", "task_id", taskID, "artifact_id", artifactID)
		writeError(w, http.StatusInternalServerError, "artifact_integrity_error", "Хэш артефакта не совпадает с метаданными.", false)
		return
	}
	w.Header().Set("Content-Type", artifact.MIMEType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) evidence(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Evidence(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Evidence{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) pendingApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.PendingApprovals(r.Context())
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Approval{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	approval, err := s.service.Approval(r.Context(), r.PathValue("approvalID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) taskApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ApprovalsForTask(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Approval{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	s.decideApproval(w, r, true)
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request) {
	s.decideApproval(w, r, false)
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request, approve bool) {
	approval, err := s.service.DecideApproval(r.Context(), r.PathValue("approvalID"), approve, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if approve {
		s.worker.Wake()
	}
	writeJSON(w, http.StatusAccepted, approval)
}

func (s *Server) memories(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.Memories(r.Context(), domain.MemoryStatus(r.URL.Query().Get("status")), r.URL.Query().Get("scope"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []domain.Memory{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Kind             string         `json:"kind"`
		Text             string         `json:"text"`
		Metadata         map[string]any `json:"metadata"`
		Scope            string         `json:"scope"`
		Confidence       *float64       `json:"confidence"`
		SourceEvidenceID string         `json:"source_evidence_id"`
		SourceTaskID     string         `json:"source_task_id"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное тело запроса.", false)
		return
	}
	memory, err := s.service.CreateMemory(r.Context(), domain.Memory{
		Kind: request.Kind, Text: request.Text, Metadata: request.Metadata, Scope: request.Scope,
		Confidence: request.Confidence, SourceEvidenceID: request.SourceEvidenceID, SourceTaskID: request.SourceTaskID,
	})
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/memories/"+memory.ID)
	writeJSON(w, http.StatusCreated, memory)
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	memory, err := s.service.Memory(r.Context(), r.PathValue("memoryID"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Kind     string         `json:"kind"`
		Text     string         `json:"text"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректное тело запроса.", false)
		return
	}
	memory, err := s.service.UpdateMemory(r.Context(), r.PathValue("memoryID"), request.Kind, request.Text, request.Metadata)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (s *Server) searchMemories(w http.ResponseWriter, r *http.Request) {
	limit := 5
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 20 {
			writeError(w, http.StatusBadRequest, "invalid_request", "Параметр limit должен быть от 1 до 20.", false)
			return
		}
		limit = parsed
	}
	matches, err := s.service.SearchMemories(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("task_id"), limit)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if matches == nil {
		matches = []domain.MemoryMatch{}
	}
	writeJSON(w, http.StatusOK, matches)
}

func (s *Server) acceptMemory(w http.ResponseWriter, r *http.Request) {
	s.decideMemory(w, r, domain.MemoryAccepted)
}

func (s *Server) rejectMemory(w http.ResponseWriter, r *http.Request) {
	s.decideMemory(w, r, domain.MemoryRejected)
}

func (s *Server) staleMemory(w http.ResponseWriter, r *http.Request) {
	s.decideMemory(w, r, domain.MemoryStale)
}

func (s *Server) supersedeMemory(w http.ResponseWriter, r *http.Request) {
	s.decideMemory(w, r, domain.MemorySuperseded)
}

func (s *Server) decideMemory(w http.ResponseWriter, r *http.Request, status domain.MemoryStatus) {
	memory, err := s.service.SetMemoryStatus(r.Context(), r.PathValue("memoryID"), status)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if s.dbPing == nil || s.dbPing(r.Context()) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "database": "unavailable"})
		return
	}
	workerState := "attached"
	if s.worker == nil {
		workerState = "absent"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "database": "ok", "worker": workerState, "worker_details": s.workerHealth(r.Context()), "runtime": s.runtimeHealth(), "api_version": 1, "model": map[string]string{"id": s.llmModel, "status": "configured"}})
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	checks := map[string]map[string]string{}
	researchLimits, pdfRuntime, pdfReadiness := s.researchPolicyStatus(r.Context())
	checks["pdf_extractor"] = pdfReadiness
	checks["app"] = map[string]string{"status": "ok", "message": "API application is ready"}
	if s.service == nil {
		checks["app"] = map[string]string{"status": "error", "message": "Task service is absent"}
	}
	checks["worker"] = map[string]string{"status": "ok", "message": "Worker is attached"}
	workerBusy := false
	if worker, ok := s.worker.(interface{ Busy() bool }); ok {
		workerBusy = worker.Busy()
		if workerBusy {
			checks["worker"]["message"] = "Worker is executing a task"
		}
	}
	if s.worker == nil {
		checks["worker"] = map[string]string{"status": "error", "message": "Worker is absent; tasks cannot execute"}
	}
	checks["artifacts"] = map[string]string{"status": "ok", "message": "Artifact reader is attached"}
	if s.artifactReader == nil {
		checks["artifacts"] = map[string]string{"status": "warning", "message": "Artifact reader is absent"}
	}
	if FrontendAvailable() {
		checks["frontend"] = map[string]string{"status": "ok", "message": "embedded frontend assets are available"}
	} else {
		checks["frontend"] = map[string]string{"status": "error", "message": "embedded frontend assets are missing"}
	}
	if s.dbPing == nil || s.dbPing(r.Context()) != nil {
		checks["database"] = map[string]string{"status": "error", "message": "SQLite connection failed"}
	} else {
		checks["database"] = map[string]string{"status": "ok", "message": "SQLite connection is available"}
	}
	providerHealth := s.searchProviderStatus()
	checks["search"] = map[string]string{"status": "warning", "message": "Search configuration is not a health check; Tavily and DuckDuckGo are not probed"}
	for name, item := range providerHealth {
		state, _ := item["health"].(string)
		checks["search_"+name] = map[string]string{"status": "warning", "message": "Last observed state: " + state}
		if state == "ok" || state == "healthy" {
			checks["search_"+name]["status"] = "ok"
		}
	}
	if endpoint, err := url.Parse(s.searchURL); err == nil && endpoint != nil && loopbackHostHeader(endpoint.Host) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		provider, err := research.NewSearXNG(s.searchURL, s.client)
		var report research.SearchReport
		if err == nil {
			report, err = provider.SearchWithDiagnostics(ctx, research.SearchOptions{Query: "qagent-doctor", MaxResults: 1, SafeSearch: true})
		}
		checks["search_probe"] = map[string]string{"status": "warning", "message": "Local SearXNG probe is unavailable"}
		if err == nil && len(report.Hits) > 0 {
			checks["search_probe"] = map[string]string{"status": "ok", "message": "Local SearXNG returned usable search hits; this probe does not replace last task outcomes"}
		} else if err == nil {
			checks["search_probe"] = map[string]string{"status": "warning", "message": "Local SearXNG returned a valid zero-result response"}
		}
		if len(report.Outcomes) > 0 {
			encoded, _ := json.Marshal(report.Outcomes[0])
			var outcome map[string]any
			if json.Unmarshal(encoded, &outcome) == nil {
				providerHealth["searxng"]["probe_outcome"] = safeDiagnostic(outcome)
			}
		}
	}
	if s.sandboxAvailable {
		checks["sandbox"] = map[string]string{"status": "ok", "message": s.sandboxMessage}
	} else {
		message := s.sandboxMessage
		if message == "" {
			message = "rootless sandbox is unavailable; host execution is disabled"
		}
		checks["sandbox"] = map[string]string{"status": "warning", "message": message}
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.llmURL+"/models", nil)
	if err == nil && s.llmKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.llmKey)
	}
	if err != nil {
		checks["llm"] = map[string]string{"status": "error", "message": "LLM health request could not be created"}
	} else if response, requestErr := s.client.Do(request); requestErr != nil {
		checks["llm"] = map[string]string{"status": "error", "message": "Local LLM endpoint is unavailable"}
	} else {
		_ = response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if workerBusy {
				checks["llm"] = map[string]string{"status": "ok", "message": "Local model endpoint is available; inference smoke is deferred while the single worker is busy"}
			} else if err := s.probeStructuredLLM(r.Context()); err != nil {
				if errors.Is(err, llm.ErrInferenceBusy) {
					checks["llm"] = map[string]string{"status": "ok", "message": "Local model endpoint is available; inference smoke is deferred while the inference slot is busy"}
				} else {
					checks["llm"] = map[string]string{"status": "error", "message": "local model failed the bounded structured JSON smoke request"}
				}
			} else {
				checks["llm"] = map[string]string{"status": "ok", "message": "local model passed the bounded structured JSON smoke request"}
			}
		} else {
			checks["llm"] = map[string]string{"status": "error", "message": fmt.Sprintf("LLM endpoint returned HTTP %d", response.StatusCode)}
		}
	}
	status := "ok"
	for _, check := range checks {
		switch check["status"] {
		case "error":
			status = "error"
			break
		case "warning":
			if status == "ok" {
				status = "warning"
			}
		}
		if status == "error" {
			break
		}
	}
	code := http.StatusOK
	if status == "error" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": checks, "providers": providerHealth, "research_limits": researchLimits, "pdf_extractor": pdfRuntime, "runtime": s.runtimeHealth()})
}

func (s *Server) probeStructuredLLM(ctx context.Context) error {
	s.llmProbeMu.Lock()
	defer s.llmProbeMu.Unlock()
	if !s.llmProbeAt.IsZero() && time.Since(s.llmProbeAt) < time.Minute {
		return s.llmProbeError
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s.llmClient == nil {
		return llm.ErrInferenceBusy
	}
	err := llm.ProbeStructuredOutput(probeCtx, s.llmClient)
	if errors.Is(err, llm.ErrInferenceBusy) || ctx.Err() != nil {
		return err
	}
	s.llmProbeError = err
	s.llmProbeAt = time.Now()
	return s.llmProbeError
}

func (s *Server) providers(w http.ResponseWriter, _ *http.Request) {
	searchStatus := "enabled"
	searchMode := "duckduckgo"
	searchType := "duckduckgo"
	if s.searchURL != "" || s.tavilyConfigured {
		searchStatus = "configured"
		searchMode = "composite"
		searchType = "composite"
	}
	if s.searchURL == "" && !s.tavilyConfigured && s.ddgDisabled {
		searchStatus, searchMode, searchType = "disabled", "disabled", "disabled"
	}
	sandboxStatus := "unavailable"
	if s.sandboxAvailable {
		sandboxStatus = "available"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"llm": map[string]any{"type": "local-openai-compatible", "status": "configured", "base_url": safeEndpoint(s.llmURL)},
		"search": map[string]any{
			"type": searchType, "mode": searchMode, "status": searchStatus, "base_url": safeEndpoint(s.searchURL),
			"providers": s.searchProviderStatus(),
		},
		"browser": map[string]string{"status": "disabled"},
		"sandbox": map[string]string{"status": sandboxStatus, "message": s.sandboxMessage},
	})
}

func providerState(configured bool) string {
	if configured {
		return "configured"
	}
	return "disabled"
}

func safeEndpoint(raw string) string {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return ""
	}
	return endpoint.Scheme + "://" + endpoint.Host
}

func safeDiagnostic(input map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"provider", "state", "code", "error_code", "tool", "step_id"} {
		if value, ok := input[key].(string); ok && len(value) <= 80 && !strings.ContainsAny(value, ":/?@&= \\·\n\r\t") {
			result[key] = value
		}
	}
	for _, key := range []string{"retryable", "http_status", "latency_ms", "result_count", "distinct_domains", "body_size"} {
		switch value := input[key].(type) {
		case bool, float64:
			result[key] = value
		}
	}
	if value, ok := input["content_type"].(string); ok {
		switch value {
		case "text/html", "application/xhtml+xml", "application/json", "text/plain":
			result["content_type"] = value
		}
	}
	if value, ok := input["parser_version"].(string); ok && len(value) < 64 && !strings.ContainsAny(value, ":/?@&= \n\r\t") {
		result["parser_version"] = value
	}
	if value, ok := input["checked_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
			result["checked_at"] = value
		}
	}
	for _, key := range []string{"started_at", "finished_at"} {
		if value, ok := input[key].(string); ok {
			if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
				result[key] = value
			}
		}
	}
	if value, ok := input["query_hash"].(string); ok && len(value) == 64 {
		if _, err := hex.DecodeString(value); err == nil {
			result["query_hash"] = value
		}
	}
	state, _ := result["state"].(string)
	message := "Provider attempt recorded"
	switch state {
	case "ok", "healthy":
		message = "Provider returned results"
	case "blocked":
		message = "Provider blocked the request"
	case "no_results":
		message = "Provider returned no results"
	case "filtered":
		message = "Provider results were filtered"
	case "unavailable":
		message = "Provider is unavailable"
	}
	result["message"] = message
	if result["code"] == "search_no_results" {
		result["message"] = "Provider returned no results"
	}
	if result["code"] == "search_filtered_by_policy" {
		result["message"] = "Provider results were filtered"
	}
	return result
}

func (s *Server) searchProviderStatus() map[string]map[string]any {
	result := map[string]map[string]any{}
	for name, enabled := range map[string]bool{"searxng": s.searchURL != "", "tavily": s.tavilyConfigured, "duckduckgo": !s.ddgDisabled} {
		result[name] = map[string]any{"configured": enabled, "enabled": enabled, "status": providerState(enabled), "health": "not_checked", "last_checked": nil, "last_outcome": nil}
	}
	result["searxng"]["endpoint"] = safeEndpoint(s.searchURL)
	result["searxng"]["base_url"] = safeEndpoint(s.searchURL)
	result["tavily"]["endpoint"] = "https://api.tavily.com"
	result["duckduckgo"]["endpoint"] = "https://html.duckduckgo.com/html/"
	result["duckduckgo"]["fallback"] = true
	if !s.ddgDisabled {
		result["duckduckgo"]["status"] = "enabled"
	}
	if s.searchDiagnostics != nil {
		for _, outcome := range s.searchDiagnostics() {
			if outcome.CheckedAt.IsZero() || outcome.Code == "provider_not_checked" {
				continue
			}
			encoded, err := json.Marshal(outcome)
			if err != nil {
				continue
			}
			var item map[string]any
			if json.Unmarshal(encoded, &item) != nil {
				continue
			}
			name, _ := item["provider"].(string)
			if entry, ok := result[name]; ok {
				diagnostic := safeDiagnostic(item)
				entry["last_outcome"] = diagnostic
				entry["health"] = diagnostic["state"]
				entry["last_checked"] = diagnostic["checked_at"]
				entry["latency_ms"] = diagnostic["latency_ms"]
				entry["retryable"] = diagnostic["retryable"]
			}
		}
	}
	return result
}

func safePublicEvent(event domain.Event) domain.Event {
	if event.Type == "context.compacted" || event.Type == "continuation.requested" || event.Type == "verification.completed" || event.Type == "refinement.evaluated" || event.Type == "refinement.promoted" || event.Type == "refinement.rollback" {
		event.Payload, _ = json.Marshal(safeHarnessEvent(event.Payload))
		return event
	}
	if resourceEventType(event.Type) {
		event.Payload, _ = json.Marshal(safeResourceEvent(event.Payload))
		return event
	}
	if event.Type == "research.warning" {
		event.Payload, _ = json.Marshal(safeResearchWarning(event.Payload))
		return event
	}
	if payload, ok := sourceLifecycleEvent(event); ok {
		event.Payload, _ = json.Marshal(payload)
		return event
	}
	if event.Type == "search.hit" {
		var payload struct {
			StepID string                        `json:"step_id"`
			Hit    research.SearchHitObservation `json:"hit"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		result := map[string]any{"hit": tools.SafeSearchHitObservation(payload.Hit)}
		if safeStepID(payload.StepID) {
			result["step_id"] = payload.StepID
		}
		event.Payload, _ = json.Marshal(result)
		return event
	}
	if event.Type == "query.updated" {
		var payload struct {
			StepID      string `json:"step_id"`
			Query       string `json:"query"`
			Fingerprint string `json:"query_fingerprint"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		fingerprint := payload.Fingerprint
		if !validFingerprint(fingerprint) {
			sum := sha256.Sum256([]byte(payload.Query))
			fingerprint = hex.EncodeToString(sum[:])
		}
		result := map[string]any{"query_fingerprint": fingerprint}
		if query := tools.SafeSearchQuery(payload.Query); query != "" {
			result["query"] = query
		}
		if safeStepID(payload.StepID) {
			result["step_id"] = payload.StepID
		}
		event.Payload, _ = json.Marshal(result)
		return event
	}
	if event.Type == "verification.updated" {
		event.Payload, _ = json.Marshal(sqlite.SafeVerificationMetadata(event.Payload, nil))
		return event
	}
	if event.Type != "search.provider" && event.Type != "tool.failed" {
		return event
	}
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	result := safeDiagnostic(payload)
	if event.Type == "tool.failed" {
		result["message"] = "Tool execution failed"
	}
	for _, key := range []string{"outcomes", "providers"} {
		if items, ok := payload[key].([]any); ok {
			outcomes := []any{}
			for _, item := range items {
				if diagnostic, ok := item.(map[string]any); ok {
					outcomes = append(outcomes, safeDiagnostic(diagnostic))
				}
			}
			result[key] = outcomes
		}
	}
	event.Payload, _ = json.Marshal(result)
	return event
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sqlite.ErrWebDisabled):
		writeError(w, http.StatusConflict, "web_disabled", "Веб-поиск отключён для этого разговора. Выберите Chat или измените настройки.", false)
	case errors.Is(err, domain.ErrInvalidConversation):
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректные параметры разговора или сообщения.", false)
	case errors.Is(err, sqlite.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Задача не найдена.", false)
	case errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, sqlite.ErrConflict), errors.Is(err, sqlite.ErrIdempotency):
		writeError(w, http.StatusConflict, "conflict", "Операция не соответствует текущему состоянию задачи.", false)
	case errors.Is(err, domain.ErrInvalidMemory):
		writeError(w, http.StatusBadRequest, "invalid_memory", err.Error(), false)
	case errors.Is(err, sqlite.ErrApprovalExpired):
		writeError(w, http.StatusConflict, "approval_expired", "Срок подтверждения истёк. Создайте новую задачу, если действие всё ещё нужно.", false)
	default:
		if strings.Contains(err.Error(), "must contain") || strings.Contains(err.Error(), "must not exceed") {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
			return
		}
		s.logger.Error("API operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Операцию не удалось выполнить.", true)
	}
}

type apiError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	var response apiError
	response.Error.Code = code
	response.Error.Message = message
	response.Error.Retryable = retryable
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	switch item := value.(type) {
	case domain.Task:
		item.Lease = nil
		value = item
	case []domain.Task:
		items := append([]domain.Task(nil), item...)
		for i := range items {
			items[i].Lease = nil
		}
		value = items
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func parseAfter(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("invalid event cursor")
	}
	return parsed, nil
}

func writeSSEData(w io.Writer, event string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	return err
}

func safeRuntimeWarningSet(value any) []string {
	runtime, _ := value.(map[string]any)
	warnings := map[string]struct{}{}
	objects := []map[string]any{runtime}
	for _, key := range []string{"resources", "last_snapshot", "snapshot"} {
		if nested, ok := runtime[key].(map[string]any); ok {
			objects = append(objects, nested)
		}
	}
	for _, object := range objects {
		for _, key := range []string{"warnings", "active_warnings"} {
			switch values := object[key].(type) {
			case []string:
				for _, text := range values {
					warnings[text] = struct{}{}
				}
			case []any:
				for _, value := range values {
					if text, ok := value.(string); ok {
						warnings[text] = struct{}{}
					}
				}
			}
		}
	}
	result := make([]string, 0, len(warnings))
	for warning := range warnings {
		result = append(result, warning)
	}
	sort.Strings(result)
	return result
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func lifecycleEventAlias(event domain.Event) string {
	switch event.Type {
	case "run.paused":
		return "task_paused"
	case "run.resumed":
		return "task_resumed"
	case "task.completed":
		return "task_completed"
	case "task.failed", "run.failed":
		return "task_failed"
	case "task.status_changed":
		var state struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if json.Unmarshal(event.Payload, &state) != nil {
			return ""
		}
		switch {
		case state.To == string(domain.StatusPaused):
			return "task_paused"
		case state.From == string(domain.StatusPaused) && (state.To == string(domain.StatusQueued) || state.To == string(domain.StatusRunning)):
			return "task_resumed"
		case state.To == string(domain.StatusCompleted):
			return "task_completed"
		case state.To == string(domain.StatusFailed):
			return "task_failed"
		}
	}
	return ""
}

func lifecycleAliasStatus(alias string) string {
	switch alias {
	case "task_paused":
		return "paused"
	case "task_resumed":
		return "resumed"
	case "task_completed":
		return "completed"
	case "task_failed":
		return "failed"
	default:
		return ""
	}
}

func publicEvent(event domain.Event) bool {
	if event.Visibility != "user" {
		return false
	}
	if resourceEventType(event.Type) {
		return true
	}
	switch event.Type {
	case "context.compacted", "continuation.requested", "verification.completed", "refinement.evaluated", "refinement.promoted", "refinement.rollback":
		return true
	case "research.warning", "source.updated", "source.fetch.started", "source.fetch.failed", "source.failed", "source.extracted", "search.hit", "query.updated", "verification.updated", "search.provider", "tool.failed", "task.created", "task.status_changed", "task.recovered", "message.created", "message.delta", "plan.created", "step.started", "step.completed", "step.skipped", "tool.started", "tool.completed", "approval.requested", "approval.approved", "approval.rejected", "approval.expired", "artifact.created", "evidence.created", "task.completed", "task.failed", "task.cancelled":
		return true
	default:
		return false
	}
}
