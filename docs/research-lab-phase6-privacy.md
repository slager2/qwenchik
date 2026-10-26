# Laboratory Phase 6 — privacy contracts and offline imports

`RESEARCH_PRIVACY_MODE` is trusted process configuration, snapshotted into each
research session. User/model request fields cannot override it. Legacy empty
snapshots mean `local_inference_external_search` for recovery compatibility.

| Contract | Search | Source retrieval |
| --- | --- | --- |
| `local_inference_external_search` (default) | Existing configured provider policy | Public guarded HTTP(S) |
| `local_searxng` | Only the loopback SearXNG adapter; no Tavily/DDG fallback | Public guarded HTTP(S) |
| `offline` | Lexical search in pinned local imports | Only exact imported URLs, from memory; no HTTP/DNS/fallback |

All modes retain local loopback inference and concurrency one. Local SearXNG is
**not offline**: its engines and source sites receive network requests. The UI
shows this distinction before submission and from the saved runtime policy.
Offline disables network search diagnostics and ntfy notification configuration.
Sandbox networking stays disabled. This is an application routing contract, not
an OS-wide firewall for other processes or browser navigation to citation links.

Offline requires `RESEARCH_OFFLINE_CORPUS=/absolute/path/corpus.json`:

```json
{"documents":[{"url":"https://source.example.org/article","title":"Imported article","text":"Previously imported plain text."}]}
```

Limits: regular file <=16 MiB, 1–128 documents, <=256 KiB text/document; unique
HTTP(S) source identities, bounded title/URL, strict JSON. The byte SHA-256 is
snapshotted; changed imports/mode cannot silently resume a saved run. Source URLs
are provenance identifiers, never network targets in offline mode. Corpus text
remains untrusted evidence and uses the same artifacts, source notes, verifier and
report pipeline. Imports are pre-extracted text; no arbitrary local file paths or
automatic web/PDF import is exposed. Recency is not independently verified.

Verified: config validation/pinning, local-provider-only gating despite enabled
external fallbacks, changed-corpus rejection, no-contact fetch/unknown URL/size/
cancellation checks, and full scripted-model offline worker execution through
durable evidence, notes, section report and runtime privacy provenance. The local
model in the integration test is scripted; real model quality is separate.

Files: domain privacy/policy/defaults, config privacy, offline research provider,
CLI serve/provider/doctor/search-smoke, API safe policy projection, runtime,
report methodology, frontend PrivacyNotice/composer/Inspector/types, `.env.example`.
