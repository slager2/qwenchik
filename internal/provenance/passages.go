package provenance

import (
	"strings"
	"unicode"
)

const (
	// passageShingleWords is the word n-gram length used to find search
	// provider passages inside the fetched extracted text. Four words are
	// long enough to be specific and short enough to survive differences
	// between the provider's and the backend's text extraction.
	passageShingleWords = 4
	// passageMinMatchedWords is the least number of extracted-text words that
	// must belong to a shared n-gram (three overlapping n-grams) before the
	// passages locate the excerpt.
	passageMinMatchedWords = passageShingleWords + 2
	// passageContextRunes is the context kept around the matched passages.
	passageContextRunes = 400
)

type passageWord struct {
	start, end int // rune offsets in the source text
	text       string
}

func passageWords(text string) []passageWord {
	words := []passageWord{}
	start := -1
	var current strings.Builder
	offset := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if start < 0 {
				start = offset
				current.Reset()
			}
			current.WriteRune(unicode.ToLower(r))
		} else if start >= 0 {
			words = append(words, passageWord{start: start, end: offset, text: current.String()})
			start = -1
		}
		offset++
	}
	if start >= 0 {
		words = append(words, passageWord{start: start, end: offset, text: current.String()})
	}
	return words
}

func passageShingle(words []passageWord, index int) string {
	parts := make([]string, passageShingleWords)
	for i := range parts {
		parts[i] = words[index+i].text
	}
	return strings.Join(parts, "\x00")
}

// SelectPassageExcerptWithSpan locates search-provider passages (for example
// agentSearcher's reranked chunks of the same page) inside the fetched
// extracted text and returns the densest matching region as a verbatim span
// with rune offsets. Matching is by shared word n-grams, so whitespace,
// punctuation and case differences between extractors do not matter, while
// the returned excerpt is always an exact slice of text. It returns ok=false
// when the passages do not occur in the text, so the caller can fall back to
// the lexical selector.
func SelectPassageExcerptWithSpan(text string, passages []string, maxRunes int) (excerpt string, start, end int, ok bool) {
	if maxRunes < 1 || strings.TrimSpace(text) == "" || len(passages) == 0 {
		return "", 0, 0, false
	}
	shingles := map[string]struct{}{}
	for _, passage := range passages {
		words := passageWords(passage)
		for i := 0; i+passageShingleWords <= len(words); i++ {
			shingles[passageShingle(words, i)] = struct{}{}
		}
	}
	if len(shingles) == 0 {
		return "", 0, 0, false
	}
	words := passageWords(text)
	covered := make([]bool, len(words))
	for i := 0; i+passageShingleWords <= len(words); i++ {
		if _, match := shingles[passageShingle(words, i)]; match {
			for j := i; j < i+passageShingleWords; j++ {
				covered[j] = true
			}
		}
	}
	// Slide a window of at most maxRunes over the words and keep the one with
	// the most covered words; ties keep the earliest window.
	bestFirst, bestLast, bestCount := -1, -1, 0
	count, left := 0, 0
	for right := range words {
		if covered[right] {
			count++
		}
		for words[right].end-words[left].start > maxRunes {
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
		return "", 0, 0, false
	}
	runes := []rune(text)
	start, end = words[bestFirst].start, words[bestLast].end
	// Keep bounded context around the matched passages, starting at a line
	// or sentence boundary when one is close.
	spare := max(0, maxRunes-(end-start))
	contextBefore := min(passageContextRunes, spare/2)
	contextAfter := min(passageContextRunes, spare-contextBefore)
	from := max(0, start-contextBefore)
	for position := start - 1; position >= from; position-- {
		if runes[position] == '\n' || (position > 0 && strings.ContainsRune(".!?", runes[position-1]) && unicode.IsSpace(runes[position])) {
			start = position + 1
			break
		}
	}
	limit := min(len(runes), end+contextAfter)
	for position := end; position < limit; position++ {
		if runes[position] == '\n' {
			break
		}
		end = position + 1
		if strings.ContainsRune(".!?", runes[position]) {
			break
		}
	}
	for start < end && unicode.IsSpace(runes[start]) {
		start++
	}
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	if end <= start {
		return "", 0, 0, false
	}
	return string(runes[start:end]), start, end, true
}
