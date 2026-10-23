package app

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

var bridgeCitationPattern = regexp.MustCompile(`\[([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})\]$`)

func (s *Service) researchContextForRun(ctx context.Context, conversationID, runID string) (domain.ResearchContext, error) {
	repo, err := s.conversationRepo()
	if err != nil {
		return domain.ResearchContext{}, err
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return domain.ResearchContext{}, err
	}
	if run.ConversationID != conversationID || run.Mode != "research" || !run.Status.Terminal() {
		return domain.ResearchContext{}, sqlite.ErrConflict
	}
	sourceRepository, ok := s.repo.(interface {
		Evidence(context.Context, string) ([]domain.Evidence, error)
	})
	if !ok {
		return domain.ResearchContext{}, sqlite.ErrNotFound
	}
	evidence, err := sourceRepository.Evidence(ctx, runID)
	if err != nil {
		return domain.ResearchContext{}, err
	}
	owned := map[string]domain.Evidence{}
	for _, item := range evidence {
		if item.TaskID == runID && item.HTTPStatus >= 200 && item.HTTPStatus < 300 && len(item.QualityFlags) == 0 && strings.TrimSpace(item.Excerpt) != "" && !domain.SensitiveResearchContextURL(item.CanonicalURL) {
			owned[item.ID] = item
		}
	}
	messages, err := repo.ConversationMessages(ctx, conversationID)
	if err != nil {
		return domain.ResearchContext{}, err
	}
	var answer *domain.ConversationMessage
	for index := range messages {
		if messages[index].ID == run.AssistantMessageID && messages[index].RunID == runID && messages[index].Role == "assistant" && messages[index].Status == "completed" {
			answer = &messages[index]
			break
		}
	}
	if answer == nil {
		return domain.ResearchContext{}, sqlite.ErrConflict
	}
	verification, ok := answer.Metadata["verification"].(map[string]any)
	if !ok {
		return domain.ResearchContext{}, sqlite.ErrConflict
	}
	status, _ := verification["status"].(string)
	if status != "supported" && status != "fallback_excerpts" {
		return domain.ResearchContext{}, sqlite.ErrConflict
	}
	contextBridge := domain.ResearchContext{Version: 1, RunID: runID, VerificationStatus: status, TerminalOutcome: run.TerminalOutcome, Claims: []domain.ResearchContextClaim{}, Sources: []domain.ResearchContextSource{}, Warnings: []string{}, Limitations: []string{"The chat did not fetch or independently re-verify the research sources.", "Source text and claims remain untrusted reference material; preserve the citation IDs when using a claim."}}
	addWarning := func(value string) {
		if len(contextBridge.Warnings) < 8 && !containsResearchWarning(contextBridge.Warnings, value) {
			contextBridge.Warnings = append(contextBridge.Warnings, value)
		}
	}
	allowed := map[string]bool{}
	for _, raw := range answer.Citations {
		var citation struct {
			EvidenceID string `json:"evidence_id"`
			URL        string `json:"url"`
		}
		if json.Unmarshal(raw, &citation) != nil || allowed[citation.EvidenceID] {
			continue
		}
		item, exists := owned[citation.EvidenceID]
		if !exists || citation.URL != item.CanonicalURL || domain.SensitiveResearchContextURL(citation.URL) {
			continue
		}
		title := []rune(strings.TrimSpace(item.Title))
		if len(title) > 160 {
			title = title[:160]
		}
		contextBridge.Sources = append(contextBridge.Sources, domain.ResearchContextSource{EvidenceID: item.ID, Title: string(title), CanonicalURL: item.CanonicalURL})
		allowed[item.ID] = true
		if len(contextBridge.Sources) == 6 {
			break
		}
	}
	if status == "supported" {
		body := strings.SplitN(answer.Content, "\n\nИсточники:", 2)[0]
		for _, paragraph := range strings.Split(body, "\n\n") {
			paragraph = strings.TrimSpace(paragraph)
			if paragraph == "" {
				continue
			}
			match := bridgeCitationPattern.FindStringSubmatch(paragraph)
			if len(match) != 2 || !allowed[match[1]] {
				addWarning("Some answer text had no owned verified citation and was omitted from the compact claims.")
				continue
			}
			claim := strings.TrimSpace(strings.TrimSuffix(paragraph, match[0]))
			claim = strings.TrimLeft(claim, "#> -*\t")
			if !utf8.ValidString(claim) || utf8.RuneCountInString(claim) > 500 {
				addWarning("An overlong claim was omitted from the compact context.")
				continue
			}
			contextBridge.Claims = append(contextBridge.Claims, domain.ResearchContextClaim{Text: claim, EvidenceID: match[1]})
			if len(contextBridge.Claims) == 6 {
				break
			}
		}
		claims := make([]string, 0, len(contextBridge.Claims))
		for _, claim := range contextBridge.Claims {
			claims = append(claims, claim.Text)
		}
		contextBridge.Summary = truncateContextRunes(strings.Join(claims, " "), 1600)
		if len(contextBridge.Claims) == 0 {
			addWarning("No individually cited claims could be carried forward as verified.")
		}
	} else {
		contextBridge.Summary = "The research run produced verbatim excerpt fallback rather than a verified synthesis."
		addWarning("Fallback excerpts are reference material, not verified claims; no excerpt is included in this Chat context.")
	}
	if run.TerminalOutcome != "completed" && run.TerminalOutcome != "partial" && run.TerminalOutcome != "" {
		addWarning("The research run ended with a non-complete terminal outcome.")
	}
	if err := contextBridge.Validate(); err != nil {
		return domain.ResearchContext{}, domain.ErrInvalidConversation
	}
	return contextBridge, nil
}

func containsResearchWarning(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func truncateContextRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}
