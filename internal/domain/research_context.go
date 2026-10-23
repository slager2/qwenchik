package domain

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxResearchContextBytes = 8192

func sensitiveContextURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return true
	}
	for key, values := range u.Query() {
		lower := strings.ToLower(key)
		for _, marker := range []string{"token", "secret", "password", "credential", "signature", "authorization", "api_key", "apikey"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
		for _, value := range values {
			lowerValue := strings.ToLower(value)
			if strings.Contains(lowerValue, "bearer ") || strings.HasPrefix(lowerValue, "eyj") && strings.Count(lowerValue, ".") >= 2 {
				return true
			}
		}
	}
	for _, part := range strings.FieldsFunc(strings.ToLower(u.Path), func(r rune) bool { return r == '/' || r == '=' || r == ';' }) {
		switch part {
		case "token", "access_token", "api_key", "apikey", "secret", "password", "credentials", "bearer", "session":
			return true
		}
		if len(part) >= 64 && !strings.Contains(part, "-") {
			return true
		}
	}
	for _, r := range u.Path {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func SensitiveResearchContextURL(raw string) bool { return sensitiveContextURL(raw) }

type ResearchContextClaim struct {
	Text       string `json:"text"`
	EvidenceID string `json:"evidence_id"`
}

type ResearchContextSource struct {
	EvidenceID   string `json:"evidence_id"`
	Title        string `json:"title"`
	CanonicalURL string `json:"canonical_url"`
}

type ResearchContext struct {
	Version            int                     `json:"version"`
	RunID              string                  `json:"run_id"`
	VerificationStatus string                  `json:"verification_status"`
	TerminalOutcome    string                  `json:"terminal_outcome"`
	Summary            string                  `json:"summary"`
	Claims             []ResearchContextClaim  `json:"claims"`
	Sources            []ResearchContextSource `json:"sources"`
	Warnings           []string                `json:"warnings"`
	Limitations        []string                `json:"limitations"`
}

func (researchContext ResearchContext) Validate() error {
	if researchContext.Version != 1 || strings.TrimSpace(researchContext.RunID) == "" || len(researchContext.RunID) > 128 || researchContext.VerificationStatus != "supported" && researchContext.VerificationStatus != "fallback_excerpts" || len(researchContext.Claims) > 6 || len(researchContext.Sources) > 6 || len(researchContext.Warnings) > 8 || len(researchContext.Limitations) > 8 {
		return errors.New("invalid research context")
	}
	if researchContext.TerminalOutcome != "" && researchContext.TerminalOutcome != "completed" && researchContext.TerminalOutcome != "partial" && researchContext.TerminalOutcome != "blocked" && researchContext.TerminalOutcome != "budget_exhausted" && researchContext.TerminalOutcome != "failed" && researchContext.TerminalOutcome != "cancelled" {
		return errors.New("invalid research context outcome")
	}
	if !utf8.ValidString(researchContext.Summary) || utf8.RuneCountInString(researchContext.Summary) > 1600 {
		return errors.New("research context summary exceeds bound")
	}
	sources := map[string]bool{}
	for _, source := range researchContext.Sources {
		if source.EvidenceID == "" || len(source.EvidenceID) > 128 || sources[source.EvidenceID] || !utf8.ValidString(source.Title) || utf8.RuneCountInString(source.Title) > 160 {
			return errors.New("invalid research context source")
		}
		if len(source.CanonicalURL) > 1024 || sensitiveContextURL(source.CanonicalURL) {
			return errors.New("invalid research context URL")
		}
		sources[source.EvidenceID] = true
	}
	for _, claim := range researchContext.Claims {
		if strings.TrimSpace(claim.Text) == "" || !utf8.ValidString(claim.Text) || utf8.RuneCountInString(claim.Text) > 500 || !sources[claim.EvidenceID] {
			return errors.New("invalid research context claim reference")
		}
	}
	for _, items := range [][]string{researchContext.Warnings, researchContext.Limitations} {
		for _, value := range items {
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 300 {
				return errors.New("research context note exceeds bound")
			}
		}
	}
	encoded, err := json.Marshal(researchContext)
	if err != nil {
		return err
	}
	if len(encoded) > MaxResearchContextBytes {
		return MaxResearchContextTooLarge
	}
	return nil
}

var MaxResearchContextTooLarge = errors.New("research context exceeds 8192-byte limit")
