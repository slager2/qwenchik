# Redesign baseline — Phase 0

Captured on 2026-11-01 from the locally served application at `127.0.0.1:8080`. The capture blocks every non-GET/HEAD request, external origin, and `/api/doctor`, so it cannot create runs, modify conversations, or leak source URLs.

## Evidence

The nine screenshots and their read-only manifest are in [`phase-0/before`](phase-0/before/): chat, Projects (currently Spaces), and Tasks (currently Workspace) at 1440×900, 1024×768, and 375×812. The reusable capture harness is [`frontend/tests/redesign-capture.mjs`](../../frontend/tests/redesign-capture.mjs).

## Computed UI baseline

The first fifteen visible controls were measured in the desktop chat by Playwright. Values are browser-computed rather than inferred from a stylesheet.

| Element | Font | Size / line-height | Padding | Box |
| --- | --- | --- | --- | --- |
| Brand link | Space Grotesk Variable | 20px / normal | 0 8px | 215×26 |
| New-chat button | Inter Variable | 14px / normal | 8px 12px | 215×44 |
| Conversation search | Inter Variable | 14px / normal | 10px 12px | 199×44 |
| Conversation row 1 | Inter Variable | 14px / normal | 8px 16px | 215×56 |
| Conversation row 1 metadata | Inter Variable | 12px / 18px | 0 | 183×18 |
| Conversation row 2 | Inter Variable | 14px / normal | 8px 16px | 215×56 |
| Conversation row 2 metadata | Inter Variable | 12px / 18px | 0 | 183×18 |
| Conversation row 3 | Inter Variable | 14px / normal | 8px 16px | 215×56 |
| Conversation row 3 metadata | Inter Variable | 12px / 18px | 0 | 183×18 |
| Conversation row 4 | Inter Variable | 14px / normal | 8px 16px | 215×56 |
| Conversation row 4 metadata | Inter Variable | 12px / 18px | 0 | 183×18 |
| Conversation row 5 | Inter Variable | 14px / normal | 8px 16px | 215×56 |
| Conversation row 5 metadata | Inter Variable | 12px / 18px | 0 | 183×18 |
| Conversation row 6 | Inter Variable | 14px / normal | 8px 16px | 215×56 |
| Conversation row 6 metadata | Inter Variable | 12px / 18px | 0 | 183×18 |

The measurement confirms the typography split: the brand uses Space Grotesk while all Cyrillic UI text falls back to Inter/Segoe UI. This is the concrete reason for the IBM Plex migration in Phase 1.

## Baseline verification

Executed sequentially after `npm --prefix frontend ci`:

- `npm --prefix frontend run typecheck` — passed.
- `npm --prefix frontend test` — passed: 121 tests.
- `npm --prefix frontend run test:browser` — **baseline failure**: the existing Research overview profile `<select>` measures 135×34px at 320px wide, below the project’s 43.5px touch-target assertion. This is an existing defect, not hidden; Phase 1/3 replaces it with an accessible 44px segmented control and keeps the assertion.
- `npm --prefix frontend run build` — passed. The initial bundle is 533.49 kB (160.54 kB gzip), which is another Phase 1–7 reduction target.

## Known redesign constraints

- Preserve backend contracts, `*.mjs` state modules, and ChatRoute’s SSE/hooks.
- Keep the accessible `Задача исследования` textbox name, `Детали` trigger, `ContextNotice[role=alert]`, assistant `data-message-id` and `tabIndex=-1` contracts until deliberately updated with their tests.
- The existing CSS has four separate ownership layers, duplicate token definitions, non-motion `!important`, and legacy 8–11px rules. Phase 1 moves to frontend-local canonical tokens and removes overrides rather than adding another correction layer.
