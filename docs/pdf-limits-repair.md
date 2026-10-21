# PDF limits and source fallback repair

Status: integrated policy/configuration, source-failure handling, doctor reporting, and frontend behavior pass the recorded automated checks. The live 40,534,667-byte PDF was downloaded and validated as a file-backed response, but was not parsed: rootless extraction was unavailable. The recorded research run ended `verification_incomplete`; it did not complete successfully.

## 1. Changed files

The integration spans configuration and CLI doctor (`internal/config`, `internal/cli`), fetch policy and live PDF coverage (`internal/providers/research`), PDF isolation (`internal/runtime/pdfextract`), orchestration and source-failure events (`internal/orchestrator`), tools and artifact storage (`internal/tools`, `internal/storage/artifacts`), API projection (`internal/api`), and the frontend source/research panels and embedded build. The contracts, README, security notes, and this report document the resulting behavior. Existing unrelated working-tree changes were retained.

## 2. Limits before and repair profile

| Limit | Before | Configured/default profile reported by doctor |
| --- | ---: | ---: |
| Cumulative task download | 5,000,000 bytes | 134,217,728 bytes (128 MiB) |
| Ordinary source | Coupled to task | 16,777,216 bytes (16 MiB) |
| PDF download/parser input | Hidden 5,000,000-byte caps | 67,108,864 bytes (64 MiB) |
| Pages / Unicode text | 20 / 50,000 | 20 / 50,000 |
| Extraction / download timeout | 5 / 20 seconds | 30 / 120 seconds |
| Isolated memory / tmpfs | 128 / 8 MiB | 512 / 64 MiB |
| Isolated CPU / PIDs | 0.5 / 16 | 1 / 32 |
| Unsandboxed fallback | Automatic | Explicit opt-in; default false |

The same doctor snapshot reports artifact cap 67,108,864 bytes, sandbox-file cap 5,000,000 bytes, and fetch-temp task/global quotas 134,217,728 / 268,435,456 bytes. These caps are independent; they are not extra download budgets.

## 3. Effective PDF bytes, MB and MiB

The effective PDF limit is **67,108,864 bytes = 67.108864 MB = 64 MiB**. The incident PDF is **40,534,667 bytes = 40.534667 MB ≈ 38.657 MiB**, below that limit. In the live source failure, declared/effective/fetched bytes were **40,534,667 / 67,108,864 / 40,534,667**. This was not an oversize failure and must not be described as exceeding 64 MiB. Download validation did not establish extraction or parsing success.

## 4. Task budget versus source cap

`MAX_FETCH_BYTES` is the cumulative task budget; `MAX_SOURCE_BYTES` and `MAX_PDF_BYTES` cap individual candidates. Header-known oversize is rejected before body reads; streaming overflow accounts for bytes actually read. Source byte/page/text/timeout/parser/resource failures remain distinct from aggregate task-budget exhaustion. The previously failing source-oversize regression coverage is fixed and passes in the final test suite.

## 5. Removed hardcoded caps

The effective task, ordinary-source, PDF, artifact, sandbox-file, and fetch-temp values are integrated into configuration and reported by doctor rather than inferred from a single coupled fetch cap. Source/PDF size failures are exercised by provider and policy tests, including the repaired oversized-source regression. Public diagnostic byte values remain bounded for safe transport; that diagnostic bound is not permission to fetch that amount.

## 6. Fallback lifecycle

The live PDF source attempt failed with `pdf_extractor_unavailable`. Its explicit URL was the only candidate in that plan step, so `try_next_source=false` and `fallback_attempted=false`. Two separately planned search steps then ran and fetched two HTML sources, producing two evidence records. Those planned searches were not a same-candidate PDF fallback. The evidence did not satisfy research verification; the run ended `verification_incomplete`. A source failure, permission to try a different candidate, an attempted fallback, successful extraction, and completion are distinct states.

Source-level failures terminate that candidate and may permit another authorized candidate. Cancellation, whole-task timeout, aggregate exhaustion, unknown outcome, policy rejection, and exact-user-URL requirements must not silently substitute candidates. Existing accepted evidence remains independent of another candidate's failure.

## 7. Error and event codes

The live source failure used `pdf_extractor_unavailable` and reported `declared_bytes=40534667`, `effective_limit_bytes=67108864`, and `fetched_bytes=40534667`. The typed public contract includes `task_fetch_budget_exceeded`, `source_response_too_large`, `pdf_source_too_large`, `pdf_extraction_input_too_large`, `pdf_page_limit_exceeded`, `pdf_text_limit_exceeded`, `pdf_resource_exceeded`, `pdf_timeout`, `pdf_extractor_unavailable`, `pdf_malformed`, `pdf_encrypted`, and `fetch_outcome_unknown`, plus allowlisted network/security codes. Unknown strings become `source_failed`; the legacy `fetch_budget_exceeded` remains ambiguous.

