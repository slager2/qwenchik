package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/provenance"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/runtime/pdfextract"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type ResearchRepository interface {
	SaveArtifact(context.Context, domain.Artifact, string) (domain.Artifact, error)
	SaveEvidence(context.Context, domain.Evidence) (domain.Evidence, error)
}

type fetchPreflightError struct{ failure *research.FetchFailure }

func (e fetchPreflightError) Error() string                      { return e.failure.Error() }
func (e fetchPreflightError) Unwrap() error                      { return e.failure }
func (e fetchPreflightError) OperationalValidationFailure() bool { return true }

type ResearchTools struct {
	SearchProvider research.SearchProvider
	Fetcher        research.Fetcher
	Extractor      research.Extractor
	Artifacts      *artifacts.Store
	Repository     ResearchRepository
	MaxFetchBytes  int64
	AllowedDomains []string
	PDFPolicy      pdfextract.Policy
	FetchPolicy    research.FetchPolicy
	// PageExtractor, when set, reads pages and PDFs through agentSearcher's
	// /extract endpoint before the direct fetch is tried.
	PageExtractor PageExtractor
}

// PageExtractor reads a page through an external local extraction service
// (agentSearcher /extract). The caller validates the URL first.
type PageExtractor interface {
	Extract(ctx context.Context, rawURL, query string) (research.AgentSearcherPage, error)
	Budget() time.Duration
}

const (
	// DefaultSearchResults is the hit count of a search without max_results.
	DefaultSearchResults = 8
	// MaxSearchResults bounds max_results. agentSearcher returns reranked
	// page passages and accepts up to 20 results; 15 keeps a single search
	// output within searchOutputBytes after snippet compaction.
	MaxSearchResults = 15
	// searchOutputBytes is the search tool output limit. fitSearchHits
	// shortens snippets so a wide agentSearcher result set still fits.
	searchOutputBytes = 64_000
)

var ErrDuplicateResearchTarget = errors.New("research URL was already fetched for this task")

