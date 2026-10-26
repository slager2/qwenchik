# Laboratory Phase 5 — PDF capability and fixture

Doctor/API/Inspector distinguish `isolated_available`,
`pure_go_fallback_available`, and `unavailable`; capability probes explicitly
return `smoke_tested=false`. A configured image is not a proven extraction.
Outcomes distinguish policy rejection, source-size rejection, unavailable runtime,
extraction failure, and successful extraction. Existing page/text/timeout/temp,
download-size, MIME/preflight, Windows and fallback-policy checks remain in force.

The checked-in one-page PDF has a pinned SHA-256 and is extracted by the real
bounded pure-Go subprocess in the ordinary test suite. Isolated fixture smoke is
opt-in: `QAGENT_PDF_RUNTIME_SMOKE=1 go test ./internal/runtime/pdfextract -run
'^TestIsolatedPDFFixtureRuntimeSmoke$' -count=1 -v` with a configured
`PDF_EXTRACTOR_IMAGE` and rootless runtime.

Fresh verification: full SQLite-FTS Go suite and vet passed; frontend 77 unit tests,
TypeScript, Chrome smoke and production build passed. The explicit isolated smoke
was attempted and failed its capability prerequisite with `isolated PDF extraction
is unavailable`; it did not extract the fixture. No live cgroup isolation or hard
memory-limit acceptance is claimed. Pure-Go fallback is process-bounded, not OS
sandbox isolation.

Files: `internal/runtime/pdfextract/{capability.go,fixture_smoke_test.go,
testdata/one-page.pdf,extractor.go}`, `internal/providers/research/html.go`,
API/CLI doctor projections, frontend types/Inspector, `.gitattributes`.
