package tools

import (
	"regexp"
	"strings"
	"sync"

	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/providers/research"
)

var httpURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

type URLLedger struct {
	mu     sync.RWMutex
	byTask map[string]map[string]struct{}
}

func NewURLLedger() *URLLedger {
	return &URLLedger{byTask: make(map[string]map[string]struct{})}
}

func (l *URLLedger) SeedText(taskID, text string) {
	for _, raw := range httpURLPattern.FindAllString(text, -1) {
		l.Add(taskID, strings.TrimRight(raw, ".,;:!?)]}"))
	}
}

func (l *URLLedger) Remember(taskID string, value any) {
	switch item := value.(type) {
	case research.SearchHit:
		l.Add(taskID, item.URL)
	case []research.SearchHit:
		for _, hit := range item {
			l.Add(taskID, hit.URL)
		}
	case map[string]any:
		if raw, ok := item["url"].(string); ok {
			l.Add(taskID, raw)
		}
		for _, child := range item {
			l.Remember(taskID, child)
		}
	case []any:
		for _, child := range item {
			l.Remember(taskID, child)
		}
	}
}

func (l *URLLedger) Add(taskID, rawURL string) {
	if taskID == "" {
		return
	}
	canonical, err := provenance.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byTask[taskID] == nil {
		l.byTask[taskID] = make(map[string]struct{})
	}
	l.byTask[taskID][canonical] = struct{}{}
}

func (l *URLLedger) Contains(taskID, rawURL string) bool {
	canonical, err := provenance.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.byTask[taskID][canonical]
	return ok
}

func (l *URLLedger) Clear(taskID string) {
	l.mu.Lock()
	delete(l.byTask, taskID)
	l.mu.Unlock()
}
