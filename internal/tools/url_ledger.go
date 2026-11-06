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
	// passagesByTask keeps agentSearcher passages per canonical URL. They
	// locate the evidence excerpt in the fetched page text.
	passagesByTask map[string]map[string][]string
}

// maxLedgerPassagesPerURL bounds the passages kept for one URL when several
// searches return the same page.
const maxLedgerPassagesPerURL = 4

func NewURLLedger() *URLLedger {
	return &URLLedger{byTask: make(map[string]map[string]struct{}), fetchedByTask: make(map[string]map[string]struct{}), passagesByTask: make(map[string]map[string][]string)}
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
		l.addPassage(taskID, item.Provider, item.URL, item.Snippet)
	case []research.SearchHit:
		for _, hit := range item {
			l.Add(taskID, hit.URL)
			l.addPassage(taskID, hit.Provider, hit.URL, hit.Snippet)
		}
	case map[string]any:
		if raw, ok := item["url"].(string); ok {
			l.Add(taskID, raw)
			provider, _ := item["provider"].(string)
			snippet, _ := item["snippet"].(string)
			l.addPassage(taskID, provider, raw, snippet)
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

// addPassage remembers the page passages agentSearcher returned for a URL.
// Other providers return short engine snippets, which are not page text.
func (l *URLLedger) addPassage(taskID, provider, rawURL, passage string) {
	passage = strings.TrimSpace(passage)
	if taskID == "" || provider != "agentsearcher" || passage == "" {
		return
	}
	canonical, err := provenance.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.passagesByTask[taskID] == nil {
		l.passagesByTask[taskID] = make(map[string][]string)
	}
	existing := l.passagesByTask[taskID][canonical]
	for index, known := range existing {
		if strings.Contains(known, passage) {
			return
		}
		if strings.Contains(passage, known) {
			existing[index] = passage
			return
		}
	}
	if len(existing) < maxLedgerPassagesPerURL {
		l.passagesByTask[taskID][canonical] = append(existing, passage)
	}
}

// Passages returns the agentSearcher passages remembered for a URL.
func (l *URLLedger) Passages(taskID, rawURL string) []string {
	canonical, err := provenance.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]string(nil), l.passagesByTask[taskID][canonical]...)
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
	delete(l.passagesByTask, taskID)
	l.mu.Unlock()
}
