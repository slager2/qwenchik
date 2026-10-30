<!-- Hallmark · pre-emit critique: P5 H5 E4 S5 R5 V4 -->
# Deep Research Phase 5 — frontend implementation plan

Phase 4 is explicitly skipped at the user's request; its useful-research gate
has not passed. Starting commit: `7e0901c`. The user confirmed the task-first,
calm technical Cobalt direction and requested parallel agents and Hallmark.

## Product decisions

1. Keep Chat as the primary task/reading surface, the conversation sidebar for
   navigation, and Workspace for the board/approvals/memory.
2. Put a compact factual research overview in the center. It answers: what is
   happening, what is saved, what has not been delivered, what can happen next.
3. Reorder Details around task outcomes and outputs before diagnostics. Display
   active round plans when supplied, separate completed/skipped steps and avoid
   presenting plan processing as report completion.
4. Keep the canonical inline answer. Improve reading/navigation of long Markdown
   reports and source inspection without creating a second competing answer.
5. Make settings for the next run available in a quiet disclosure: profile,
   time ceiling and model generation settings. Preserve explicit zero temperature.
6. Never open a mobile drawer as a side effect of changing mode/submitting/loading.
   Preserve the existing native dialog's keyboard containment and focus return.
7. Replace background Doctor with passive health/configuration. Explicit checking
   may invoke Doctor; opening a page does not.
8. Localize current pause/resource reasons and share resume eligibility between
   Chat and Workspace. Unknown conditions are explained, not silently resumed.

## Design and ownership

Shared system: root `design.md`; existing palette/fonts, task-first Workbench,
N3 side navigation, Ft2 quiet footer, no enrichment. CSS is additive to the
existing global styles and all new colors/fonts use named tokens.

| Owner | Files / responsibility |
| --- | --- |
| Integration | `ChatRoute.tsx`, `WorkspaceRoute.tsx`, `api.ts`, `types.ts`; passive configuration, settings persistence, overview/action wiring, explicit drawers, existing synchronization |
| Research | `ResearchInspector.tsx`, new `ResearchOverview.tsx`, `run-presentation.mjs/.d.mts`, tests; truthful run/output/plan/source presentation and shared action semantics |
| Conversation | `ChatComposer.tsx`, `ChatTranscript.tsx`, `ChatSidebar.tsx`, `MarkdownMessage.tsx`, new `ResearchSettings.tsx`, settings/outline helpers and tests |
| Visual system | root `tokens.css`, `style.css`, `chat.css`, `TelemetryPanel.tsx/.css` and tests; contrast, hierarchy, target sizes, readable provenance, responsive layout |
| Browser verification | existing browser fixture tests and new `tests/phase5-capture.mjs`; real read-only before/after capture and responsive interaction assertions |

No file/route deletions or new queue/DB/orchestrator. The `.mjs` helper declaration
and tests stay aligned. Presentational components do not add fetch loops.

## Required states

- New conversation; mode/profile selection; settings validation and save failure.
- Sending, rejected submission, canonical acceptance without duplicate text.
- Running/streaming, reconnecting, terminal polling and replay.
- Manual pause, resource guard pause, resource-held manual pause, uncertain-call
  recovery, context mismatch/unverified capacity, unavailable telemetry.
- Failed/blocked with no answer; interrupted/partial saved text; delivered partial
  report with gaps; persisted report with safe links and section navigation.
- Sources loading/error/empty, discovered/fetching/rejected/extracted/cited,
  historical run isolation and evidence failure retry.

## Verification and evidence

Before capture uses the real existing app, with Doctor and every mutation blocked
before navigation; real allowed GET responses remain unchanged. The authoritative
initial baseline was `/tmp/opencode/phase5-before-verified/manifest.json`: 35 screenshots
across 320/375/414/768/1024/1280/1440 widths, 35 automatic Doctor calls blocked,
zero mutations/external requests/page errors. The earlier incomplete preliminary
capture is not baseline evidence.

At final archiving that temporary baseline directory was no longer present. The
retained old-UI reference is explicitly reconstructed from `7e0901c` compiled
assets with the current real API, not represented as recovered original evidence.
See the completion report and `docs/experiments/phase5/README.md` for provenance.

After implementation: frontend unit tests, nonincremental TypeScript, embedded
build, mocked Chrome scenarios and production chunk tests; Go checks appropriate
to the embedded frontend. Deploy and capture the same real saved conversations
again. Require zero automatic Doctor and mutating requests, check child bounds,
44px controls, short height, keyboard navigation, focus return, reduced motion,
and explicit Details. Review actual images rather than equating automation with
visual acceptance. Classify scripted versus real/live evidence separately.

Results, screenshots selected for retention, remaining limitations and final
Hallmark self-critique will be recorded in the Phase 5 report. The UI must not
imply that the Phase 3 model-RSS/research-completion limitation was repaired.

## Completion gate

Commit the coherent Phase 5 slice and stop before Phase 6. Do not run a new
research benchmark, useful-research task, or 15m/60m/4h soak as UI verification.
