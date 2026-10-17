package research

import (
	"encoding/json"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	smartJSONBytes = 1 << 20
	smartJSONNodes = 20000
	smartJSONDepth = 48
	smartTextLimit = 400000
)

type htmlCandidate struct {
	title string
	text  string
	body  int
}

func extractSmartHTML(root *html.Node, maxChars int) (string, string) {
	var title, ogTitle, description, ogDescription string
	var structured, article, hydration htmlCandidate
	var scopes [3][]*html.Node
	budget := 4 * smartJSONBytes
	scripts := 0
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if title == "" {
					title = nodeText(n, 500, false)
				}
			case "meta":
				key := strings.ToLower(firstNonempty(nodeAttr(n, "property"), nodeAttr(n, "name")))
				value := nodeAttr(n, "content")
				switch key {
				case "og:title":
					ogTitle = value
				case "og:description":
					ogDescription = value
				case "description":
					description = value
				}
			case "script":
				kind := strings.ToLower(strings.TrimSpace(strings.Split(nodeAttr(n, "type"), ";")[0]))
				id := nodeAttr(n, "id")
				if scripts < 32 && budget > 0 && n.FirstChild != nil {
					raw := strings.TrimSpace(n.FirstChild.Data)
					isLD := kind == "application/ld+json"
					isState := id == "__NEXT_DATA__" || id == "__NUXT_DATA__"
					if !isLD && !isState && strings.HasPrefix(raw, "window.__NUXT__") {
						rest := strings.TrimSpace(strings.TrimPrefix(raw, "window.__NUXT__"))
						if strings.HasPrefix(rest, "=") {
							raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest[1:]), ";"))
							isState = strings.HasPrefix(raw, "{")
						}
					}
					if isLD || isState {
						scripts++
						budget -= len(raw)
						if budget >= 0 {
							if value, ok := boundedJSON(raw); ok {
								if isLD {
									collectStructured(value, &structured, &article)
								} else {
									candidate := hydrationCandidate(value, id == "__NUXT_DATA__")
									if len(candidate.text) > len(hydration.text) {
										hydration = candidate
									}
								}
							}
						}
					}
				}
				continue
			}
			if n.Data != "head" && discardHTMLNode(n) {
				continue
			}
			rank := -1
			switch {
			case n.Data == "article":
				rank = 0
			case n.Data == "main" || nodeAttr(n, "role") == "main":
				rank = 1
			case nodeAttr(n, "id") == "content" || hasClass(n, "post-content") || hasClass(n, "article-body"):
				rank = 2
			}
			if rank >= 0 && len(scopes[rank]) < 64 {
				scopes[rank] = append(scopes[rank], n)
			}
		}
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, c)
		}
	}
	limit := smartTextLimit
	if maxChars < limit/2 {
		limit = maxChars*2 + 1000
	}
	var dom string
	for _, group := range scopes {
		for _, n := range group {
			text := nodeText(n, limit, true)
			if len(text) > len(dom) {
				dom = text
			}
		}
		if dom != "" {
			break
		}
	}
	chosen := htmlCandidate{}
	switch {
	case article.body > 300:
		chosen = article
	case hydration.body > 0 && (hydration.body > 300 || hydration.body > utf8.RuneCountInString(dom)):
		chosen = hydration
	case dom != "":
		chosen.text = dom
	case structured.text != "":
		chosen = structured
	case hydration.text != "":
		chosen = hydration
	default:
		chosen.text = nodeText(root, limit, true)
	}
	if chosen.text == "" {
		chosen.text = firstNonempty(ogDescription, description)
	}
	return trimRunes(strings.Join(strings.Fields(firstNonempty(chosen.title, ogTitle, title, structured.title, hydration.title)), " "), 500), sentenceTrim(chosen.text, maxChars)
}

func nodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, name string) bool {
	for _, class := range strings.Fields(nodeAttr(n, "class")) {
		if strings.EqualFold(class, name) {
			return true
		}
	}
	return false
}

func discardHTMLNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if isDiscardedTag(n.Data) || strings.EqualFold(nodeAttr(n, "aria-hidden"), "true") {
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" {
			return true
		}
	}
	for _, role := range strings.Fields(strings.ToLower(nodeAttr(n, "role"))) {
		if role == "navigation" || role == "complementary" || role == "banner" || role == "contentinfo" {
			return true
		}
	}
	parts := strings.FieldsFunc(strings.ToLower(nodeAttr(n, "id")+" "+nodeAttr(n, "class")), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, part := range parts {
		switch part {
		case "sidebar", "related", "recommendation", "recommendations", "ad", "ads", "advert", "advertisement", "advertising", "navigation":
			return true
		}
	}
	return false
}

func nodeText(root *html.Node, limit int, filter bool) string {
	var b strings.Builder
	type entry struct {
		n    *html.Node
		exit bool
	}
	stack := []entry{{n: root}}
	for len(stack) > 0 && b.Len() < limit*4 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := e.n
		if filter && discardHTMLNode(n) {
			continue
		}
		if n.Type == html.ElementNode && isBlockTag(n.Data) {
			b.WriteByte('\n')
		}
		if e.exit {
			continue
		}
		if n.Type == html.TextNode {
			remaining := limit*4 - b.Len()
			text := n.Data
			if len(text) > remaining {
				text = safeBytePrefix(text, remaining)
			}
			b.WriteString(text)
		}
		if n.Type == html.ElementNode && isBlockTag(n.Data) {
			stack = append(stack, entry{n: n, exit: true})
		}
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, entry{n: c})
		}
	}
	return normalizeExtractedText(b.String(), limit)
}

func boundedJSON(raw string) (any, bool) {
	if len(raw) > smartJSONBytes {
		return nil, false
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	depth, nodes := 0, 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		nodes++
		if nodes > smartJSONNodes {
			return nil, false
		}
		if delim, ok := token.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				depth++
			} else {
				depth--
			}
			if depth > smartJSONDepth {
				return nil, false
			}
		}
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return nil, false
	}
	return value, true
}

func sortedJSONKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cleanJSONText(value string) string {
	if len(value) > smartTextLimit {
		value = safeBytePrefix(value, smartTextLimit)
	}
	if strings.Contains(value, "<") {
		if root, err := html.Parse(strings.NewReader(value)); err == nil {
			return nodeText(root, smartTextLimit, true)
		}
	}
	return normalizeExtractedText(value, smartTextLimit)
}

func safeBytePrefix(value string, limit int) string {
	for limit > 0 && limit < len(value) && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func jsonFieldText(value any, pool []any, depth int, budget *int) string {
	if depth > smartJSONDepth || *budget <= 0 {
		return ""
	}
	*budget--
	switch v := value.(type) {
	case string:
		return cleanJSONText(v)
	case float64:
		if pool != nil && v >= 0 && v < float64(len(pool)) && v == float64(int(v)) {
			return jsonFieldText(pool[int(v)], pool, depth+1, budget)
		}
		if pool == nil {
			encoded, _ := json.Marshal(v)
			return string(encoded)
		}
	case []any:
		var parts []string
		size := 0
		for _, child := range v {
			part := jsonFieldText(child, pool, depth+1, budget)
			parts = append(parts, part)
			size += len(part)
			if size >= smartTextLimit || *budget <= 0 {
				break
			}
		}
		return normalizeExtractedText(strings.Join(parts, "\n"), smartTextLimit)
	case map[string]any:
		var parts []string
		size := 0
		for _, key := range []string{"headline", "name", "articleBody", "description", "text", "value", "acceptedAnswer", "suggestedAnswer", "price", "priceCurrency", "availability", "ratingValue", "reviewCount", "ratingCount"} {
			if child, ok := v[key]; ok {
				part := jsonFieldText(child, pool, depth+1, budget)
				if part != "" {
					parts = append(parts, part)
					size += len(part)
				}
				if size >= smartTextLimit || *budget <= 0 {
					break
				}
			}
		}
		return normalizeExtractedText(strings.Join(parts, "\n"), smartTextLimit)
	}
	return ""
}

func collectStructured(value any, best, article *htmlCandidate) {
	budget := smartJSONNodes
	fieldBudget := smartJSONNodes
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > smartJSONDepth || budget <= 0 {
			return
		}
		budget--
		switch v := value.(type) {
		case []any:
			for _, child := range v {
				walk(child, depth+1)
			}
		case map[string]any:
			types := strings.Fields(strings.ReplaceAll(jsonFieldText(v["@type"], nil, 0, &fieldBudget), "\n", " "))
			isArticle, supported := false, false
			for _, kind := range types {
				if i := strings.LastIndexAny(kind, "/#"); i >= 0 {
					kind = kind[i+1:]
				}
				switch kind {
				case "Article", "NewsArticle", "TechArticle", "BlogPosting":
					isArticle, supported = true, true
				case "Product", "FAQPage":
					supported = true
				}
			}
			if supported {
				field := func(key string) string { return jsonFieldText(v[key], nil, 0, &fieldBudget) }
				candidate := htmlCandidate{title: firstNonempty(field("headline"), field("name"))}
				body := firstNonempty(field("articleBody"), field("text"), field("description"))
				candidate.body = utf8.RuneCountInString(body)
				parts := []string{candidate.title, body}
				for _, key := range []string{"author", "datePublished", "dateModified", "publisher", "brand", "sku", "mpn", "gtin", "category", "offers", "aggregateRating", "mainEntity", "additionalProperty"} {
					if text := field(key); text != "" {
						parts = append(parts, key+": "+text)
					}
				}
				candidate.text = normalizeExtractedText(strings.Join(parts, "\n"), smartTextLimit)
				if len(candidate.text) > len(best.text) {
					*best = candidate
				}
				if isArticle && candidate.body > article.body {
					*article = candidate
				}
			}
			for _, key := range sortedJSONKeys(v) {
				walk(v[key], depth+1)
			}
		}
	}
	walk(value, 0)
}