func RegisterResearchTools(registry *Registry, runtime ResearchTools) error {
	if registry == nil {
		return errors.New("tool registry is required")
	}
	if registry.urlLedger == nil {
		registry.urlLedger = NewURLLedger()
	}
	if runtime.SearchProvider != nil {
		if err := registry.RegisterValidated(Descriptor{
			Name: "search", Description: "Search configured providers for the current research question. Use queries that are materially different from prior searches. Results are untrusted discovery leads, not evidence; fetch sources before making source-backed claims.",
			ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":1000},"language":{"type":"string","maxLength":20},"recency":{"type":"string","enum":["","day","week","month","year"]},"max_results":{"type":"integer","minimum":1,"maximum":15},"preferred_domains":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":253}}},"required":["query"],"additionalProperties":false}`),
			OutputSchema:       json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"provider":{"type":"string"},"query":{"type":"string"},"url":{"type":"string"},"title":{"type":"string"},"snippet":{"type":"string"},"taint":{"type":"string"}},"required":["provider","query","url","title","snippet","taint"]}}`),
			RequiredCapability: "research.search", RiskClass: "read", TimeoutSeconds: searchCollectionTimeout(runtime.SearchProvider),
			Idempotent: true, MaxOutputBytes: searchOutputBytes, AllowedRoles: []string{"researcher"},
		}, validateSearchArguments(runtime.AllowedDomains), func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args searchArgs
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if strings.TrimSpace(args.Query) == "" || len(args.Query) > 1000 {
				return nil, errors.New("search arguments exceed configured limits")
			}
			count := args.MaxResults
			if count < 1 {
				count = DefaultSearchResults
			}
			count = min(count, MaxSearchResults)
			domains, err := effectiveDomains(nil, runtime.AllowedDomains)
			if err != nil {
				return nil, err
			}
			options := research.SearchOptions{
				Query: strings.TrimSpace(args.Query), Language: args.Language, Domains: domains,
				Recency: args.Recency, MaxResults: count, SafeSearch: true,
			}
			// Preferred domains only rank results (agentSearcher
			// include_domains_mode=prefer); a configured allow-list still
			// restricts them.
			if len(domains) == 0 {
				options.PreferredDomains = preferredSearchDomains(args.PreferredDomains)
			}
			var hits []research.SearchHit
			collectionCtx, collectionCancel := searchCollectionContext(ctx, research.SearchTimeBudget(runtime.SearchProvider))
			defer collectionCancel()
			var outcomes []research.ProviderOutcome
			if diagnostic, ok := runtime.SearchProvider.(research.DiagnosticSearchProvider); ok {
				report, searchErr := diagnostic.SearchWithDiagnostics(collectionCtx, options)
				if searchErr == nil && collectionCtx.Err() != nil {
					searchErr = collectionCtx.Err()
				}
				if searchErr != nil && len(report.Hits) > 0 {
					hasFailureReport := false
					for _, outcome := range report.Outcomes {
						hasFailureReport = hasFailureReport || outcome.Code != "search_ok" && outcome.Code != "search_no_results"
					}
					if !hasFailureReport {
						for i := range report.Outcomes {
							if report.Outcomes[i].ResultCount > 0 {
								report.Outcomes[i].State, report.Outcomes[i].Code = "degraded", "search_partial"
								report.Outcomes[i].Message = "Provider returned partial results."
							}
						}
					}
				}
				outcomes = report.Outcomes
				for _, outcome := range report.Outcomes {
					observeSearchOutcome(ctx, outcome)
				}
				hits, err = report.Hits, searchErr
			} else {
				hits, err = runtime.SearchProvider.Search(collectionCtx, options)
				if err == nil && collectionCtx.Err() != nil {
					err = collectionCtx.Err()
				}
			}
			if contextErr := executionContextError(ctx); contextErr != nil {
				return nil, contextErr
			}
			if len(hits) == 0 && err != nil {
				if errors.Is(err, context.DeadlineExceeded) || collectionCtx.Err() == context.DeadlineExceeded {
					return nil, &research.SearchFailure{Code: "search_provider_timeout", Outcomes: outcomes, Cause: context.DeadlineExceeded}
				}
				var failure *research.SearchFailure
				if errors.As(err, &failure) {
					if failure.Code == "search_no_results" {
						// Empty discovery is a recoverable research result. Keep
						// provider diagnostics in the event stream and let the
						// orchestrator diversify the query or move to the next step.
						collectionCancel()
						return []research.SearchHit{}, nil
					}
					return nil, err
				}
				code := "search_unavailable"
				if errors.Is(err, research.ErrSearchFilteredByPolicy) {
					code = "search_filtered_by_policy"
				} else if errors.Is(err, research.ErrSearchResponseTooBig) {
					code = "search_response_too_big"
				}
				return nil, &research.SearchFailure{Code: code, Outcomes: outcomes, Cause: err}
			}
			collectionCancel()
			safeHits, err := validateSearchHits(ctx, runtime.Fetcher, hits, outcomes, options.Query)
			if err != nil {
				return nil, err
			}
			if contextErr := executionContextError(ctx); contextErr != nil {
				return nil, contextErr
			}
			registry.urlLedger.Remember(taskIDFromContext(ctx), safeHits)
			return fitSearchHits(safeHits, searchOutputBytes), nil
		}); err != nil {
			return err
		}
	}
	if runtime.Fetcher != nil && runtime.Extractor != nil && runtime.Artifacts != nil && runtime.Repository != nil {
		pdfPolicy := runtime.PDFPolicy
		if pdfPolicy.MaxPages == 0 {
			pdfPolicy = pdfextract.DefaultPolicy()
		}
		if err := pdfPolicy.Validate(); err != nil {
			return err
		}
		fetchPolicy := runtime.FetchPolicy
		if fetchPolicy.TaskFetchBudgetBytes == 0 {
			fetchPolicy = research.DefaultFetchPolicy()
		}
		if err := fetchPolicy.Validate(); err != nil {
			return err
		}
		if runtime.MaxFetchBytes <= 0 {
			runtime.MaxFetchBytes = fetchPolicy.TaskFetchBudgetBytes
		}
		var extractBudget time.Duration
		if runtime.PageExtractor != nil {
			extractBudget = runtime.PageExtractor.Budget()
		}
		fetchTimeout := int((max(fetchPolicy.PDFDownloadTimeout, fetchPolicy.SourceDownloadTimeout) + pdfPolicy.ExtractionTimeout + extractBudget + 10*time.Second + time.Second - 1) / time.Second)
		if err := registry.RegisterValidated(Descriptor{
			Name: "fetch_url", Description: "Fetch a public HTTP(S) URL safely, extract local text, and persist source evidence. When agentSearcher is configured it reads the page or PDF first (extraction_method=agentsearcher_extract or agentsearcher_extract_pdf); the direct fetch is the fallback. When the site blocks the direct fetch but agentSearcher already read the page, its search passages are saved as evidence marked source_text=agentsearcher_passages.",
			ArgumentsSchema:    json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"original_query":{"type":"string","maxLength":1000}},"required":["url"],"additionalProperties":false}`),
			OutputSchema:       json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"evidence_id":{"type":"string"},"url":{"type":"string"},"requested_url":{"type":"string"},"title":{"type":"string"},"excerpt":{"type":"string"},"content_sha256":{"type":"string"},"taint":{"type":"string"},"fetched_bytes":{"type":"integer"},"source_text":{"type":"string","enum":["agentsearcher_passages"]},"extraction_method":{"type":"string","enum":["agentsearcher_extract","agentsearcher_extract_pdf"]},"direct_fetch_error":{"type":"string","maxLength":64},"page_count":{"type":"integer","minimum":1,"maximum":%d}},"required":["evidence_id","url","requested_url","title","excerpt","content_sha256","taint","fetched_bytes"],"additionalProperties":false}`, pdfPolicy.MaxPages)),
			RequiredCapability: "research.fetch", RiskClass: "read", TimeoutSeconds: fetchTimeout,
			Idempotent: true, MaxOutputBytes: 32 << 10, AllowedRoles: []string{"researcher"},
		}, func(ctx context.Context, raw json.RawMessage) error {
			var args fetchArgs
			if err := decodeToolArgs(raw, &args); err != nil {
				return err
			}
			if strings.TrimSpace(args.URL) == "" || len(args.URL) > 2048 || len(args.OriginalQuery) > 1000 {
				return errors.New("fetch_url arguments exceed configured limits")
			}
			taskID := taskIDFromContext(ctx)
			if taskID == "" || !registry.urlLedger.Contains(taskID, args.URL) {
				return fmt.Errorf("%w: fetch_url target was not supplied by the user or a search result", ErrUnknownTarget)
			}
			if registry.urlLedger.WasFetched(taskID, args.URL) {
				return ErrDuplicateResearchTarget
			}
			if err := runtime.Fetcher.ValidateURL(ctx, args.URL); err != nil {
				return err
			}
			// agentSearcher reads PDFs itself, so a missing local PDF
			// extractor does not block a PDF URL when it is configured.
			if isPDFURL(args.URL) && !pageExtractorUsable(runtime) {
				if checker, ok := runtime.Extractor.(interface {
					PreflightPDF(context.Context) error
				}); ok {
					if err := checker.PreflightPDF(ctx); err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						if errors.Is(err, research.ErrPDFExtractorUnavailable) {
							return fetchPreflightError{failure: &research.FetchFailure{Err: err, Code: "pdf_extractor_unavailable", Phase: "preflight"}}
						}
						return err
					}
				}
			}
			return nil
		}, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args fetchArgs
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if strings.TrimSpace(args.URL) == "" || len(args.URL) > 2048 || len(args.OriginalQuery) > 1000 {
				return nil, errors.New("fetch_url arguments exceed configured limits")
			}
			taskID := taskIDFromContext(ctx)
			if taskID == "" || !registry.urlLedger.Contains(taskID, args.URL) {
				return nil, fmt.Errorf("%w: fetch_url target was not supplied by the user or a search result", ErrUnknownTarget)
			}
			if registry.urlLedger.WasFetched(taskID, args.URL) {
				return nil, ErrDuplicateResearchTarget
			}
			fetchLimit := runtime.MaxFetchBytes
			if taskLimit := FetchByteLimit(ctx, fetchLimit); taskLimit > 0 && taskLimit < fetchLimit {
				fetchLimit = taskLimit
			}
			fetchOptions := research.FetchOptions{MaxBytes: fetchLimit, TaskID: taskID}
			// The validator skipped the PDF preflight for PDF URLs when
			// agentSearcher is configured; the direct fallback checks it.
			if !isPDFURL(args.URL) || pageExtractorUsable(runtime) {
				if checker, ok := runtime.Extractor.(interface {
					PreflightPDF(context.Context) error
				}); ok {
					fetchOptions.PDFPreflight = checker.PreflightPDF
				}
			}
			// A page the direct fetch cannot read (bot blocking, challenge
			// pages, scripted content) may still have been read by
			// agentSearcher; its passages then become flagged evidence.
			passageFallback := func(cause error) (any, error) {
				if !passageFallbackEligible(ctx, cause) {
					return nil, cause
				}
				output, err := savePassageEvidence(ctx, runtime, registry.urlLedger, taskID, args, cause)
				if err != nil || output == nil {
					return nil, cause
				}
				return output, nil
			}
			// agentSearcher reads bot-protected pages (browser TLS
			// impersonation, headless rendering) and PDFs, so it is asked
			// first; when it cannot read the page the direct fetch follows.
			if pageExtractorUsable(runtime) {
				output, err := saveExtractEvidence(ctx, runtime, registry.urlLedger, taskID, args, fetchLimit)
				if output != nil || err != nil {
					return output, err
				}
			}
			fetched, err := runtime.Fetcher.Fetch(ctx, args.URL, fetchOptions)
			defer fetched.Cleanup()
			if err != nil {
				return passageFallback(err)
			}
			if fetched.ByteSize() > fetchLimit {
				return nil, &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "task_fetch_budget_exceeded", Phase: "fetch", FetchedBytes: fetched.ByteSize(), DeclaredBytes: fetched.DeclaredBytes, EffectiveLimitBytes: fetchLimit, MIMEType: fetched.ContentType, LimitKind: "task_remaining"}
			}
			if observer, ok := ctx.Value(extractionObserverKey{}).(func() error); ok && observer != nil {
				if err := observer(); err != nil {
					return nil, extractionFailure(ctx, fetched, err)
				}
			}
			extracted, err := runtime.Extractor.Extract(ctx, fetched)
			if err != nil {
				return passageFallback(extractionFailure(ctx, fetched, err))
			}
			if err := ctx.Err(); err != nil {
				return nil, extractionFailure(ctx, fetched, err)
			}
			if strings.TrimSpace(extracted.Text) == "" {
				return passageFallback(extractionFailure(ctx, fetched, research.ErrExtractionEmpty))
			}
			if utf8.RuneCountInString(extracted.Text) > pdfPolicy.MaxTextRunes {
				// Keep the first MaxTextRunes of a long document instead of
				// rejecting it: official calls and guidelines (e.g. a 356 KB
				// GKS .docx) were dropped entirely. Excerpts stay verbatim
				// spans of the stored, truncated text.
				extracted.Text = string([]rune(extracted.Text)[:pdfPolicy.MaxTextRunes])
			}
			if extracted.PageCount > pdfPolicy.MaxPages {
				return nil, extractionFailure(ctx, fetched, pdfextract.ErrTooManyPages)
			}
			persistCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			ctx = persistCtx
			var rawReader io.Reader = bytes.NewReader(fetched.Body)
			if fetched.File != nil {
				rawReader = io.NewSectionReader(fetched.File, 0, fetched.ByteSize())
			}
			rawArtifact, rawRef, err := runtime.Artifacts.PutReader(ctx, taskID, artifactFileName("source", fetched.ContentSHA256, ".raw"), fetched.ContentType, rawReader)
			if err != nil {
				return nil, err
			}
			textBytes := []byte(extracted.Text)
			textArtifact, textRef, err := runtime.Artifacts.Put(ctx, taskID, artifactFileName("source", fetched.ContentSHA256, ".txt"), "text/plain; charset=utf-8", textBytes)
			if err != nil {
				return nil, err
			}
			fetched.ContentSHA256 = rawArtifact.SHA256
			evidence, err := provenance.BuildEvidenceWithPassages(taskID, args.OriginalQuery, fetched, extracted, rawArtifact.ID, textArtifact.ID, registry.urlLedger.Passages(taskID, args.URL))
			if err != nil {
				return nil, err
			}
			evidence, err = saveResearchSource(ctx, runtime.Repository, rawArtifact, rawRef, textArtifact, textRef, evidence)
			if err != nil {
				return nil, err
			}
			registry.urlLedger.MarkFetched(taskID, args.URL)
			registry.urlLedger.MarkFetched(taskID, evidence.CanonicalURL)
			output := map[string]any{
				"evidence_id": evidence.ID, "url": evidence.CanonicalURL, "requested_url": args.URL, "title": evidence.Title,
				"excerpt": truncateUTF8(evidence.Excerpt, 4000), "content_sha256": evidence.ContentSHA256,
				"taint": research.TaintExternal, "fetched_bytes": fetched.ByteSize(),
			}
			if extracted.PageCount > 0 {
				output["page_count"] = extracted.PageCount
			}
			return output, nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func saveResearchSource(ctx context.Context, repository ResearchRepository, rawArtifact domain.Artifact, rawRef string, textArtifact domain.Artifact, textRef string, evidence domain.Evidence) (domain.Evidence, error) {
	if atomic, ok := repository.(interface {
		SaveResearchSource(context.Context, domain.Artifact, string, domain.Artifact, string, domain.Evidence) (domain.Evidence, error)
	}); ok {
		return atomic.SaveResearchSource(ctx, rawArtifact, rawRef, textArtifact, textRef, evidence)
	}
	rawArtifact, err := repository.SaveArtifact(ctx, rawArtifact, rawRef)
	if err != nil {
		return domain.Evidence{}, err
	}
	textArtifact, err = repository.SaveArtifact(ctx, textArtifact, textRef)
	if err != nil {
		return domain.Evidence{}, err
	}
	evidence.RawArtifactID, evidence.TextArtifactID = rawArtifact.ID, textArtifact.ID
	return repository.SaveEvidence(ctx, evidence)
}

