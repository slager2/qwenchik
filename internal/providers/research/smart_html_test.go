package research

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func extractHTMLFixture(t *testing.T, fixture string, limit int) ExtractedDocument {
	t.Helper()
	result, err := NewHTMLExtractorWithPDF(limit, nil).Extract(context.Background(), FetchedDocument{
		ContentType: "text/html", ContentSHA256: "fixture-sha", Body: []byte(fixture),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Taint != TaintExternal || result.ContentSHA256 != "fixture-sha" || result.Method != "local_html_parser" {
		t.Fatalf("missing external provenance: %#v", result)
	}
	if utf8.RuneCountInString(result.Text) > limit {
		t.Fatalf("output exceeds %d characters: %q", limit, result.Text)
	}
	return result
}

func TestSmartHTMLStructuredArticlesArePrimary(t *testing.T) {
	body := strings.Repeat("Verified article evidence with measured results. ", 9)
	for _, kind := range []string{"Article", "NewsArticle", "TechArticle", "BlogPosting"} {
		t.Run(kind, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"@context": "https://schema.org", "@graph": []any{
				map[string]any{"@type": "SiteNavigationElement", "name": "JSON navigation poison"},
				map[string]any{"@type": []string{"Thing", "https://schema.org/" + kind}, "headline": "Structured headline", "articleBody": body,
					"author": []any{map[string]any{"@type": "Person", "name": "Ada Example"}}, "datePublished": "2026-01-02", "dateModified": "2026-02-03"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			fixture := `<html><head><title>Generic site title</title><script type="application/ld+json">` + string(data) + `</script></head><body><nav>DOM navigation poison</nav><main><p>DOM teaser must lose to full article.</p></main></body></html>`
			result := extractHTMLFixture(t, fixture, 2000)
			if result.Title != "Structured headline" {
				t.Fatalf("title = %q", result.Title)
			}
			for _, want := range []string{strings.TrimSpace(body), "Ada Example", "2026-01-02", "2026-02-03"} {
				if !strings.Contains(result.Text, want) {
					t.Errorf("missing %q in %q", want, result.Text)
				}
			}
			for _, unwanted := range []string{"navigation poison", "DOM teaser", "schema.org"} {
				if strings.Contains(result.Text, unwanted) {
					t.Errorf("unexpected %q in %q", unwanted, result.Text)
				}
			}
		})
	}
}

func TestSmartHTMLProductAndFAQProperties(t *testing.T) {
	for _, fixture := range []struct {
		name string
		json string
		want []string
	}{
		{"product", `{"@type":"Product","name":"Sample instrument","description":"Precision measuring instrument.","brand":{"name":"Example Labs"},"sku":"SKU-42","offers":{"price":42.5,"priceCurrency":"EUR","availability":"InStock"},"aggregateRating":{"ratingValue":4.8,"reviewCount":12},"additionalProperty":{"name":"Weight","value":"2 kg"}}`, []string{"Sample instrument", "Precision measuring", "Example Labs", "SKU-42", "42.5", "EUR", "InStock", "4.8", "12", "Weight", "2 kg"}},
		{"faq", `{"@type":"FAQPage","name":"Useful answers","mainEntity":[{"@type":"Question","name":"How does it work?","acceptedAnswer":{"@type":"Answer","text":"<p>It uses local fixtures.</p>\u003cscript>evil()\u003c/script>"}},{"@type":"Question","name":"Where?","acceptedAnswer":{"text":"On this machine."}}]}`, []string{"Useful answers", "How does it work?", "It uses local fixtures.", "Where?", "On this machine."}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result := extractHTMLFixture(t, `<script type="application/ld+json">`+fixture.json+`</script><nav>Menu poison</nav>`, 2000)
			for _, want := range fixture.want {
				if !strings.Contains(result.Text, want) {
					t.Errorf("missing %q in %q", want, result.Text)
				}
			}
			if strings.Contains(result.Text, "evil") || strings.Contains(result.Text, "poison") {
				t.Fatalf("unwanted content: %q", result.Text)
			}
		})
	}
}

func TestSmartHTMLNextHydration(t *testing.T) {
	body := strings.Repeat("The server supplied complete article text. ", 10)
	data, _ := json.Marshal(map[string]any{"props": map[string]any{"pageProps": map[string]any{
		"article":         map[string]any{"title": "Next article", "content": "<h2>Evidence</h2><p>" + body + "</p><ul><li>First finding</li><li>Second finding</li></ul><aside>Embedded sidebar poison</aside>"},
		"navigation":      map[string]any{"title": "Navigation poison", "text": "Menu poison"},
		"recommendations": []any{map[string]any{"title": "Recommendation poison", "text": "Ignore this"}},
	}, "buildId": "Internal build poison"}})
	result := extractHTMLFixture(t, `<title>Site</title><script id="__NEXT_DATA__" type="application/json">`+string(data)+`</script><main>Loading placeholder.</main>`, 2000)
	if result.Title != "Next article" || !strings.Contains(result.Text, strings.TrimSpace(body)) || !strings.Contains(result.Text, "Evidence\n") || !strings.Contains(result.Text, "First finding\nSecond finding") {
		t.Fatalf("unexpected Next extraction: %#v", result)
	}
	for _, bad := range []string{"poison", "Ignore this", "Loading placeholder"} {
		if strings.Contains(result.Text, bad) {
			t.Errorf("unexpected %q in %q", bad, result.Text)
		}
	}
}

func TestSmartHTMLNuxtJSONOnly(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		script string
		want   string
	}{
		{"data object", `<script id="__NUXT_DATA__" type="application/json">{"data":[{"title":"Nuxt title","text":"Nuxt JSON evidence."}]}</script>`, "Nuxt JSON evidence."},
		{"indexed data", `<script id="__NUXT_DATA__" type="application/json">[{"data":1}, {"article":2}, {"title":3,"body":4}, "Indexed title", "Indexed Nuxt evidence."]</script>`, "Indexed Nuxt evidence."},
		{"indexed noise exclusion", `<script id="__NUXT_DATA__" type="application/json">[["Reactive",1],{"article":2,"navigation":5},{"title":3,"body":4},"Indexed title","Indexed Nuxt evidence.",{"title":6,"text":7},"Navigation poison","Menu poison"]</script>`, "Indexed Nuxt evidence."},
		{"strict assignment", `<script>window.__NUXT__ = {"data":[{"title":"Nuxt title","body":"Strict assignment evidence."}]};</script>`, "Strict assignment evidence."},
		{"function", `<script>window.__NUXT__ = (function(){throw new Error("Executable poison"); return {"text":"Wrong"}})();</script>`, "DOM fallback evidence."},
		{"unquoted keys", `<script>window.__NUXT__ = {text:"Executable poison"};</script>`, "DOM fallback evidence."},
		{"trailing statement", `<script>window.__NUXT__ = {"text":"Executable poison"}; globalThis.run();</script>`, "DOM fallback evidence."},
		{"trailing json", `<script>window.__NUXT__ = {"text":"Executable poison"} {"text":"More poison"};</script>`, "DOM fallback evidence."},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result := extractHTMLFixture(t, fixture.script+`<nav>Menu poison</nav><div>DOM fallback evidence.</div>`, 1000)
			if !strings.Contains(result.Text, fixture.want) || strings.Contains(result.Text, "poison") {
				t.Fatalf("unexpected Nuxt extraction: %#v", result)
			}
		})
	}
}

