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
	"strconv"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
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
}

type WorkerControl interface {
	Wake()
	Interrupt(string)
}

type ArtifactReader interface {
	Read(context.Context, string, string) ([]byte, error)
}

type Server struct {
	service          TaskService
	worker           WorkerControl
	dbPing           func(context.Context) error
	llmURL           string
	llmKey           string
	searchURL        string
	artifactReader   ArtifactReader
	sandboxAvailable bool
	sandboxMessage   string
	logger           *slog.Logger
	client           *http.Client
}

func New(service TaskService, worker WorkerControl, dbPing func(context.Context) error, llmURL, llmKey, searchURL string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{service: service, worker: worker, dbPing: dbPing, llmURL: strings.TrimRight(llmURL, "/"), llmKey: llmKey, searchURL: strings.TrimRight(searchURL, "/"), logger: logger, client: &http.Client{
		Timeout:       3 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
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
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) SetArtifactReader(reader ArtifactReader) { s.artifactReader = reader }

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
	if task.Status != domain.StatusInbox && task.Status != domain.StatusQueued && task.Status != domain.StatusPaused {
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
	if task.Status == domain.StatusInbox || task.Status == domain.StatusPaused {
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
	events, err := s.service.Events(r.Context(), r.PathValue("taskID"), after, 500)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	result := make([]domain.Event, 0, len(events))
	for _, event := range events {
		if publicEvent(event) {
			result = append(result, event)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if _, err := s.service.GetTask(r.Context(), taskID); err != nil {
		s.writeServiceError(w, err)
		return
	}
	after, err := parseAfter(r.Header.Get("Last-Event-ID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Заголовок Last-Event-ID должен быть неотрицательным целым числом.", false)
		return
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
			if event.Seq > after {
				after = event.Seq
			}
			if !publicEvent(event) {
				continue
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, encoded); err != nil {
				return
			}
			flusher.Flush()
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

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.dbPing(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "database": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "database": "ok"})
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	checks := map[string]map[string]string{}
	if FrontendAvailable() {
		checks["frontend"] = map[string]string{"status": "ok", "message": "embedded frontend assets are available"}
	} else {
		checks["frontend"] = map[string]string{"status": "error", "message": "embedded frontend assets are missing"}
	}
	if err := s.dbPing(r.Context()); err != nil {
		checks["database"] = map[string]string{"status": "error", "message": "SQLite connection failed"}
	} else {
		checks["database"] = map[string]string{"status": "ok", "message": "SQLite is writable"}
	}
	if s.searchURL == "" {
		checks["search"] = map[string]string{"status": "warning", "message": "SearXNG is not configured"}
	} else {
		searchRequest, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.searchURL+"/search?q=qagent-doctor&format=json&safesearch=1", nil)
		if err != nil {
			checks["search"] = map[string]string{"status": "warning", "message": "SearXNG health request could not be created"}
		} else if response, err := s.client.Do(searchRequest); err != nil {
			checks["search"] = map[string]string{"status": "warning", "message": "Configured SearXNG endpoint is unavailable"}
		} else {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				checks["search"] = map[string]string{"status": "ok", "message": "SearXNG search endpoint responded"}
			} else {
				checks["search"] = map[string]string{"status": "warning", "message": fmt.Sprintf("SearXNG returned HTTP %d", response.StatusCode)}
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
			checks["llm"] = map[string]string{"status": "ok", "message": "OpenAI-compatible models endpoint responded"}
		} else {
			checks["llm"] = map[string]string{"status": "error", "message": fmt.Sprintf("LLM endpoint returned HTTP %d", response.StatusCode)}
		}
	}
	status := "ok"
	for _, check := range checks {
		if check["status"] == "error" {
			status = "error"
			break
		}
	}
	code := http.StatusOK
	if status == "error" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": checks})
}

func (s *Server) providers(w http.ResponseWriter, _ *http.Request) {
	searchStatus := "disabled"
	if s.searchURL != "" {
		searchStatus = "configured"
	}
	sandboxStatus := "unavailable"
	if s.sandboxAvailable {
		sandboxStatus = "available"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"llm":     map[string]any{"type": "local-openai-compatible", "status": "configured", "base_url": s.llmURL},
		"search":  map[string]any{"type": "searxng", "status": searchStatus, "base_url": s.searchURL},
		"browser": map[string]string{"status": "disabled"},
		"sandbox": map[string]string{"status": sandboxStatus, "message": s.sandboxMessage},
	})
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sqlite.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Задача не найдена.", false)
	case errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, sqlite.ErrConflict), errors.Is(err, sqlite.ErrIdempotency):
		writeError(w, http.StatusConflict, "conflict", "Операция не соответствует текущему состоянию задачи.", false)
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

func publicEvent(event domain.Event) bool {
	if event.Visibility != "user" {
		return false
	}
	switch event.Type {
	case "task.created", "task.status_changed", "task.recovered", "message.created", "plan.created", "step.started", "step.completed", "tool.started", "tool.completed", "approval.requested", "approval.approved", "approval.rejected", "approval.expired", "artifact.created", "evidence.created", "task.completed", "task.failed", "task.cancelled":
		return true
	default:
		return false
	}
}