// passageFallbackEligible reports whether a direct fetch failure is one where
// the page exists but the backend could not read it: a non-success status
// (403 bot blocking), a failed request, a challenge page or empty extracted
// text. Policy, size and cancellation failures never fall back.
func passageFallbackEligible(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	for _, cause := range []error{research.ErrFetchSourceUnavailable, research.ErrFetchRequestFailed, research.ErrExtractionChallenge, research.ErrExtractionEmpty} {
		if errors.Is(err, cause) {
			return true
		}
	}
	var failure *research.FetchFailure
	if errors.As(err, &failure) {
		switch failure.Code {
		case "source_unavailable", "fetch_source_unavailable", "fetch_request_failed", "extraction_challenge", "extraction_empty":
			return true
		}
	}
	return false
}

// passageRecord is the raw artifact of passage evidence: it records which
// provider supplied the text and why the direct fetch was not used.
type passageRecord struct {
	Provider         string   `json:"provider"`
	URL              string   `json:"url"`
	Title            string   `json:"title"`
	Passages         []string `json:"passages"`
	DirectFetchError string   `json:"direct_fetch_error"`
}

// savePassageEvidence stores agentSearcher passages of a page as evidence
// after its direct fetch failed. It returns nil output when no passages are
// known for the URL. The raw artifact is a JSON record of the passages and
// the failure; the text artifact holds the passages, and the excerpt is a
// verbatim span of it.
func savePassageEvidence(ctx context.Context, runtime ResearchTools, ledger *URLLedger, taskID string, args fetchArgs, cause error) (map[string]any, error) {
	title, passages := ledger.SearchPassages(taskID, args.URL)
	text := provenance.JoinPassages(passages)
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	code := "source_unavailable"
	var failure *research.FetchFailure
	if errors.As(cause, &failure) && failure.Code != "" {
		code = failure.Code
	}
	raw, err := json.Marshal(passageRecord{Provider: provenance.PassageEvidenceProvider, URL: args.URL, Title: title, Passages: passages, DirectFetchError: code})
	if err != nil {
		return nil, err
	}
	persistCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hash := sha256.Sum256(raw)
	contentHash := hex.EncodeToString(hash[:])
	rawArtifact, rawRef, err := runtime.Artifacts.Put(persistCtx, taskID, artifactFileName("passages", contentHash, ".json"), "application/json", raw)
	if err != nil {
		return nil, err
	}
	textArtifact, textRef, err := runtime.Artifacts.Put(persistCtx, taskID, artifactFileName("passages", contentHash, ".txt"), "text/plain; charset=utf-8", []byte(text))
	if err != nil {
		return nil, err
	}
	evidence, err := provenance.BuildPassageEvidence(taskID, args.OriginalQuery, args.URL, title, text, rawArtifact.SHA256, time.Now().UTC(), rawArtifact.ID, textArtifact.ID)
	if err != nil {
		return nil, err
	}
	evidence, err = saveResearchSource(persistCtx, runtime.Repository, rawArtifact, rawRef, textArtifact, textRef, evidence)
	if err != nil {
		return nil, err
	}
	ledger.MarkFetched(taskID, args.URL)
	ledger.MarkFetched(taskID, evidence.CanonicalURL)
	var fetchedBytes int64
	if failure != nil {
		fetchedBytes = failure.FetchedBytes
	}
	return map[string]any{
		"evidence_id": evidence.ID, "url": evidence.CanonicalURL, "requested_url": args.URL, "title": evidence.Title,
		"excerpt": truncateUTF8(evidence.Excerpt, 4000), "content_sha256": evidence.ContentSHA256,
		"taint": research.TaintExternal, "fetched_bytes": fetchedBytes,
		"source_text": provenance.PassageEvidenceMethod, "direct_fetch_error": code,
	}, nil
}

