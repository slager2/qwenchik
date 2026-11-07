package provenance

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
)

const (
	// ExcerptSpanSeparator joins the verbatim spans of a multi-span excerpt.
	// Every part between separators is an exact rune range of the stored
	// text (one QuoteSpan each); a quote is supported when it lies within
	// one part.
	ExcerptSpanSeparator = "\n\n[...]\n\n"
	// MaxExcerptSpans is the most query-ranked chunks one excerpt keeps.
	MaxExcerptSpans = 5
	// spanContextRunes bounds the neighbouring text kept on each side of a
	// located chunk: the rest of its sentence plus a few more. agentSearcher
	// chunks are often only 150-400 characters, so context matters.
	spanContextRunes = 700
	// maxSpanCoreRunes bounds the located chunk region itself. agentSearcher
	// chunks are about 600 characters; a located region much longer than its
	// chunk would be a false match spread over the page.
	maxSpanCoreRunes = 2400
	// minSpanRunes is the shortest span worth keeping when the budget is
	// nearly used up.
	minSpanRunes = 160
)

// textWords caches the word segmentation of a stored text, shared by every
// chunk located in it.
type textWords struct {
	runes []rune
	words []passageWord
}

func newTextWords(text string) textWords {
	return textWords{runes: []rune(text), words: passageWords(text)}
}

// locate finds one chunk in the text by shared word n-grams and returns the
// rune range of its densest matching region (without context), or ok=false.
func (t textWords) locate(chunk string) (start, end int, ok bool) {
	chunkWords := passageWords(chunk)
	if len(chunkWords) < passageShingleWords || len(t.words) < passageShingleWords {
		return 0, 0, false
	}
	shingles := map[string]struct{}{}
	for i := 0; i+passageShingleWords <= len(chunkWords); i++ {
		shingles[passageShingle(chunkWords, i)] = struct{}{}
	}
	covered := make([]bool, len(t.words))
	for i := 0; i+passageShingleWords <= len(t.words); i++ {
		if _, match := shingles[passageShingle(t.words, i)]; match {
			for j := i; j < i+passageShingleWords; j++ {
				covered[j] = true
			}
		}
	}
	window := min(maxSpanCoreRunes, max(240, utf8.RuneCountInString(chunk)*3/2))
	bestFirst, bestLast, bestCount := -1, -1, 0
	count, left := 0, 0
	for right := range t.words {
		if covered[right] {
			count++
		}
		for t.words[right].end-t.words[left].start > window {
			if covered[left] {
				count--
			}
			left++
		}
		if count > bestCount {
			bestCount = count
			bestFirst, bestLast = -1, -1
			for i := left; i <= right; i++ {
				if covered[i] {
					if bestFirst < 0 {
						bestFirst = i
					}
					bestLast = i
				}
			}
		}
	}
	if bestCount < passageMinMatchedWords {
		return 0, 0, false
	}
	return t.words[bestFirst].start, t.words[bestLast].end, true
}

func sentenceEnd(r rune) bool { return strings.ContainsRune(".!?;", r) }

// spanContextSentences is how many neighbouring sentences (or lines) a
// span keeps on each side of its located chunk.
const spanContextSentences = 3

