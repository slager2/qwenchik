package orchestrator

import (
	"strings"

	"github.com/slager2/qwenchik/internal/llm"
)

// Fake models must select responses by request role rather than call index:
// canonical search/fetch steps run through deterministicResearchAction and no
// longer issue actor inference, so call positions shift with plan shape.

func isPlannerRequest(request llm.ChatRequest) bool {
	return strings.Contains(string(request.JSONSchema), `"subtasks"`)
}

func isVerifierRequest(request llm.ChatRequest) bool {
	return strings.Contains(string(request.JSONSchema), `"criterion_assessments"`)
}