// pageExtractorUsable reports whether fetch_url asks agentSearcher first.
// With a configured domain allow-list it does not: agentSearcher follows
// redirects itself and reports no final URL, so the backend could not
// check that the text came from an allowed host.
func pageExtractorUsable(runtime ResearchTools) bool {
	return runtime.PageExtractor != nil && len(runtime.AllowedDomains) == 0
}

// extractRecord is the raw artifact of agentSearcher extract evidence: the
// request parameters and the service's JSON responses, so the content hash
// covers exactly what agentSearcher returned.
type extractRecord struct {
	Provider        string          `json:"provider"`
	Endpoint        string          `json:"endpoint"`
	URL             string          `json:"url"`
	Query           string          `json:"query,omitempty"`
	ExtractDepth    string          `json:"extract_depth"`
	Format          string          `json:"format"`
	ChunksPerSource int             `json:"chunks_per_source"`
	PDF             bool            `json:"pdf"`
	TextResponse    json.RawMessage `json:"text_response"`
	ChunksResponse  json.RawMessage `json:"chunks_response,omitempty"`
}

// saveExtractEvidence reads args.URL through agentSearcher /extract and
// stores the page text as evidence. It returns nil output and nil error when
// agentSearcher could not read the page, so the caller falls back to the
// direct fetch. URL policy (SSRF, canonical form) is checked before the
// service is asked. The page text counts against the task fetch budget.
func saveExtractEvidence(ctx context.Context, runtime ResearchTools, ledger *URLLedger, taskID string, args fetchArgs, fetchLimit int64) (map[string]any, error) {
	if err := runtime.Fetcher.ValidateURL(ctx, args.URL); err != nil {
		return nil, err
	}
	if _, err := provenance.CanonicalURL(args.URL); err != nil {
		return nil, err
	}
	page, err := runtime.PageExtractor.Extract(ctx, args.URL, args.OriginalQuery)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil || strings.TrimSpace(page.Text) == "" {
		return nil, nil
	}
	textBytes := int64(len(page.Text))
	if textBytes > fetchLimit {
		return nil, &research.FetchFailure{Err: research.ErrResponseTooBig, Code: "task_fetch_budget_exceeded", Phase: "fetch", FetchedBytes: textBytes, EffectiveLimitBytes: fetchLimit, MIMEType: "text/plain", LimitKind: "task_remaining"}
	}
	if observer, ok := ctx.Value(extractionObserverKey{}).(func() error); ok && observer != nil {
		if err := observer(); err != nil {
			return nil, err
		}
	}
	title, passages := ledger.SearchPassages(taskID, args.URL)
	text := provenance.ExtractEvidenceText(page.Text, page.Chunks)
	raw, err := json.Marshal(extractRecord{
		Provider: provenance.ExtractEvidenceProvider, Endpoint: "/extract", URL: args.URL, Query: strings.TrimSpace(args.OriginalQuery),
		ExtractDepth: research.AgentSearcherExtractDepth, Format: research.AgentSearcherExtractFormat, ChunksPerSource: research.AgentSearcherExtractChunks,
		PDF: page.PDF, TextResponse: page.TextResponse, ChunksResponse: page.ChunksResponse,
	})
	if err != nil {
		return nil, err
	}
	persistCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hash := sha256.Sum256(raw)
	contentHash := hex.EncodeToString(hash[:])
	rawArtifact, rawRef, err := runtime.Artifacts.Put(persistCtx, taskID, artifactFileName("extract", contentHash, ".json"), "application/json", raw)
	if err != nil {
		return nil, err
	}
	textArtifact, textRef, err := runtime.Artifacts.Put(persistCtx, taskID, artifactFileName("extract", contentHash, ".txt"), "text/plain; charset=utf-8", []byte(text))
	if err != nil {
		return nil, err
	}
	evidence, err := provenance.BuildExtractEvidence(provenance.ExtractEvidenceInput{
		TaskID: taskID, Query: args.OriginalQuery, URL: args.URL, Title: title, Text: text,
		Chunks: page.Chunks, Passages: passages, PDF: page.PDF, ContentSHA256: rawArtifact.SHA256,
		RetrievedAt: time.Now().UTC(), RawArtifactID: rawArtifact.ID, TextArtifactID: textArtifact.ID,
	})
	if err != nil {
		return nil, err
	}
	evidence, err = saveResearchSource(persistCtx, runtime.Repository, rawArtifact, rawRef, textArtifact, textRef, evidence)
	if err != nil {
		return nil, err
	}
	ledger.MarkFetched(taskID, args.URL)
	ledger.MarkFetched(taskID, evidence.CanonicalURL)
	return map[string]any{
		"evidence_id": evidence.ID, "url": evidence.CanonicalURL, "requested_url": args.URL, "title": evidence.Title,
		"excerpt": truncateUTF8(evidence.Excerpt, provenance.ExtractExcerptRunes), "content_sha256": evidence.ContentSHA256,
		"taint": research.TaintExternal, "fetched_bytes": textBytes, "extraction_method": evidence.ExtractionMethod,
	}, nil
}

func isPDFURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(path.Ext(parsed.Path), ".pdf")
}

func extractionFailure(ctx context.Context, fetched research.FetchedDocument, err error) error {
	code := "extraction_failed"
	if fetched.ContentType == "application/pdf" {
		code = "pdf_extraction_failed"
		for _, item := range []struct {
			cause error
			code  string
		}{
			{pdfextract.ErrInputTooLarge, "pdf_extraction_input_too_large"},
			{pdfextract.ErrTooManyPages, "pdf_page_limit_exceeded"},
			{pdfextract.ErrTextTooLarge, "pdf_text_limit_exceeded"},
			{pdfextract.ErrResourceExceeded, "pdf_resource_exceeded"},
			{pdfextract.ErrOutputExceeded, "pdf_resource_exceeded"},
			{pdfextract.ErrTimeout, "pdf_timeout"},
			{pdfextract.ErrUnavailable, "pdf_extractor_unavailable"},
			{pdfextract.ErrMalformed, "pdf_malformed"},
			{pdfextract.ErrEncrypted, "pdf_encrypted"},
			{pdfextract.ErrNoText, "pdf_no_text"},
			{research.ErrPDFExtractorUnavailable, "pdf_extractor_unavailable"},
			{research.ErrPDFTooManyPages, "pdf_page_limit_exceeded"},
			{research.ErrPDFTooLarge, "pdf_extraction_input_too_large"},
			{research.ErrPDFEncrypted, "pdf_encrypted"},
			{research.ErrPDFNoText, "pdf_no_text"},
		} {
			if errors.Is(err, item.cause) {
				code = item.code
				break
			}
		}
	}
	if errors.Is(err, research.ErrExtractionEmpty) {
		code = "extraction_empty"
	}
	if errors.Is(err, research.ErrExtractionChallenge) {
		code = "extraction_challenge"
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
		code = ""
	}
	return &research.FetchFailure{Err: err, Code: code, Phase: "extract", FetchedBytes: fetched.ByteSize(), DeclaredBytes: fetched.DeclaredBytes, EffectiveLimitBytes: fetched.EffectiveLimitBytes, MIMEType: fetched.ContentType}
}

