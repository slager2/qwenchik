package pdfextract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

func fixturePDF(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/one-page.pdf")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != "880ecbfc21127fc178ea95cac9b11aa0173f8722c0548cbeb1fa01fc509d23dc" {
		t.Fatal("PDF fixture bytes differ from pinned hash")
	}
	return data
}

func TestCheckedInPDFFixtureUsesRealBoundedFallback(t *testing.T) {
	requirePDFExecutionPlatform(t)
	policy := DefaultPolicy()
	policy.AllowUnsandboxedFallback = true
	extractor, err := NewWithPolicy("", nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	if extractor.Capability(context.Background()).State != "pure_go_fallback_available" {
		t.Fatal("fallback capability not reported")
	}
	result, err := extractor.Extract(context.Background(), fixturePDF(t))
	if err != nil || result.PageCount != 1 || !strings.Contains(result.Text, "Qwenchik local PDF fixture.") {
		t.Fatalf("real fixture parse failed: %+v %v", result, err)
	}
	if result.Method != methodPureGo {
		t.Fatalf("unexpected extraction method %q", result.Method)
	}
}

func TestIsolatedPDFFixtureRuntimeSmoke(t *testing.T) {
	if os.Getenv("QAGENT_PDF_RUNTIME_SMOKE") != "1" {
		t.Skip("opt-in local rootless runtime smoke requires QAGENT_PDF_RUNTIME_SMOKE=1")
	}
	extractor, err := New(os.Getenv("PDF_EXTRACTOR_IMAGE"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractor.Check(context.Background()); err != nil {
		t.Fatalf("isolated PDF capability unavailable: %v", err)
	}
	result, err := extractor.Extract(context.Background(), fixturePDF(t))
	if err != nil || result.Method != methodPoppler || result.PageCount != 1 || !strings.Contains(result.Text, "Qwenchik local PDF fixture.") {
		t.Fatalf("isolated fixture parse failed: %+v %v", result, err)
	}
}

func TestPDFOutcomeClassificationKeepsPolicyAndCapabilitySeparate(t *testing.T){
	for _,test:=range []struct{Err error;Outcome string}{{nil,"extracted"},{ErrUnavailable,"unavailable"},{errors.Join(ErrUnavailable,ErrPolicyRejected),"policy_rejected"},{ErrInputTooLarge,"source_too_large"},{ErrTooManyPages,"extraction_failed"},{ErrTimeout,"extraction_failed"},{ErrResourceExceeded,"extraction_failed"}}{if got:=Outcome(test.Err);got!=test.Outcome{t.Fatalf("PDF outcome %v=%q want %q",test.Err,got,test.Outcome)}}
}
