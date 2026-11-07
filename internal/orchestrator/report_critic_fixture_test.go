package orchestrator

import (
	"encoding/json"
	"strings"

	"github.com/slager2/qwenchik/internal/llm"
)

// isIndexedReportCritic reports whether a fake model request is an indexed
// report critic call.
func isIndexedReportCritic(user string) bool {
	return strings.Contains(user, `"critic_format":"indexed"`)
}

// indexedCriticReply answers an indexed report critic call the way the
// legacy test fakes did: each claim is "supported" with its own text as the
// quote from its first source, unless supported(claim) is false.
func indexedCriticReply(user string, covered []string, supported func(string) bool) (string, error) {
	var input struct {
		Claims []struct {
			Index   int      `json:"index"`
			Claim   string   `json:"claim"`
			Sources []string `json:"sources"`
		} `json:"claims_to_check"`
	}
	if err := json.Unmarshal([]byte(user), &input); err != nil {
		return "", err
	}
	results := []reportCriticResult{}
	for _, claim := range input.Claims {
		result := reportCriticResult{Index: claim.Index, Kind: "unknown", Evidence: []reportCriticEvidence{}}
		if supported == nil || supported(claim.Claim) {
			result.Kind = "supported"
			result.Evidence = []reportCriticEvidence{{EvidenceID: claim.Sources[0], Quote: claim.Claim}}
		}
		results = append(results, result)
	}
	if covered == nil {
		covered = []string{}
	}
	raw, err := json.Marshal(reportCriticReview{Claims: results, CriteriaCovered: covered})
	return string(raw), err
}

// reportFactsFixtureReply answers fact extraction with one record quoting
// the start of the first excerpt, the fact checker with "supported" and the
// TL;DR writer with one bullet per statement (at most three).
func reportFactsFixtureReply(request llm.ChatRequest, evidence []citationEvidence) (string, bool) {
	system := request.Messages[0].Content
	switch {
	case strings.Contains(system, "You extract fact records"):
		if len(evidence) == 0 {
			return `{"facts":[]}`, true
		}
		quote := strings.TrimSuffix(truncateRunes(evidence[0].Excerpt, 60), "…")
		// A Russian value with no numbers: shown in Russian and English
		// reports alike and grounded in any quote.
		value := "выдержка источника"
		raw, _ := json.Marshal(map[string]any{"facts": []map[string]string{{"entity": "Protocol " + evidence[0].EvidenceID, "attribute": "описание", "value": value, "year": "", "evidence_id": evidence[0].EvidenceID, "quote": quote}}})
		return string(raw), true
	case strings.Contains(system, "You check fact records"):
		var input struct {
			Facts []struct {
				Index int `json:"index"`
			} `json:"facts_to_check"`
		}
		_ = json.Unmarshal([]byte(request.Messages[1].Content), &input)
		results := []map[string]any{}
		for _, fact := range input.Facts {
			results = append(results, map[string]any{"index": fact.Index, "verdict": "supported"})
		}
		raw, _ := json.Marshal(map[string]any{"results": results})
		return string(raw), true
	case strings.Contains(system, "You write the TL;DR"):
		var input struct {
			Statements []struct {
				ID   int    `json:"id"`
				Text string `json:"text"`
			} `json:"statements"`
		}
		_ = json.Unmarshal([]byte(request.Messages[1].Content), &input)
		bullets := []map[string]any{}
		for _, statement := range input.Statements[:min(3, len(input.Statements))] {
			bullets = append(bullets, map[string]any{"text": statement.Text, "statements": []int{statement.ID}})
		}
		raw, _ := json.Marshal(map[string]any{"bullets": bullets})
		return string(raw), true
	}
	return "", false
}