type searchOutcomeObserverKey struct{}

type extractionObserverKey struct{}

func WithExtractionObserver(ctx context.Context, observer func() error) context.Context {
	return context.WithValue(ctx, extractionObserverKey{}, observer)
}

func WithSearchOutcomeObserver(ctx context.Context, observer func(research.ProviderOutcome)) context.Context {
	return context.WithValue(ctx, searchOutcomeObserverKey{}, observer)
}

func observeSearchOutcome(ctx context.Context, outcome research.ProviderOutcome) {
	if observer, ok := ctx.Value(searchOutcomeObserverKey{}).(func(research.ProviderOutcome)); ok && observer != nil {
		observer(outcome)
	}
}

func validateSearchArguments(configuredDomains []string) ArgumentsValidator {
	return func(_ context.Context, raw json.RawMessage) error {
		var args searchArgs
		if err := decodeToolArgs(raw, &args); err != nil {
			return err
		}
		if strings.TrimSpace(args.Query) == "" || len(args.Query) > 1000 || len(args.Language) > 20 {
			return errors.New("search arguments exceed configured limits")
		}
		if args.MaxResults != 0 && (args.MaxResults < 1 || args.MaxResults > MaxSearchResults) {
			return fmt.Errorf("max_results must be between 1 and %d", MaxSearchResults)
		}
		if args.Recency != "" && args.Recency != "day" && args.Recency != "week" && args.Recency != "month" && args.Recency != "year" {
			return errors.New("invalid search recency")
		}
		if len(args.PreferredDomains) > maxPreferredSearchDomains {
			return errors.New("too many preferred search domains")
		}
		for _, domain := range args.PreferredDomains {
			if normalizeDomain(domain) == "" {
				return errors.New("invalid preferred search domain")
			}
		}
		_, err := effectiveDomains(nil, configuredDomains)
		return err
	}
}

