package tools

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/providers/research"
)

var httpURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

type URLLedger struct {
	mu            sync.RWMutex
	byTask        map[string]map[string]struct{}
	fetchedByTask map[string]map[string]struct{}
}

func NewURLLedger() *URLLedger {
	return &URLLedger{byTask: make(map[string]map[string]struct{}), fetchedByTask: make(map[string]map[string]struct{})}
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

func (l *URLLedger) URLs(taskID string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	urls := make([]string, 0, len(l.byTask[taskID]))
	for rawURL := range l.byTask[taskID] {
		urls = append(urls, rawURL)
	}
	sort.Strings(urls)
	return urls
}

func (l *URLLedger) MarkFetched(taskID, rawURL string) {
	if taskID == "" {
		return
	}
	canonical, err := provenance.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fetchedByTask[taskID] == nil {
		l.fetchedByTask[taskID] = make(map[string]struct{})
	}
	l.fetchedByTask[taskID][canonical] = struct{}{}
	if l.byTask[taskID] == nil {
		l.byTask[taskID] = make(map[string]struct{})
	}
	l.byTask[taskID][canonical] = struct{}{}
}

func (l *URLLedger) FetchedURLs(taskID string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	urls := make([]string, 0, len(l.fetchedByTask[taskID]))
	for rawURL := range l.fetchedByTask[taskID] {
		urls = append(urls, rawURL)
	}
	sort.Strings(urls)
	return urls
}

func (l *URLLedger) WasFetched(taskID, rawURL string) bool {
	canonical, err := provenance.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.fetchedByTask[taskID][canonical]
	return ok
}

func (l *URLLedger) Clear(taskID string) {
	l.mu.Lock()
	delete(l.byTask, taskID)
	delete(l.fetchedByTask, taskID)
	l.mu.Unlock()
}
