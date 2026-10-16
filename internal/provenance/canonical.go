package provenance

import (
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/urlcanon"
)

var ErrInvalidSourceURL = errors.New("invalid source URL")

func CanonicalURL(raw string) (string, error) {
	canonical, err := urlcanon.Canonical(raw)
	if err != nil {
		return "", ErrInvalidSourceURL
	}
	return canonical, nil
}

func BuildEvidence(taskID, query string, fetched research.FetchedDocument, extracted research.ExtractedDocument, rawArtifactID, textArtifactID string) (domain.Evidence, error) {
	canonical, err := CanonicalURL(fetched.FinalURL)
	if err != nil {
		return domain.Evidence{}, err
	}
	if taskID == "" || fetched.ContentSHA256 == "" {
		return domain.Evidence{}, errors.New("evidence requires a task ID and content hash")
	}
	retrievedAt := fetched.RetrievedAt
	if retrievedAt.IsZero() {
		retrievedAt = time.Now().UTC()
	}
	excerpt := selectEvidenceExcerpt(extracted.Text, query, 1200)
	quotes := []domain.QuoteSpan{}
	if excerpt != "" {
		quotes = append(quotes, domain.QuoteSpan{Start: 0, End: utf8.RuneCountInString(excerpt), Text: excerpt})
	}
	provider := fetched.Provider
	if provider == "" {
		provider = "direct_http"
	}
	return domain.Evidence{
		TaskID: taskID, Provider: provider, OriginalQuery: query,
		OriginalURL: fetched.OriginalURL, CanonicalURL: canonical,
		Title: extracted.Title, Language: fetched.Language, RetrievedAt: retrievedAt, HTTPStatus: fetched.HTTPStatus,
		ContentSHA256: fetched.ContentSHA256, ExtractionMethod: extracted.Method,
		RawArtifactID: rawArtifactID, TextArtifactID: textArtifactID,
		Excerpt: excerpt, QuoteSpans: quotes, QualityFlags: append([]string(nil), extracted.QualityFlags...),
		Taint: research.TaintExternal,
	}, nil
}

func selectEvidenceExcerpt(text, query string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes < 1 || text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}

	queryTerms := excerptQueryTerms(query)
	if len(queryTerms) == 0 {
		return string(runes[:maxRunes])
	}
	type scoredLine struct {
		start  int
		end    int
		length int
		terms  map[string]int
	}
	lines := make([]scoredLine, 0)
	documentFrequency := make(map[string]int, len(queryTerms))
	runeOffset := 0
	var totalLength int
	chunkSize := min(500, maxRunes)
	chunkStride := max(1, chunkSize*3/4)
	for _, line := range strings.Split(text, "\n") {
		lineRunes := []rune(line)
		appendChunk := func(start, end int) {
			if end <= start {
				return
			}
			counts := make(map[string]int)
			for _, token := range excerptQueryTerms(string(lineRunes[start:end])) {
				counts[token]++
			}
			for term := range counts {
				documentFrequency[term]++
			}
			lines = append(lines, scoredLine{start: runeOffset + start, end: runeOffset + end, length: end - start, terms: counts})
			totalLength += end - start
		}
		if len(lineRunes) <= chunkSize {
			appendChunk(0, len(lineRunes))
		} else {
			for start := 0; start < len(lineRunes); start += chunkStride {
				end := min(start+chunkSize, len(lineRunes))
				appendChunk(start, end)
				if end == len(lineRunes) {
					break
				}
			}
		}
		runeOffset += len(lineRunes) + 1
	}
	averageLength := float64(totalLength) / float64(max(1, len(lines)))
	bestStart, bestEnd, bestScore := -1, -1, 0.0
	for _, line := range lines {
		if line.length == 0 {
			continue
		}
		score := 0.0
		for _, term := range queryTerms {
			frequency := line.terms[term]
			if frequency == 0 {
				continue
			}
			documents := documentFrequency[term]
			inverseFrequency := math.Log(1 + (float64(len(lines)-documents)+0.5)/(float64(documents)+0.5))
			normalizedFrequency := float64(frequency) * 2.2 / (float64(frequency) + 1.2*(0.25+0.75*float64(line.length)/averageLength))
			score += inverseFrequency * normalizedFrequency
		}
		if score > bestScore {
			bestStart, bestEnd, bestScore = line.start, line.end, score
		}
	}
	if bestStart < 0 || bestScore <= 0 {
		return string(runes[:maxRunes])
	}

	start := max(0, bestStart-maxRunes/4)
	for start < bestStart && runes[start] != '\n' {
		start++
	}
	if start < bestStart {
		start++
	}
	if start > bestStart {
		start = bestStart
	}
	end := min(len(runes), start+maxRunes)
	if bestEnd > end {
		end = min(len(runes), bestStart+maxRunes)
		start = bestStart
	}
	return strings.TrimSpace(string(runes[start:end]))
}

func excerptQueryTerms(value string) []string {
	stopWords := map[string]struct{}{
		"the": {}, "and": {}, "for": {}, "from": {}, "with": {}, "that": {}, "this": {}, "into": {}, "about": {}, "what": {}, "which": {}, "where": {}, "when": {}, "как": {}, "это": {}, "для": {}, "что": {}, "где": {}, "или": {}, "при": {}, "над": {}, "под": {},
	}
	seen := make(map[string]struct{})
	terms := make([]string, 0)
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if utf8.RuneCountInString(token) < 3 {
			continue
		}
		if _, stop := stopWords[token]; stop {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		terms = append(terms, token)
	}
	return terms
}