// expand widens a located region to sentence boundaries with up to
// spanContextSentences neighbouring sentences on each side, at most context
// runes per side. A line break counts as a boundary only after sentence
// punctuation or as a paragraph break, because PDF text wraps lines inside
// sentences; without any sentence boundary in reach the span ends at the
// farthest line break (list items), else at the located region itself.
func (t textWords) expand(start, end, context int) (int, int) {
	runes := t.runes
	boundary := func(position int) bool {
		// position is a whitespace rune that may end a sentence or paragraph.
		if position <= 0 || !unicode.IsSpace(runes[position]) {
			return false
		}
		previous := runes[position-1]
		if sentenceEnd(previous) || previous == ':' {
			return true
		}
		return runes[position] == '\n' && (previous == '\n' || position+1 < len(runes) && runes[position+1] == '\n')
	}
	from := max(0, start-context)
	boundaries, lineStart := 0, -1
	newStart := start
	for position := start - 1; position >= from && boundaries < spanContextSentences+1; position-- {
		if position == 0 {
			newStart, boundaries = 0, boundaries+1
			break
		}
		if boundary(position) {
			if position+1 < newStart && unicode.IsSpace(runes[position+1]) {
				newStart = position + 1
				continue
			}
			boundaries++
			newStart = position + 1
		} else if runes[position] == '\n' {
			lineStart = position + 1
		}
	}
	if boundaries == 0 && lineStart >= 0 {
		newStart = lineStart
	}
	limit := min(len(runes), end+context)
	boundaries, lineEnd := 0, -1
	newEnd := end
	for position := end; position < limit && boundaries < spanContextSentences+1; position++ {
		if position+1 == len(runes) {
			newEnd, boundaries = len(runes), boundaries+1
			break
		}
		if boundary(position) {
			if position > newEnd && unicode.IsSpace(runes[position-1]) {
				continue
			}
			boundaries++
			newEnd = position
		} else if runes[position] == '\n' {
			lineEnd = position
		}
	}
	if boundaries == 0 && lineEnd >= 0 {
		newEnd = lineEnd
	}
	return t.trim(newStart, newEnd)
}

func (t textWords) trim(start, end int) (int, int) {
	for start < end && unicode.IsSpace(t.runes[start]) {
		start++
	}
	for end > start && unicode.IsSpace(t.runes[end-1]) {
		end--
	}
	return start, end
}

var (
	dotLeaderPattern  = regexp.MustCompile(`(?:\.\s?){4,}|…{2,}|(?:·\s?){4,}|_{5,}|(?:-\s?){6,}`)
	pageNumberLine    = regexp.MustCompile(`^\S.{0,120}?[^\d\s.,]\s+\d{1,4}$`)
	tocHeadingPattern = regexp.MustCompile(`(?i)\b(table of contents|contents)\b|содержание|оглавление`)
)

// TableOfContentsNoise reports whether text is dominated by a table of
// contents: dot leaders, or short lines ending in page numbers. Such chunks
// match many query words but state no facts.
func TableOfContentsNoise(text string) bool {
	if len(dotLeaderPattern.FindAllStringIndex(text, -1)) >= 3 {
		return true
	}
	lines, numbered := 0, 0
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		lines++
		if pageNumberLine.MatchString(line) && utf8.RuneCountInString(line) <= 120 {
			numbered++
		}
	}
	if lines >= 4 && numbered*2 >= lines {
		return true
	}
	return lines >= 3 && numbered >= 2 && tocHeadingPattern.MatchString(text)
}

