<!-- Hallmark · pre-emit critique: P5 H5 E4 S5 R5 V4 -->
# Design — Qwenchik

Confirmed by the user for Deep Research Phase 5. Shared system for the existing
application; extend this document rather than selecting a new theme per route.

## Audience, purpose and tone

- Audience: the user of a local research agent, who needs readable answers and
  control over saved work.
- Primary job: ask a question or start research, then read the saved result or
  understand the recorded interruption and available next action.
- Tone: calm, specific, technical; Russian interface copy, source text unchanged.
- Genre: modern-minimal. Theme: existing Cobalt, retained for app consistency.

## Structure

- App family: task-first Workbench adaptation. This is the actual application,
  not a marketing tour or a simulated interface.
- Navigation: N3 side rail/sidebar. Existing `#/chat`, conversation URLs and
  `#/workspace` stay authoritative. Workspace keeps tasks, approvals and memory.
- Conversation: task header → factual research overview → canonical transcript
  and saved answer → composer → quiet connection footer (Ft2 inline-rule).
- Details: run outcome/actions → report availability → current plan/round →
  sources/materials → disclosed configuration, resources and event diagnostics.
- Mobile: one primary reading surface. Navigation and Details open explicitly
  in the existing native dialogs; changing mode or loading research never opens
  a modal automatically. Escape, focus containment and return focus are required.
- No hero, enrichment, fake browser chrome, decorative dashboard tiles or
  hardcoded benchmark statistics in product screens.

## Typography and rhythm

- Display: Space Grotesk Variable, 500–650, roman, functional 18–28px headings.
- Body: Inter Variable, 400–600; reading text 15–16px, line-height 1.65–1.75,
  approximately 68–76 characters per line where viewport space permits.
- Technical values/code: JetBrains Mono, 400–500. Long hashes are abbreviated
  accessibly with a full copy target.
- Four-point spacing: 4, 8, 12, 16, 20, 24, 32, 40px; new scale extensions and
  size/easing tokens live in `tokens.css`. Preserve existing CSS token names.
- Control radius 6px; content radius 10px. Every touch control has a 44px target.
- Headings wrap within their container; interactive labels stay on one line.

## Palette

Existing semantic OKLCH roles remain the visual anchor, from
`frontend/src/style.css`:

| Token | Value |
| --- | --- |
| `--color-paper` | `oklch(98.5% 0.004 250)` |
| `--color-paper-raised` | `oklch(99.7% 0.002 250)` |
| `--color-paper-muted` | `oklch(96.5% 0.007 252)` |
| `--color-ink` | `oklch(24% 0.02 258)` |
| `--color-ink-2` | `oklch(34% 0.018 257)` |
| `--color-accent` | `oklch(58% 0.2 256)` |
| `--color-rule` | `oklch(89% 0.012 255)` |
| `--color-focus` | `oklch(58% 0.2 256)` |

Use color through semantic tokens only. Cobalt signals the primary action and
focus, not large background floods. Aim for ≤5% accent coverage per viewport.
Warnings/errors have explicit text and do not rely on color. New text tokens may
deepen contrast without replacing the established palette.

## State and content truth

- Run lifecycle, report delivery and evidence readiness are different dimensions.
- An internal PASS or completed report checkpoint without artifact IDs is not a
  delivered report. Skipped steps are not successful steps. Progress refers to
  processed plan work, never a fabricated answer-completion percentage or ETA.
- Unknown/stale data is explicitly unavailable/last-observed, not measured zero.
- A resource-held manual pause is distinguished from automatic resource recovery
  and uncertain-call recovery. Backend remains authoritative for mutations.
- Evidence/source ownership is not independent factual accuracy. Preserve source
  failures, retry availability, URLs, excerpts and provenance.
- Optimistic sending is not failed sending. Keep idempotency keys, canonical
  message reconciliation, historical citation isolation, SSE replay and fallback.
- Opening Chat/Workspace must not call inference-capable Doctor. Model/provider
  readiness checks are explicit user actions; passive configuration is labelled.
- Settings apply to a next run, never enlarge an existing run's pinned budget.
  Time profiles are ceilings, not duration or completion guarantees.

## Interaction and motion

- All controls cover default, hover, focus, pressed, disabled, loading, error
  and successful/persisted states where applicable. Success is quiet.
- Native controls and native `<dialog>`; visible labels, linked explanations,
  immediate focus rings with at least 3:1 contrast, constant border widths.
- Animate only opacity/transform with named `--ease-out`, `--ease-in`,
  `--ease-in-out`. No bounce, background polling spinners or decorative motion.
- Reduced motion: no spatial motion; at most 150ms opacity transitions.
- Root `overflow-x: clip`; verify child bounds independently so clipping cannot
  hide inaccessible controls. Required widths: 320, 375, 414, 768, 1024, 1280,
  1440px, plus short viewport, enlarged text and reduced-motion checks.

## Exports and implementation boundaries

- Core existing token block: `frontend/src/style.css` (retained for compatibility).
- Additive CSS system export: root `tokens.css`, imported by the frontend.
- Existing React/TypeScript/Vite and Go-embedded asset build remain in place.
- Extend components in place; no route/component deletion or new backend system.
- Documentation is not copied into application content. No fabricated sources,
  scores, active states, performance claims or testimonials.
