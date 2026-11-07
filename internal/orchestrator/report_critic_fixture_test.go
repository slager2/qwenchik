package orchestrator

import (
	"encoding/json"
	"strings"
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