Failure and nested warning projections expose sanitized source metadata only. The live run's source failure was available through the source-failure contract and nested `research.warning.source_failures`; neither includes raw parser errors or response bodies.

## 8. API and UI

Failure metadata is keyed by `attempt_id`, `plan_attempt`, and `step_id` and includes the tool, safe target/fingerprint, status, error code, retry/fallback flags, optional declared/effective/fetched byte counts, normalized MIME, remaining candidates, timestamp, and `fallback_attempted`. Raw errors, response bodies, stderr, secret URLs, and arbitrary payload fields are excluded. Missing values remain unknown; zero remains zero. The UI displays exact bytes plus decimal MB/binary MiB, body-read state, the failure explanation, candidate state, and timestamp. Failed/rejected source cards cannot supply citations, while accepted evidence remains visible.

`GET /api/doctor` reports the integrated policy snapshot and PDF runtime availability. The current doctor values match the profile in section 2; extractor readiness is unavailable in this environment and unsandboxed fallback is false. Doctor checks readiness, not PDF parsing.

The API composition contract is `SetResearchPolicy(policy map[string]any, pdfCheck func(context.Context) error)`. It receives a constructed safe policy snapshot and the runtime extractor `Check` method, which checks availability only and does not parse PDFs. CLI doctor delegates to the running app's bounded sanitized snapshot when the app responds; local configuration is not substituted for missing server policy.

## 9. Body versus streaming temporary files

The gated live test verified a file-backed download of **40,534,667 bytes in 41.939 seconds**. It checked a nil in-memory body, PDF magic/MIME, HTTP status 200, mode `0600`, the SHA-256 digest, and cleanup. The digest was `6777923e8a164fe653a64d6fc5bc0e8ef37a3b608bef01ed0d9e5dce4dc81f9c`. This is download and temporary-file evidence only; no extracted text, parser output, or evidence from PDF parsing is claimed.

## 10. Required runtime

Isolated extraction requires an available immutable local Poppler image and verified local **rootless** Docker, with `--pull=never`, no network, read-only root, dropped capabilities, no-new-privileges, non-root UID, resource bounds, bounded output, and cleanup. In the recorded environment, `PDF_EXTRACTOR_IMAGE` was unset and rootless extraction was unavailable. Rootful Docker does not establish rootless isolation. With fallback false, the expected result is `pdf_extractor_unavailable`; that is the source failure observed in the live run.

## 11. Test results

The final integrated checks passed:

- `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...` — passed.
- `CGO_ENABLED=1 go vet -tags sqlite_fts5 ./...` — passed.
- `CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...` — passed.
- Frontend tests — 33/33 passed; typecheck and production build passed.
- Mocked Chrome browser suite — passed; this verifies UI behavior, not PDF extraction.
- Source-oversize regression coverage — passed in the final suites.

The real gated command was:

```sh
QAGENT_LIVE_PDF=1 CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/providers/research -run '^TestLivePDFPolicy$' -count=1 -v -timeout180s
```

The file-backed download subtest **passed** with the checks and digest in section 9. `RootlessExtractor` was **skipped** because `PDF_EXTRACTOR_IMAGE` was unset and `Check` reported unavailable. The gated command therefore does not demonstrate true PDF parsing or enforcement of extraction CPU/memory limits.

## 12. Live checks

Research run `52f28017-840c-47e9-8297-70a4a5580941` ended with status **`verification_incomplete`**, not `complete`. Its explicit PDF source failed with `pdf_extractor_unavailable` and byte counts 40,534,667 / 67,108,864 / 40,534,667 (declared/effective/fetched). The attempt had no next candidate and recorded `fallback_attempted=false`. The plan subsequently ran two search steps; two HTML sources yielded two evidence records, which were still insufficient for research verification. This is an observed incomplete research result, not a successful PDF extraction or completed answer.

The gated live download test separately passed its file-backed transfer, PDF metadata/magic, hash, file-mode, and cleanup checks. It skipped the rootless extractor. No live parser text/hash or incident-PDF extraction result exists in this run.

## 13. Environment limitations

`PDF_EXTRACTOR_IMAGE` was unset and rootless Docker was unavailable to the extractor check. Rootful Docker was available but does not satisfy the isolation requirement. The test's configured CPU, memory, tmpfs, and PID values are policy settings, not measurements or evidence that a real parser ran under those limits. The pure-Go helper remains process-bounded rather than OS-isolated and was not used for this live extraction.

## 14. Remaining risks and completion boundary

The configuration, doctor integration, source-failure contract, oversized-source regression, automated Go suites, frontend checks, and file-backed live download passed their recorded checks. The 40,534,667-byte PDF was **not parsed**, the isolated extractor was unavailable, and the live research run was **not complete**. The run does not establish parser safety or verified claims from the PDF. Any claim of successful extraction, resource-bounded parsing, or a completed research answer would exceed the available evidence.