func TestSmartHTMLShortHydrationReplacesPlaceholder(t *testing.T) {
	result := extractHTMLFixture(t, `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"article":{"title":"Hydrated title","body":"Actual complete evidence from the server state."}}}}</script><main>Loading...</main>`, 1000)
	if result.Title != "Hydrated title" || !strings.Contains(result.Text, "Actual complete evidence") || strings.Contains(result.Text, "Loading") {
		t.Fatalf("short hydration lost to placeholder: %#v", result)
	}
}

func TestSmartHTMLScopedDOM(t *testing.T) {
	for _, scope := range []string{"article", "main", `div id="content"`, `div class="post-content"`, `div class="article-body"`} {
		t.Run(scope, func(t *testing.T) {
			tag := strings.Fields(scope)[0]
			fixture := `<head><title>Document title</title></head><body><div>Unscoped poison</div><` + scope + `><h1>Heading</h1><p>Hello <strong>world</strong>.</p><ul><li>One</li><li>Two</li></ul><p>Last<br>line</p><header>Header poison</header><footer>Footer poison</footer><aside>Aside poison</aside><div class="sidebar">Sidebar poison</div><div id="related-posts">Related poison</div><section class="recommendations">Recommendations poison</section><div class="ad-slot">Ad poison</div><span aria-hidden="true">Hidden poison</span><span hidden>Hidden attribute poison</span><script>Script poison</script><style>Style poison</style></` + tag + `><nav>Nav poison</nav></body>`
			result := extractHTMLFixture(t, fixture, 1000)
			if result.Title != "Document title" || result.Text != "Heading\nHello world.\nOne\nTwo\nLast\nline" {
				t.Fatalf("scoped extraction = %#v", result)
			}
		})
	}
}