func effectiveDomains(requested, configured []string) ([]string, error) {
	for _, domain := range requested {
		if normalizeDomain(domain) == "" {
			return nil, errors.New("invalid search domain filter")
		}
	}
	for _, domain := range configured {
		if normalizeDomain(domain) == "" {
			return nil, errors.New("invalid configured search domain")
		}
	}
	if len(configured) == 0 {
		result := make([]string, 0, len(requested))
		for _, domain := range requested {
			result = append(result, normalizeDomain(domain))
		}
		return result, nil
	}
	if len(requested) == 0 {
		result := make([]string, 0, len(configured))
		for _, domain := range configured {
			result = append(result, normalizeDomain(domain))
		}
		return result, nil
	}
	var result []string
	for _, requestDomain := range requested {
		requestDomain = normalizeDomain(requestDomain)
		if requestDomain == "" {
			return nil, errors.New("invalid search domain filter")
		}
		matched := false
		for _, allowed := range configured {
			allowed = normalizeDomain(allowed)
			if domainWithin(requestDomain, allowed) {
				result = append(result, requestDomain)
				matched = true
				break
			}
			if domainWithin(allowed, requestDomain) {
				result = append(result, allowed)
				matched = true
			}
		}
		if !matched {
			return nil, errors.New("search domain is outside configured ALLOWED_DOMAINS")
		}
	}
	return uniqueStrings(result), nil
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	if value == "" || strings.ContainsAny(value, "/:@?# \\[]") {
		return ""
	}
	if net.ParseIP(value) != nil {
		return value
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") {
		return ""
	}
	return value
}

