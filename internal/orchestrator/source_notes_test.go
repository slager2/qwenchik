package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

func TestSourceNotesBoundContextAndRejectForeignOrForgedProvenance(t *testing.T) {
	evidence := []domain.Evidence{{ID: "e", TaskID: "owned", HTTPStatus: 200, CanonicalURL: "https://example.org/source", ContentSHA256: "raw-hash", RawArtifactID: "raw", TextArtifactID: "text", Excerpt: strings.Repeat("source excerpt ", 100_000)}}
	files := []domain.Artifact{{ID: "raw", TaskID: "owned", MIMEType: "application/pdf", SHA256: "raw-hash", SizeBytes: 40_000_000}, {ID: "text", TaskID: "owned", MIMEType: "text/plain", SHA256: "text-hash", SizeBytes: 2_000_000}}
	notes, err := buildSourceNotes("owned", []string{"e"}, evidence, files, nil, []string{"unresolved"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(compactSourceNotes(notes))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 4096 || len(notes) != 1 || notes[0].ContentHash == notes[0].ExtractedTextHash || notes[0].EvidenceID != "e" {
		t.Fatalf("raw corpus leaked or provenance lost: bytes=%d notes=%+v", len(raw), notes)
	}
	if _, err := buildSourceNotes("foreign", []string{"e"}, evidence, files, nil, nil); err == nil {
		t.Fatal("foreign evidence accepted")
	}
	if _, err := buildSourceNotes("owned", []string{"forged"}, evidence, files, nil, nil); err == nil {
		t.Fatal("forged evidence accepted")
	}
	if _, err := buildSourceNotes("owned", []string{"e", "e"}, evidence, files, nil, nil); err == nil {
		t.Fatal("duplicate source note identity accepted")
	}
	files[1].MIMEType = "text/plain-forged"
	if _, err := buildSourceNotes("owned", []string{"e"}, evidence, files, nil, nil); err == nil {
		t.Fatal("forged text MIME accepted")
	}
	files[1].MIMEType = "text/plain"
	files[1].TaskID = "foreign"
	if _, err := buildSourceNotes("owned", []string{"e"}, evidence, files, nil, nil); err == nil {
		t.Fatal("foreign extracted artifact accepted")
	}
	files[1].TaskID = "owned"
	files[0].SHA256 = "forged-hash"
	if _, err := buildSourceNotes("owned", []string{"e"}, evidence, files, nil, nil); err == nil {
		t.Fatal("mismatched artifact hash accepted")
	}
}
