# Design — Qwenchik

Shared visual system for the local research agent. This document replaces the
earlier "Cobalt Workbench" direction. The user approved a full visual redesign,
including deleting or splitting legacy routes' components and stylesheets.

## Direction

- Calm minimalism in the spirit of ChatGPT / Claude: one reading column, quiet
  chrome, content first. No dashboard tiles, no pill/box clutter, no eyebrow
  labels in capitals, no decorative gradients.
- Audience: one person using a local agent for chat and Deep Research.
- Tone: calm, specific, technical. Interface copy is Russian; source text is
  shown unchanged.

## Layout

- **App shell**: collapsible left sidebar (new chat, primary navigation —
  Чат / Проекты / Задачи, route-specific list such as conversations or
  projects, then theme and settings at the bottom). Collapsed state is a 64px
  icon rail and persists in `localStorage` (`qwenchik.sidebar.collapsed`).
- **Top bar** per route: title on the left, at most three quiet actions on the
  right. On mobile (≤ 768px) the sidebar becomes a left sheet opened from the
  top bar menu button; the sheet never covers the full width.
- **Chat**: centered column, `--measure-chat` (760px) max width. Transcript
  scrolls on desktop; on mobile the whole conversation surface scrolls. The
  composer is a single rounded field anchored below the transcript.
- **Details** (research plan, sources, telemetry, diagnostics) live in a right
  panel (360px) on wide screens (> 1120px) and a right drawer below that. They
  never open automatically.
- **Workspace / Projects**: same shell, content constrained to 1120px, lists
  instead of boxed grids where possible. Task details use the same right
  panel pattern.

## Tokens

Canonical tokens live in `frontend/src/styles/tokens.css`. Light and dark are
defined once with `light-dark()`; `data-theme="light|dark"` on `<html>` forces
`color-scheme`, otherwise the OS preference applies.

| Role | Light | Dark |
| --- | --- | --- |
| `--bg` page | `#ffffff` | `#1a1a1a` |
| `--bg-subtle` sidebar, panels | `#f7f7f8` | `#202020` |
| `--bg-muted` hover, code, user bubble | `#f0f0f2` | `#2a2a2a` |
| `--text` | `#1b1b1f` | `#ececec` |
| `--text-secondary` | `#55565c` | `#b4b4b4` |
| `--text-tertiary` | `#73747b` | `#8e8e8e` |
| `--border` | `#e6e6ea` | `#333333` |
| `--border-strong` | `#d3d3d9` | `#454545` |
| `--accent` | `#3b5bdb` | `#8ea4ff` |

- One accent color, used for the primary action, links, focus and the active
  state of selection. Never as a large background.
- Status colors (`--success`, `--warning`, `--danger`) are used for small text
  or a 6px dot, always with a text label.
- Type: Inter (variable, local `@fontsource`), mono is IBM Plex Mono.
  Scale: 13 / 14 / 15 / 16 / 20 / 24px (`--text-xs … --text-2xl`). Reading
  text 16px / 1.7; UI text 14–15px. Nothing below 12px.
- Spacing: 4pt grid (`--space-1` = 4px … `--space-12` = 48px).
- Radii: `--radius-sm` 8px (controls), `--radius-md` 12px (cards, menus),
  `--radius-lg` 20px (composer), `--radius-full` for icon buttons.
- Borders are 1px `--border`. Shadows only on floating layers (`--shadow-md`
  for menus/drawers, `--shadow-sm` for the composer).
- Focus ring: 2px `--focus` outline with 2px offset, on every interactive
  element via `:focus-visible`.

## Components

`frontend/src/ui/` holds the primitives: Button, IconButton, Input, Textarea,
Badge, Card, Drawer, Tabs, Tooltip, EmptyState, Spinner. Styles are in
`frontend/src/styles/components.css` with `ui-` prefixed classes. Routes use
these instead of bespoke button/box styles.

- Buttons: `primary` (accent fill), `secondary` (bordered), `ghost`
  (transparent), `danger` (danger text). Every touch target is at least 44px;
  visual weight comes from color, not size.
- Badges are text with an optional status dot, not filled pills.
- Cards are used for genuinely separate objects only (approval, memory entry).

## Interaction and motion

- Native `<dialog>` for drawers and modals; Escape closes, focus is trapped and
  returned to the trigger.
- Motion: opacity/transform only, 120–180ms, `--ease-out`. Reduced motion
  disables spatial motion.
- Composer: auto-growing textarea, Enter sends, Shift+Enter inserts a newline,
  IME composition never sends. While a run is active the send button becomes
  a stop button.
- Research progress is a single quiet line under the question
  ("Поиск источников · 3 из 7 этапов") that expands into steps and sources.

## State and content truth

- Run lifecycle, report delivery and evidence readiness are different
  dimensions; an internal PASS without artifact IDs is not a delivered report.
- Unknown/stale data is shown as unavailable, never as zero.
- Optimistic sending is not failed sending; keep idempotency keys, canonical
  message reconciliation, SSE replay and fallback.
- Opening Chat/Workspace must not call the inference-capable Doctor endpoint.
- Settings apply to the next run only.
- Citations are footnote markers linking to real evidence URLs; a link shows
  where material came from, not that each claim is verified.

## Implementation boundaries

- React 19 + TypeScript + Vite; plain CSS with tokens. Bundle is embedded into
  the Go binary from `internal/api/web/dist`.
- State logic stays in the `.mjs` modules (conversation-state, research-state,
  chat-sync, evidence-cache, …) with their tests; the API client and types are
  the backend contract and are not changed by visual work.
- Components and stylesheets may be split, replaced or deleted when the
  redesign makes them obsolete. No external fonts or CDN resources.
