package domain

type SourceNote struct {
	Version            int      `json:"version"`
	EvidenceID         string   `json:"evidence_id"`
	CanonicalURL       string   `json:"canonical_url"`
	Title              string   `json:"title"`
	SourceType         string   `json:"source_type"`
	RetrievedAt        string   `json:"retrieved_at"`
	ContentHash        string   `json:"content_hash"`
	ExtractedTextHash  string   `json:"extracted_text_hash"`
	RelevantExcerpts   []string `json:"relevant_excerpts"`
	Claims             []string `json:"claims"`
	SupportedCriteria  []string `json:"supported_criteria"`
	UnresolvedCriteria []string `json:"unresolved_criteria"`
	QualityFlags       []string `json:"quality_flags"`
	SourceFailureState string   `json:"source_failure_state"`
	Taint              string   `json:"taint"`
}
