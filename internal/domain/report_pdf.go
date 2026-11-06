package domain

import "time"

// ReportPDFCandidate is a completed research run whose report PDF has not
// yet been considered for automatic saving.
type ReportPDFCandidate struct {
	TaskID              string
	Title               string
	Profile             string
	StartedAt           time.Time
	CompletedAt         time.Time
	ReportMarkdownBytes int64
}