func hydrationNoise(key string) bool {
	switch strings.ToLower(key) {
	case "nav", "navigation", "menu", "menus", "header", "footer", "sidebar", "related", "relatedarticles", "recommendations", "ads", "advertisements", "scripts", "styles", "config", "runtimeconfig":
		return true
	}
	return false
}

func hydrationCandidate(value any, indexed bool) htmlCandidate {
	var pool []any
	if indexed {
		pool, _ = value.([]any)
	}
	budget := smartJSONNodes
	fieldBudget := smartJSONNodes
	var parts []string
	size := 0
	seen := map[string]bool{}
	visited := map[int]bool{}
	candidate := htmlCandidate{}
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > smartJSONDepth || budget <= 0 || size >= smartTextLimit {
			return
		}
		budget--
		switch v := value.(type) {
		case float64:
			if pool != nil && v >= 0 && v < float64(len(pool)) && v == float64(int(v)) && !visited[int(v)] {
				visited[int(v)] = true
				walk(pool[int(v)], depth+1)
			}
		case []any:
			for _, child := range v {
				walk(child, depth+1)
			}
		case map[string]any:
			for _, key := range []string{"headline", "title", "name", "articleBody", "body", "content", "description", "text", "summary", "excerpt", "author", "datePublished", "dateModified", "publishedAt"} {
				child, ok := v[key]
				if !ok {
					continue
				}
				text := jsonFieldText(child, pool, 0, &fieldBudget)
				if text == "" || seen[text] {
					continue
				}
				seen[text] = true
				if key == "headline" || key == "title" || key == "name" {
					if candidate.title == "" {
						candidate.title = text
					}
				} else if key != "author" && key != "datePublished" && key != "dateModified" && key != "publishedAt" {
					candidate.body += utf8.RuneCountInString(text)
				}
				parts = append(parts, text)
				size += len(text)
				if size >= smartTextLimit {
					return
				}
			}
			for _, key := range sortedJSONKeys(v) {
				if !hydrationNoise(key) {
					walk(v[key], depth+1)
				}
			}
		}
	}
	if len(pool) > 0 {
		visited[0] = true
		walk(pool[0], 0)
	} else {
		walk(value, 0)
	}
	candidate.text = normalizeExtractedText(strings.Join(parts, "\n"), smartTextLimit)
	return candidate
}

func sentenceTrim(text string, limit int) string {
	text = normalizeExtractedText(text, smartTextLimit)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	for i := limit - 1; i >= limit/2; i-- {
		if strings.ContainsRune("。！？", runes[i]) || strings.ContainsRune(".!?", runes[i]) && (i+1 == len(runes) || unicode.IsSpace(runes[i+1])) {
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	for i := limit - 1; i >= limit*3/4; i-- {
		if unicode.IsSpace(runes[i]) {
			return strings.TrimSpace(string(runes[:i]))
		}
	}
	return trimRunes(text, limit)
}
