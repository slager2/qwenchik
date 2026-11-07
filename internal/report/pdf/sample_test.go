package pdf

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRenderSamplePDF renders a report Markdown file into a PDF for visual
// checks: QWENCHIK_PDF_SAMPLE=<in.md>:<out.pdf>. Skipped by default.
func TestRenderSamplePDF(t *testing.T) {
	spec := os.Getenv("QWENCHIK_PDF_SAMPLE")
	if spec == "" {
		t.Skip("set QWENCHIK_PDF_SAMPLE=<in.md>:<out.pdf>")
	}
	var in, out string
	for index := len(spec) - 1; index >= 0; index-- {
		if spec[index] == ':' {
			in, out = spec[:index], spec[index+1:]
			break
		}
	}
	markdown, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := DetectChrome("")
	if err != nil {
		t.Skip(err)
	}
	ids := map[string]string{
		"3c757d00-1032-43dc-8b10-547adc82b579": "https://stipendiumhungaricum.hu/apply/",
		"8e37407c-9eec-4764-a66e-0680229bba13": "https://www.turkiyeburslari.gov.tr/en/page/prospective-students/scholarship-coverage",
		"1be322ed-9841-423c-ba3a-04965fc464fb": "https://www.studyinkorea.go.kr/gks/2027-undergraduate.pdf",
		"5c766154-6377-4f72-bd5d-e45213895e2b": "https://www.gov.kz/memleket/entities/edu/press/news/details/sh-2025",
	}
	sources := []Source{}
	// QWENCHIK_PDF_SAMPLE_SOURCES=<sources.json> ([{"Title","URL","EvidenceIDs"}])
	// replaces the built-in source list for reports of other runs.
	if path := os.Getenv("QWENCHIK_PDF_SAMPLE_SOURCES"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &sources); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"3c757d00-1032-43dc-8b10-547adc82b579", "8e37407c-9eec-4764-a66e-0680229bba13", "1be322ed-9841-423c-ba3a-04965fc464fb", "5c766154-6377-4f72-bd5d-e45213895e2b"} {
		if os.Getenv("QWENCHIK_PDF_SAMPLE_SOURCES") == "" {
			sources = append(sources, Source{Title: ids[id], URL: ids[id], EvidenceIDs: []string{id}})
		}
	}
	html, err := BuildHTML(Document{Markdown: string(markdown), Sources: sources, GeneratedAt: time.Now(), Outcome: "partial"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := NewChrome(binary).Render(context.Background(), html)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
