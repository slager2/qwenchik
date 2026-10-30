<!-- Hallmark · final critique: P4 H4 E3 S5 R4 V4 · designed-as-app -->
# Deep Research Phase 5 — task-first frontend

Date: 2026-10-29. Phase 4 was explicitly skipped by the user; its useful-research
gate is not treated as passed. The user confirmed the technical Cobalt direction
and requested parallel agents. Design and implementation started on `7e0901c`;
independent runtime work advanced HEAD through `f0b9324` and `e26d462` during this
phase. Those backend commits are not Phase 5 implementation or verification.

## Delivered — Implemented

The frontend is a research workbench, with the existing Chat/Workspace routes and
canonical answers preserved. The [plan](deep-research-phase5-frontend-plan.md) and
user-confirmed [design system](../design.md) precede implementation.

- **Conversation center:** compact research overview with actual run state,
  reason, saved materials, answer/report availability and available actions.
  An internal PASS or completed checkpoint without an artifact is not a report.
- **Reading:** canonical Markdown remains inline. Long answers have a scoped
  section outline, keyboard focus navigation and a “Читать ответ” action. Saved
  research opens at its beginning rather than at trailing report metadata;
  active streaming still follows the correct scroll surface.
- **Details:** outcome and report availability precede active round plan,
  completed/skipped work, sources, owned evidence and disclosed diagnostics.
  Source failures, MIME/byte limits, provenance and provider distinctions remain
  available. Unknown or stale measurements are not fabricated zeros.
- **Pauses:** shared reason labels/resume eligibility in Chat and Workspace.
  Manual resource holds allow an explicit backend recheck; guarded and uncertain
  recovery pauses explain why Resume is unavailable.
- **Composer:** explicit task/message labels, distinct sending versus failed
  submission, visible research-context explanation and next-run settings.
  Profile/time ceilings and model settings are persisted through existing API
  contracts; temperature zero is preserved. Editing a draft invalidates a stale
  failed-save retry so it cannot overwrite newer changes.
- **Passive browsing:** opening Chat/Workspace and periodic refresh use health,
  configured policy and providers. Inference-capable Doctor is an explicit
  “Проверить модель и поиск” action, not a background readiness probe.
- **Responsive system:** locked Cobalt fonts/tokens, readable secondary contrast,
  functional headings, 44px major controls, explicit mobile drawers and focus
  restoration. Mobile reading has one natural scroll surface; header actions
  have their own row. Workspace navigation has a two-row mobile layout.
- **Telemetry:** live measurements and a separately disclosed saved invocation
  remain distinct. Saved rates never refill the live ticker.

Removed behaviors: automatic mobile Details opening and background Doctor calls.
They obscured the task and could consume the single inference slot just by
viewing a page. No route, component directory, database or worker was replaced.

The current client default/ceiling for output is **5120**, aligned with the
concurrent user configuration; explicit 4096 remains supported and tested. This
does not rewrite the Phase 3 benchmark's fixed 4096-token protocol or enlarge an
already-pinned run. Default temperature/top-p remain 0.2/0.8. Settings identify
client defaults rather than claiming they are measured server capacity.

## Parallel implementation

Independent ownership separated research presentation/helpers, conversation and
reading/settings, shared CSS/telemetry, and browser verification. The main agent
owned API/types, route integration, passive discovery, Workspace actions and
final evidence. Agents also cross-reviewed state boundaries and evidence.

The implementation retains the existing reducer, canonical snapshot precedence,
SSE cursor/replay, terminal reconciliation, fallback refresh, evidence-cache
ownership, idempotent submission, hash routing and lazy Workspace loading.

## Scripted-test verified

| Check | Result / scope |
| --- | --- |
| `npm test` | **114/114 PASS**: pure helpers and static React rendering |
| `npx tsc --noEmit --incremental false --pretty false` | PASS |
| `npm run build` | PASS; actual Go-embedded destination `internal/api/web/dist` |
| `npm run test:browser` | PASS: mocked API/SSE, interaction and responsive scenarios |
| `npm run test:production` | PASS: current production assets, passive discovery, lazy chunk and recoverable load failure |
| `CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/api/... -timeout=5m` | PASS for the API/embedded frontend integration checked during this phase |
| `go vet -tags sqlite_fts5 ./internal/api/...` | PASS |
| `git diff --check` | PASS |

