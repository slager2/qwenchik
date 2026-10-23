# Frontend network acceptance, 2026-10-23

## Read-only local service probes

All live requests were GET-only to loopback `127.0.0.1:8080`; no task, conversation, approval, refinement, or memory was created or changed. No model or provider probe was run, and no SSE payload was inspected.

| Request | Result |
|---|---|
| `/api/health` | HTTP 200; `status=ok`, database `ok`, worker attached |
| `/` | HTTP 200; embedded UI HTML returned |
| Referenced JS and CSS assets | HTTP 200 |
| Existing run SSE stream with a cursor ahead of history | HTTP 200; first event was `snapshot`; payload withheld |

The running service's HTML referenced JS/CSS asset hashes different from the current checkout's production build. The running binary therefore serves an older frontend than this worktree. It was left untouched; no restart or redeploy was attempted. These probes establish process/database liveness and SSE handshake, not current-bundle deployment or model readiness.

## Automated acceptance

- `npm --prefix frontend run typecheck` — passed.
- `npm --prefix frontend test` — **48/48 passed**. Endpoint diagnostics tests now distinguish `/api/memories/search` and `/api/bundles/rollback`, and cover caller-visible JSON decoding failure accounting.
- `npm --prefix frontend run test:browser` — passed with mocked API/SSE, including the malformed-JSON-over-HTTP-200 diagnostics case and citation source navigation. This does not call the Go server or a model.
- `npm --prefix frontend run build` — passed. Workspace is emitted as a dedicated lazy chunk (**43.15 KiB**, gzip **12.55 KiB**); the production Chat entry contains no Kanban board component code.
- `npm --prefix frontend run test:production` — passed against Vite's production preview and mocked API. Chat does not fetch the Workspace chunk before navigation; Workspace navigation loads it once; failed chunk loading shows the recoverable fallback; the production page does not expose development diagnostics. This check also caught and fixed a duplicate empty-chat onboarding panel.
- `CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/api -run '^TestEmbeddedUIAndSecurityHeaders$' -count=1` — passed for the embedded frontend artifacts.

The browser tests use isolated mocked API data. No live-browser run, real research request, `/api/doctor` probe, task mutation, or runtime restart was part of this acceptance.
