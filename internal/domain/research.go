package domain

import "time"

const (
	ResearchProfileQuick      = "quick"
	ResearchProfileStandard   = "standard"
	ResearchProfileDeepLaptop = "deep_laptop"
	ResearchProfileLongLaptop = "long_laptop"
	ResearchProfileOvernight  = "overnight_laptop"
)

type ResearchBudget struct {
	WallTimeSeconds      int64 `json:"wall_time_seconds"`
	MaxRounds            int   `json:"max_rounds"`
	MaxSteps             int   `json:"max_steps"`
	MaxSearchQueries     int   `json:"max_search_queries"`
	MaxFetches           int   `json:"max_fetches"`
	MaxLLMCalls          int   `json:"max_llm_calls"`
	MaxEvidence          int   `json:"max_evidence"`
	MaxQuestionsCreated  int   `json:"max_questions_created"`
	MaxQuestionsAccepted int   `json:"max_questions_accepted"`
	MaxStagnantRounds    int   `json:"max_stagnant_rounds"`
	MaxCitationSources   int   `json:"max_citation_sources"`
	MaxCitationClaims    int   `json:"max_citation_claims"`
}

type ResearchPolicy struct {
	Version               int            `json:"version"`
	Profile               string         `json:"profile"`
	AdaptiveEnabled       bool           `json:"adaptive_enabled"`
	Budget                ResearchBudget `json:"budget"`
	TaskFetchBudgetBytes  int64          `json:"task_fetch_budget_bytes"`
	SourceFetchBytes      int64          `json:"source_fetch_bytes"`
	PDFBytes              int64          `json:"pdf_bytes"`
	PDFPages              int            `json:"pdf_pages"`
	PDFTextRunes          int            `json:"pdf_text_runes"`
	PDFDownloadTimeoutSec int            `json:"pdf_download_timeout_seconds"`
	PDFExtractTimeoutSec  int            `json:"pdf_extraction_timeout_seconds"`
	PDFMemoryBytes        int64          `json:"pdf_memory_bytes"`
	PDFTempBytes          int64          `json:"pdf_temp_bytes"`
	PDFCPULimit           float64        `json:"pdf_cpu_limit"`
	PDFPIDLimit           int            `json:"pdf_pid_limit"`
	AllowUnsandboxedPDF   bool           `json:"allow_unsandboxed_pdf"`
}

type ModelProfileSnapshot struct {
	Version         int      `json:"version"`
	Model           string   `json:"model"`
	BaseURL         string   `json:"base_url"`
	ContextLength   int      `json:"context_length"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Temperature     float64  `json:"temperature"`
	TopP            float64  `json:"top_p"`
	StopSequences   []string `json:"stop_sequences"`
	PromptVersion   string   `json:"prompt_version"`
	SchemaVersion   int      `json:"schema_version"`
}

type ResearchCounters struct {
	LLMCalls          int   `json:"llm_calls"`
	HTTPAttempts      int   `json:"http_attempts"`
	SearchQueries     int   `json:"search_queries"`
	Fetches           int   `json:"fetches"`
	FetchedBytes      int64 `json:"fetched_bytes"`
	Evidence          int   `json:"evidence"`
	QuestionsCreated  int   `json:"questions_created"`
	QuestionsAccepted int   `json:"questions_accepted"`
	Rounds            int   `json:"rounds"`
	StagnantRounds    int   `json:"stagnant_rounds"`
}

type ResearchSession struct {
	ID             string               `json:"id"`
	TaskID         string               `json:"task_id"`
	ConversationID string               `json:"conversation_id"`
	Goal           string               `json:"goal"`
	Status         string               `json:"status"`
	Profile        string               `json:"profile"`
	Policy         ResearchPolicy       `json:"policy"`
	Budget         ResearchBudget       `json:"budget"`
	ModelSnapshot  ModelProfileSnapshot `json:"model_snapshot"`
	Counters       ResearchCounters     `json:"counters"`
	CurrentRound   int                  `json:"current_round"`
	CurrentPhase   string               `json:"current_phase"`
	PlanVersion    int                  `json:"plan_version"`
	StopReason     string               `json:"stop_reason,omitempty"`
	StartedAt      *time.Time           `json:"started_at,omitempty"`
	DeadlineAt     time.Time            `json:"deadline_at"`
	HeartbeatAt    *time.Time           `json:"heartbeat_at,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}
