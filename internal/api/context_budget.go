package api

import (
	"context"
	"encoding/json"
	"github.com/slager2/qwenchik/internal/domain"
)

func (s *Server) SetContextInspector(inspect func(context.Context, int, int, string) (domain.ContextBudget, error)) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.contextInspector = inspect
}
func (s *Server) contextBudget(ctx context.Context, session, output int, identity string) (domain.ContextBudget, bool) {
	s.healthMu.RLock()
	inspect := s.contextInspector
	s.healthMu.RUnlock()
	if inspect == nil {
		return domain.ContextBudget{}, false
	}
	d, _ := inspect(ctx, session, output, identity)
	return d, true
}
func (s *Server) addRunContext(ctx context.Context, result map[string]any) {
	var stored domain.ContextBudget
	if raw, err := json.Marshal(result["context_budget"]); err == nil {
		_ = json.Unmarshal(raw, &stored)
	}
	session := 0
	if stored.SessionContext != nil {
		session = *stored.SessionContext
	}
	identity, _ := result["runtime_model_fingerprint"].(string)
	if d, ok := s.contextBudget(ctx, session, stored.MaxOutputTokens, identity); ok {
		result["context_budget"] = d
	}
}