func domainWithin(candidate, parent string) bool {
	return candidate == parent || strings.HasSuffix(candidate, "."+parent)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type searchArgs struct {
	Query      string `json:"query"`
	Language   string `json:"language"`
	Recency    string `json:"recency"`
	MaxResults int    `json:"max_results"`
	// PreferredDomains rank results from these sites first without
	// excluding other sites (for example official domains found earlier).
	PreferredDomains []string `json:"preferred_domains,omitempty"`
}

// maxPreferredSearchDomains bounds preferred_domains of one search.
const maxPreferredSearchDomains = 8

// preferredSearchDomains normalizes preferred domains, dropping invalid and
// repeated entries.
func preferredSearchDomains(values []string) []string {
	result := []string{}
	for _, value := range values {
		if domain := normalizeDomain(value); domain != "" && len(result) < maxPreferredSearchDomains {
			result = append(result, domain)
		}
	}
	return uniqueStrings(result)
}

type fetchArgs struct {
	URL           string `json:"url"`
	OriginalQuery string `json:"original_query"`
}

type taskContextKey struct{}
type fetchByteLimitKey struct{}

func WithTaskID(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, taskContextKey{}, taskID)
}

func taskIDFromContext(ctx context.Context) string {
	taskID, _ := ctx.Value(taskContextKey{}).(string)
	return taskID
}

func WithFetchByteLimit(ctx context.Context, limit int64) context.Context {
	return context.WithValue(ctx, fetchByteLimitKey{}, limit)
}

func FetchByteLimit(ctx context.Context, fallback int64) int64 {
	limit, ok := ctx.Value(fetchByteLimitKey{}).(int64)
	if !ok || limit < 1 {
		return fallback
	}
	return limit
}

func decodeToolArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid research tool arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("research tool accepts one JSON object")
	}
	return nil
}

func truncateUTF8(value string, maxRunes int) string {
	if maxRunes < 1 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

func artifactFileName(prefix, hash, extension string) string {
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return prefix + "-" + hash + extension
}