Browser coverage includes 320/375/414/768/1024/1280/1440 widths, child bounds,
major 44px controls, short height, enlarged text, reduced motion, forward/reverse
drawer focus wrapping, Escape/return focus, explicit Details, three pause classes
in both routes, profile/model save/reload/error handling, stale-draft isolation,
delayed sending and idempotent retry, section focus without hash changes, reading
from saved-answer start, mobile/desktop streaming follow, source failure/retry,
historical citations, Stop/Retry/Continue, and Workspace/memory navigation.

The browser runner observes **zero automatic Doctor calls**, including simulated
idle refresh; its two intentional calls are one explicit check per route. Those
requests are mocked. Mock streaming/save/lifecycle checks are not real-model or
live database mutation acceptance. No full Phase 6 regression/soak is claimed.

## Live acceptance verified — read-only UI

The new production frontend was served at **http://127.0.0.1:4177** against the
existing real API on 8080. This allowed inspection while separate real research
was active, without restarting its worker. Capture interception was installed
before navigation: real allowed GET responses passed unchanged; Doctor, external
origins and all mutations were blocked.

Final evidence is retained in [experiments/phase5](experiments/phase5/README.md):

| Capture | Screenshots | Requests | Automatic Doctor attempts | Mutations / external attempts | JS page errors |
| --- | ---: | ---: | ---: | ---: | ---: |
| Reconstructed old-UI reference | 35 | 855 | 35, blocked | 0 / 0 | 0 |
| Accepted new UI | 39 | 720 | 0 | 0 / 0 | 0 |

Both include five real saved views at seven widths, 900px height. New-UI capture
adds Details/Sources views at 375/1440. The selected paused run has saved evidence
and no answer/artifacts; the partial run has canonical text and persisted partial
report artifacts. Failed/blocked, empty Chat and Workspace are also inspected.

The original before run was captured and reviewed early in the work, but its
temporary directory was no longer present when archiving. The retained reference
therefore uses immutable **`7e0901c` compiled frontend assets** with the current
real API on 4178. It is explicitly reconstructed, not a recovered original or a
time/database snapshot. The temporary reference server was stopped after capture.
Earlier new-UI captures exposed defects and are superseded by the retained final
manifest; they are not presented as accepted evidence.

Final built assets:

- `index-BZ4TRU_s.js`
- `index-DSp9ij_S.css`
- `WorkspaceRoute-4c_C3rLI.js`

The preview serves those actual production assets. The 8080 embedded binary was
managed by concurrent runtime work and was observed serving an earlier Phase 5
asset set; this report does not label preview screenshots as proof of the final
embedded-Go deployment. The final frontend is built and usable on 4177; embedding
these latest assets requires the normal Go build/restart at an appropriate idle
point. No runtime source changes are included in this frontend implementation.

### Screenshot-driven corrections

Actual image review found and corrected three problems that a root-overflow check
alone missed: a mobile title compressed between actions, a roughly 80px report
reading sliver, and clipped/overlapping Workspace navigation at 320/375px.
Dedicated browser regressions and rebuilt live screenshots verify the fixes.

| View | Old-UI reference | Accepted new UI |
| --- | --- | --- |
| Mobile paused | [reference](experiments/phase5/reference-before-paused-375.png) | [new](experiments/phase5/after-paused-375.png) |
| Mobile partial report | [reference](experiments/phase5/reference-before-partial-375.png) | [new](experiments/phase5/after-partial-375.png) |
| Desktop partial report | [reference](experiments/phase5/reference-before-partial-1440.png) | [new](experiments/phase5/after-partial-1440.png) |
| Mobile Workspace | [reference](experiments/phase5/reference-before-workspace-320.png) | [new](experiments/phase5/after-workspace-320.png) |

## Hallmark review and limitations

The [58-gate review](deep-research-phase5-hallmark-audit.md) records Pass/NA/Limited
per gate, rather than inventing “58/58”. Self-critique: **P4 H4 E3 S5 R4 V4**.
App consistency intentionally overrides theme rotation. Selected token contrast
pairs meet the documented thresholds; this is not a full computed-style or
accessibility certification. Inline prose/citation links can be below 44px,
native-title timing is browser-controlled, and legacy component states remain
partially audited. The build also reports the existing-class >500kB JS-chunk
advisory; no broad bundle/performance claim is made.

## Not established / next gate

Phase 5 initiates no real model task and performs no live save/cancel/delete as
verification. Research usefulness, independent factual accuracy, sustained
resource-safe completion and the 15m → 60m → 4h sequence are not established by
these frontend results. Phase 4 remains skipped, not passed. Stop after this
frontend phase's report and commit; await the user's next phase instruction.
