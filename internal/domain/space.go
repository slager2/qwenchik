package domain

import "time"

// Space groups conversations and their durable, verified research history.
// Conversations without a SpaceID remain in the legacy "Unfiled" view.
type Space struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

type SpaceResearchRun struct {
	Run               Run       `json:"run"`
	ConversationTitle string    `json:"conversation_title"`
	Goal              string    `json:"goal"`
	VerificationStatus string   `json:"verification_status,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type SpaceContextItem struct {
	RunID              string                  `json:"run_id"`
	ConversationID     string                  `json:"conversation_id"`
	ConversationTitle  string                  `json:"conversation_title"`
	VerificationStatus string                  `json:"verification_status"`
	Summary            string                  `json:"summary"`
	Sources            []ResearchContextSource `json:"sources"`
	UpdatedAt          time.Time               `json:"updated_at"`
}
