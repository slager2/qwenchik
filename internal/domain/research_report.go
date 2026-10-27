package domain

import "encoding/json"

type ReportSection struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Criteria       []string        `json:"criteria"`
	EvidenceIDs    []string        `json:"evidence_ids"`
	Status         string          `json:"status"`
	Body           string          `json:"markdown"`
	CorrectionUsed bool            `json:"correction_used"`
	Review         json.RawMessage `json:"citation_review,omitempty"`
	Limitation     string          `json:"limitation,omitempty"`
	RejectedDraft  string          `json:"rejected_draft,omitempty"`
}

type ResearchReportState struct {
	ByteBudgetFallback    bool                     `json:"byte_budget_fallback,omitempty"`
	PrivacyMode           string                   `json:"privacy_mode"`
	OfflineCorpusSHA256   string                   `json:"offline_corpus_sha256,omitempty"`
	ExecutionCriteria     []string                 `json:"execution_criteria"`
	SourceLimitations     []ReportSourceLimitation `json:"source_limitations"`
	Version               int                      `json:"version"`
	InputHash             string                   `json:"input_hash"`
	Title                 string                   `json:"title"`
	ExecutiveSummary      string                   `json:"executive_summary"`
	ResearchQuestion      string                   `json:"research_question"`
	Sections              []ReportSection          `json:"sections"`
	Unresolved            []string                 `json:"unresolved_criteria"`
	SupplementaryMissing  []string                 `json:"supplementary_missing"`
	UnrepresentedCriteria []string                 `json:"unrepresented_criteria"`
	GeneratedAt           string                   `json:"generated_at"`
	ArtifactID            string                   `json:"artifact_id"`
	ArtifactSHA256        string                   `json:"artifact_sha256"`
	MarkdownArtifactID    string                   `json:"markdown_artifact_id"`
	Outcome               string                   `json:"final_outcome"`
}

type ReportSourceLimitation struct {
	Code         string `json:"code"`
	Phase        string `json:"phase"`
	FetchedBytes int64  `json:"fetched_bytes"`
}