// SelectMultiSpanExcerpt locates up to maxSpans query-ranked chunks (best
// first) in text, widens each with its neighbouring sentences, and returns
// them as verbatim spans in document order joined by ExcerptSpanSeparator,
// at most maxRunes runes in all. Overlapping spans are merged and table of
// contents noise is skipped. Every returned QuoteSpan is an exact rune range
// of text. ok is false when no chunk occurs in text.
func SelectMultiSpanExcerpt(text string, chunks []string, maxSpans, maxRunes int) (excerpt string, spans []domain.QuoteSpan, ok bool) {
	if maxSpans < 1 || maxRunes < minSpanRunes || strings.TrimSpace(text) == "" || len(chunks) == 0 {
		return "", nil, false
	}
	words := newTextWords(text)
	type span struct{ start, end int }
	selected := []span{}
	used := 0
	separator := utf8.RuneCountInString(ExcerptSpanSeparator)
	overlaps := func(candidate span) bool {
		for _, existing := range selected {
			if candidate.start < existing.end && existing.start < candidate.end {
				return true
			}
		}
		return false
	}
	for _, chunk := range chunks {
		if len(selected) == maxSpans {
			break
		}
		if strings.TrimSpace(chunk) == "" || TableOfContentsNoise(chunk) {
			continue
		}
		coreStart, coreEnd, found := words.locate(chunk)
		if !found {
			continue
		}
		start, end := words.expand(coreStart, coreEnd, spanContextRunes)
		candidate := span{start, end}
		if end <= start || TableOfContentsNoise(string(words.runes[start:end])) {
			continue
		}
		// A chunk already inside a kept span adds nothing; one that only
		// overlaps is merged below and costs its new runes.
		covered := false
		for _, existing := range selected {
			covered = covered || coreStart >= existing.start && coreEnd <= existing.end
		}
		if covered {
			continue
		}
		cost := end - start
		if len(selected) > 0 && !overlaps(candidate) {
			cost += separator
		}
		if used+cost > maxRunes {
			// Narrow the context to the remaining budget, else keep the
			// located chunk alone when that fits.
			extra := 0
			if len(selected) > 0 {
				extra = separator
			}
			room := maxRunes - used - extra
			core := coreEnd - coreStart
			if room < minSpanRunes || core > room {
				continue
			}
			candidate = span{}
			candidate.start, candidate.end = words.expand(coreStart, coreEnd, (room-core)/2)
			if candidate.end-candidate.start > room {
				candidate.start, candidate.end = words.trim(coreStart, coreEnd)
			}
			cost = candidate.end - candidate.start + extra
			if candidate.end <= candidate.start || cost > maxRunes-used {
				continue
			}
		}
		selected = append(selected, candidate)
		used += cost
	}
	if len(selected) == 0 {
		return "", nil, false
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].start < selected[j].start })
	merged := []span{selected[0]}
	for _, item := range selected[1:] {
		last := &merged[len(merged)-1]
		if item.start <= last.end || strings.TrimSpace(string(words.runes[last.end:item.start])) == "" {
			last.end = max(last.end, item.end)
			continue
		}
		merged = append(merged, item)
	}
	parts := make([]string, 0, len(merged))
	total := 0
	for _, item := range merged {
		partRunes := item.end - item.start
		if len(parts) > 0 {
			partRunes += separator
		}
		if total+partRunes > maxRunes {
			// Merging can only shrink the sum, so this is a safety bound.
			break
		}
		total += partRunes
		part := string(words.runes[item.start:item.end])
		parts = append(parts, part)
		spans = append(spans, domain.QuoteSpan{Start: item.start, End: item.end, Text: part, CoordinateSpace: "extracted_text_runes"})
	}
	return strings.Join(parts, ExcerptSpanSeparator), spans, len(spans) > 0
}

// ExcerptSpanTexts splits an excerpt into its verbatim parts.
func ExcerptSpanTexts(excerpt string) []string {
	return strings.Split(excerpt, ExcerptSpanSeparator)
}

// QuoteWithinExcerpt reports whether quote lies within one verbatim part of
// an excerpt (multi-span excerpts never let a quote straddle a separator).
func QuoteWithinExcerpt(excerpt, quote string) bool {
	if quote == "" {
		return false
	}
	for _, part := range ExcerptSpanTexts(excerpt) {
		if strings.Contains(part, quote) {
			return true
		}
	}
	return false
}

// FitExcerptBytes bounds an excerpt to maxBytes while keeping every part
// verbatim: a single-span excerpt keeps its prefix; a multi-span excerpt
// gives each part a fair share (short parts stay whole) and keeps the prefix
// of every part, so later spans are not cut off by the earlier ones.
func FitExcerptBytes(excerpt string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(excerpt) <= maxBytes {
		return excerpt
	}
	parts := ExcerptSpanTexts(excerpt)
	budget := maxBytes - (len(parts)-1)*len(ExcerptSpanSeparator)
	if len(parts) == 1 || budget < len(parts)*minSpanRunes {
		return bytePrefix(excerpt, maxBytes)
	}
	// Water-filling: parts shorter than the fair share keep their length.
	share := make([]int, len(parts))
	remaining, open := budget, len(parts)
	done := make([]bool, len(parts))
	for changed := true; changed && open > 0; {
		changed = false
		fair := remaining / open
		for i, part := range parts {
			if !done[i] && len(part) <= fair {
				share[i], done[i] = len(part), true
				remaining -= len(part)
				open--
				changed = true
			}
		}
	}
	if open > 0 {
		fair := remaining / open
		for i := range parts {
			if !done[i] {
				share[i] = fair
			}
		}
	}
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		if trimmed := strings.TrimRightFunc(bytePrefix(part, share[i]), unicode.IsSpace); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, ExcerptSpanSeparator)
}

func bytePrefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
