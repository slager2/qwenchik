package api

import "net/http"

// SetLabHoldResourcePauses records the worker's effective configured hold policy.
func (s *Server) SetLabHoldResourcePauses(hold bool) {
	s.healthMu.Lock()
	s.labHoldResourcePauses = hold
	s.healthMu.Unlock()
}

// labPolicy reports configuration only. It performs no readiness checks or probes.
func (s *Server) labPolicy(w http.ResponseWriter, r *http.Request) {
	s.healthMu.RLock()
	policy := map[string]any{"configured": false, "pdf_allow_unsandboxed_fallback": false}
	// SetResearchPolicy already sanitizes this map to allowlisted scalar values.
	for key, value := range s.researchPolicy {
		policy[key] = value
	}
	hold := s.labHoldResourcePauses
	s.healthMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"research_limits":      policy,
		"hold_resource_pauses": hold,
	})
}
