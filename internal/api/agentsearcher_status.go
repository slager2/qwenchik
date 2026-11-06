package api

// agentSearcherStatus describes the optional loopback agentSearcher search
// service. It is kept separate from the other provider fields so the status
// entry stays a small, self-contained addition.
type agentSearcherStatus struct {
	baseURL string
	depth   string
}

// SetAgentSearcher records the configured agentSearcher endpoint and depth for
// /api/providers and /api/health. An empty URL leaves the provider disabled.
func (s *Server) SetAgentSearcher(baseURL, depth string) {
	s.agentSearcher = agentSearcherStatus{baseURL: baseURL, depth: depth}
}

func (s *Server) addAgentSearcherStatus(result map[string]map[string]any) {
	enabled := s.agentSearcher.baseURL != ""
	entry := map[string]any{
		"configured": enabled, "enabled": enabled, "status": providerState(enabled),
		"health": "not_checked", "last_checked": nil, "last_outcome": nil,
		"endpoint": safeEndpoint(s.agentSearcher.baseURL), "primary": enabled,
	}
	if enabled {
		entry["search_depth"] = s.agentSearcher.depth
	}
	result["agentsearcher"] = entry
}