func TestSmartHTMLMetadataFallback(t *testing.T) {
	result := extractHTMLFixture(t, `<title>Generic title</title><meta property="og:title" content="OG title"><meta name="description" content="Generic description"><meta property="og:description" content="OG evidence &amp; facts"><script>untrusted()</script><nav>Menu</nav>`, 1000)
	if result.Title != "OG title" || result.Text != "OG evidence & facts" {
		t.Fatalf("OG fallback = %#v", result)
	}
	result = extractHTMLFixture(t, `<title>Plain title</title><meta name="description" content="Plain description">`, 1000)
	if result.Title != "Plain title" || result.Text != "Plain description" {
		t.Fatalf("meta fallback = %#v", result)
	}
}

func TestSmartHTMLRejectsUnboundedJSON(t *testing.T) {
	for _, fixture := range []struct {
		name string
		json string
	}{
		{"deep", strings.Repeat(`{"child":`, smartJSONDepth+1) + `{"text":"Deep poison"}` + strings.Repeat("}", smartJSONDepth+1)},
		{"large", `{"text":"` + strings.Repeat("Large poison ", smartJSONBytes/10) + `"}`},
		{"many nodes", `{"items":[` + strings.Repeat(`{"text":"Node poison"},`, smartJSONNodes) + `null]}`},
		{"malformed", `{"text":"Malformed poison",}`},
		{"prototype", `{"__proto__":{"polluted":"Prototype poison"},"constructor":{"prototype":{"polluted":"Constructor poison"}}}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result := extractHTMLFixture(t, `<script id="__NEXT_DATA__" type="application/json">`+fixture.json+`</script><main>Safe DOM evidence.</main>`, 1000)
			if result.Text != "Safe DOM evidence." {
				t.Fatalf("unsafe JSON affected extraction: %#v", result)
			}
		})
	}
	result := extractHTMLFixture(t, `<script id="__NUXT_DATA__" type="application/json">[{"text":0}]</script><main>Cycle fallback.</main>`, 1000)
	if result.Text != "Cycle fallback." {
		t.Fatalf("cyclic reference affected extraction: %#v", result)
	}
}

func TestSmartHTMLSentenceAndRuneLimits(t *testing.T) {
	result := extractHTMLFixture(t, `<article><p>First complete sentence. Second sentence is much too long to fit within the limit.</p></article>`, 40)
	if result.Text != "First complete sentence." {
		t.Fatalf("sentence trim = %q", result.Text)
	}
	result = extractHTMLFixture(t, `<main><h1>Факты</h1><p>`+strings.Repeat("界", 100)+`</p></main>`, 20)
	if !utf8.ValidString(result.Text) || utf8.RuneCountInString(result.Text) > 20 || !strings.HasPrefix(result.Text, "Факты\n") {
		t.Fatalf("invalid bounded Unicode output: %q", result.Text)
	}
}
