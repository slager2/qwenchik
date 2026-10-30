<!-- Hallmark · final review: P4 H4 E3 S5 R4 V4 · designed-as-app · limited verification, not 58/58 -->
# Deep Research Phase 5 — Hallmark Step 7 audit

Reviewed on 2026-10-29, after the parent reported the build and test suites complete.
**Verdict: the Phase 5 reading/interaction slice is materially improved. The
mobile Workspace navigation clipping/overlap defect found during review is now
corrected and visually confirmed in the accepted rebuilt captures at 320/375px;
the parent reports the corresponding mock checks passed. No outstanding critical
visual blocker was identified in the reviewed accepted images. This is not a
58/58 certification or a whole-app accessibility certification.**

## Scope, authority and evidence

The user-confirmed [design.md](../design.md#L16-L42) governs this existing app:
modern-minimal, existing Cobalt, task-first Workbench, N3 sidebar and quiet Ft2
connection footer. App consistency overrides catalog diversification; the
multi-page rule in `references/verbs/redesign.md` §§3–4 also explicitly exempts
`designed-as-app` outputs from rotation gates. The work preserves the actual
product and saved content rather than creating a marketing page.

Post-build references read for this review:

- `/home/slager/.agents/skills/hallmark/references/slop-test.md`
- `/home/slager/.agents/skills/hallmark/references/contract.md`

Evidence keys used below:

| Key | Evidence inspected |
| --- | --- |
| S1 | [Accepted partial, 375px](experiments/phase5/after-partial-375.png) |
| S2 | [Accepted paused, 375px](experiments/phase5/after-paused-375.png) |
| S3 | [Accepted partial, 1440px](experiments/phase5/after-partial-1440.png) |
| S4 | [Accepted Details, 375px](experiments/phase5/after-partial-375-details.png) |
| S5 | [Accepted Sources, 1440px](experiments/phase5/after-partial-1440-sources.png) |
| S6 | [Accepted Workspace, 375px](experiments/phase5/after-workspace-375.png) |
| S7 | [Accepted Workspace, 320px](experiments/phase5/after-workspace-320.png) |
| M | [Accepted after manifest](experiments/phase5/after-manifest.json) |
| B | [Reconstructed reference-before manifest](experiments/phase5/reference-before-manifest.json) |
| C | [Phase 5 conversation CSS](../frontend/src/chat.css#L196-L409) |
| G | [Additive global CSS, including the Workspace correction](../frontend/src/style.css#L499) |
| T | [Root tokens](../tokens.css) |
| I | [Telemetry rendering and states](../frontend/src/TelemetryPanel.tsx) and [CSS](../frontend/src/TelemetryPanel.css) |
| F | [Settings and validation](../frontend/src/ResearchSettings.tsx) |

These seven accepted repository images were actually opened, not inferred from filenames. S1/S2 show
the corrected two-row mobile header. S1 shows the saved report and its section
outline in natural reading flow; S2 honestly shows a paused run with no answer.
S3 shows the report heading and summary instead of the former tiny metadata-only
reading slot. S4 distinguishes a saved partial report, report gaps, evidence
readiness and plan processing. S5 shows readable source provenance in the
explicit desktop details panel. S6/S7 retain the existing Workspace board while
putting services behind a quiet disclosure. Both show all four navigation labels
inside the viewport, separate from the brand/runtime row, without the previous
glyph/text overlap. The four equal-width tracks are 73px at 320px and 86.75px at
375px with the declared spacing; each nav target has a 44px minimum height in G.
The parent reports the 44px/bounds mock checks passed. Pixel images alone are not
a measurement of every interactive hit box.

M records **39 screenshots**, widths 320/375/414/768/1024/1280/1440, Chrome
153.0.8010.36, 900px height, Russian locale, UTC and reduced motion. Its base URL
is **`http://127.0.0.1:4177`**: the source-app preview against the existing real
8080 API, not proof that a newly embedded Go frontend was deployed on 8080.
The parent identifies the API arrangement; M itself records the preview URL and
real GET policy. Its accepted run originated at `/tmp/opencode/phase5-accepted`
and is retained as M. It reports 720 requests, zero blocked Doctor attempts, zero
mutations, zero external-origin attempts, `errors: []`, and `result: passed`.
Doctor and mutations were blocked by policy; zero Doctor attempts is therefore
evidence of no automatic Doctor request, not a successful Doctor check.
The parent reports zero page/console errors; inspected manifest entries agree.
No capture, service, inference, mutation or new benchmark was run by this audit.

Accepted asset identities include `index-DSp9ij_S.css`, `index-BZ4TRU_s.js` and
`WorkspaceRoute-4c_C3rLI.js`, as recorded by the parent and accepted manifest.
Earlier rejected `/tmp/opencode/phase5-after-final` images exposed the Workspace
rail defect; those images are historical discovery evidence, not the accepted
after evidence. The current links above refer to the rebuilt accepted set.

**Reference-before provenance:** the original temporary before capture is no
longer available. B is an explicitly reconstructed reference: immutable committed
UI assets from **`7e0901c`**, served at **4178** against the **current real API**.
It records **35 screenshots, 855 requests, 35 Doctor attempts blocked, zero
mutations and zero external-origin attempts**. Selected images use the repository
prefix `reference-before-`. This is **not a recovered original capture** and not
a temporally paired original-before/after experiment. The manifests' runtime
source revision does not change the declared old-asset reference revision.
Expected browser errors from blocked Doctor requests in B must not be described
as zero console/request errors. Its top-level capture result is passed with
`errors: []`; that is a different claim.

The manifest calls layout findings observations, explicitly excluding a blanket
accessibility assertion. Screenshot existence is not equivalent to inspecting
every one of the 39 images. Selected visible conversations/source excerpts are
synthetic lab cases; sidebar data comes from the real local database. This audit
does not reproduce IDs or full responses, and does not publish the evidence.
Selected repository screenshots and the main completion report are parent-owned;
this audit updates references without modifying capture evidence.

## Six-axis self-critique

| Axis | Score | Reason |
| --- | --- | --- |
| Philosophy (P) | 4 | Reading saved work and understanding interruptions are the primary jobs. |
| Hierarchy (H) | 4 | Header, factual overview, report and explicit details have distinct roles; mobile overview still consumes meaningful vertical space. |
| Execution (E) | 3 | The inspected layout is usable and major reading regressions are corrected; inline targets, legacy states and complete computed contrast remain limitations. |
| Specificity (S) | 5 | Resource-held pause, saved partial report, source ownership and next-run settings are specific to this local research app. |
| Restraint (R) | 4 | No enrichment or decorative motion; diagnostics are disclosed. Existing Workspace chrome and some repeated factual status copy remain. |
| Variety (V) | 4 | Deliberate within-system variation: history, conversation, source inspector and task board serve different functions while retaining the locked Cobalt system. This is not a claim of theme rotation or structural distance from a different site. |

All scores meet the skill's floor of 3. These are qualitative review scores,
not measured performance/accessibility metrics.

## Interaction and contrast evidence

- **Default / hover / focus / pressed:** C defines named-easing transform feedback,
  hover/active surfaces, and instant 2px `:focus-visible` outlines. Open disclosure
  content allows focus outlines to escape its collapse wrapper. Native details
  remain native rather than simulating a different control.
- **Disabled / loading:** native `disabled`, cursor and opacity are used for
  applicable controls. Pending copy/save/send operations expose text. Non-applicable
  states are not invented for ordinary links or native summaries.
- **Error / successful-persisted:** F links invalid fields and textual validation;
  I exposes copy failure/success, resetting successful copy feedback after 2.5s.
  Saved invocation and report states are distinct from live measurements. Existing
  focused telemetry/state tests passed 15/15 earlier; the isolated Telemetry TS
  check passed. The parent's broader test completion is reported separately, not
  re-run or assigned invented counts here.
- **Touch:** major buttons, summary/disclosure controls, close/copy controls and
  form controls have a 44px target in C/G/I. **Inline citation/source/download
  links can remain smaller than 44px.** The manifest's small-target observation
  does not certify these links or every legacy control.
- **Motion:** Phase 5 disclosure geometry is instant; remaining new motion uses
  transform with named easings. Reduced-motion overrides make this slice static.
  M used reduced motion, so screenshots cannot demonstrate the normal-motion
  transition timing or all hover/pressed combinations.
- **Tooltips:** retained `title` attributes are browser-native, not custom
  800ms-hover/0ms-focus tooltips. No timing guarantee is claimed for native titles.
  Essential stale/unknown/resource information is visible text, not title-only.

The earlier local WCAG ratio calculation converted the declared OKLCH values to
linear-sRGB relative luminance. It was **token-pair arithmetic**, not a browser
computed-style sweep or an automated assertion for every rendered node:

| Pair from T | Raised / paper / muted / pressed | Threshold |
| --- | --- | --- |
| `--color-readable` on four working surfaces | 7.69 / 7.43 / 7.01 / 6.61:1 | 4.5:1 text |
| `--color-focus` on those surfaces | 4.34 / 4.19 / 3.96 / 3.73:1 | 3:1 focus |
| `--color-control-rule` on those surfaces | 3.76 / 3.63 / 3.42 / 3.23:1 | 3:1 boundary |
| `--color-success-ink` on those surfaces | 7.06 / 6.82 / 6.43 / 6.06:1 | 4.5:1 text |
| `--color-accent-ink` on `--color-accent-dark` | 6.51:1 | 4.5:1 text |

The original bright accent with accent-ink was approximately 4.26:1; the Phase 5
primary controls use the existing dark accent instead. Legacy muted/faint text,
disabled composites, browser-native elements and all warning/status combinations
have not received an exhaustive computed-style contrast audit. Earlier checking
matched 37 distinct added CSS token references to the reviewed root inventory;
it was not a full CSS dependency or dynamic contrast analyzer.

## 58-gate checklist

The reference contains gates **1–57 plus 38a**, totaling 58. **Pass** means no
listed tell found in the scoped source/evidence reviewed. **NA** means the
feature is absent or the locked app exception applies. **Limited** means partial
verification, a known inherited constraint, or an explicit departure from a
literal catalog rule; it is not counted as a pass. No aggregate pass fraction is
claimed.

| Gate | Status | Scoped finding / evidence |
| --- | --- | --- |
| 1 Display default | Pass | Space Grotesk display token, paired with Inter body; T, S1–S5. Cyrillic/fallback font rendering is not a per-glyph font audit. |
| 2 Gradient | Pass | No gradient background/headline introduced; flat cool surfaces in S1–S6. |
| 3 Equal icon-tile grid | Pass | Functional report/source/board layouts, no marketing three-tile feature grid; S1–S6. |
| 4 Card-in-card | Limited | New source rows are flattened; semantic warning/report containers remain. Legacy Workspace containment is not wholly restructured; C/G, S4/S6. |
| 5 Side-stripe card | Limited | Phase 5 provider/source rows use hairlines. Inherited health/approval CSS still exists; no claim of eliminating every legacy stripe; G. |
| 6 Centered full-viewport hero | NA | No hero; full-height shell is the actual application, not a promotional composition. |
| 7 Pure black/white base | Pass | Working base surfaces are tinted OKLCH tokens; T. No whole-dependency stylesheet assertion. |
| 8 Repeated macrostructure | NA | `designed-as-app`: locked Workbench consistency explicitly overrides rotation; design.md, redesign reference. |
| 9 Uniform whitespace rhythm | Pass | Header/outline/source hairlines and differentiated reading/control spacing; S1/S3/S5. |
| 10 Transition-all | Pass | None introduced in the scoped additive layer; C/G/I. This is not a scan of every dependency/legacy style. |
| 11 Universal hover scale | Pass | No universal hover-scale; C/G. |
| 12 Bounce/overshoot | Pass | Named exponential easings, no overshooting UI curve in new tokens/layer; T/C. |
| 13 Multiple hover effects | Limited | New controls can combine immediate surface feedback with transform feedback; inherited Workspace hover rules also remain. Not a literal whole-app single-effect pass. |
| 14 Layout-property animation | Pass | Effective Phase 5 collapse geometry is instant; transform-only new transitions; C/I. Older overridden declarations are retained by append-only policy. |
| 15 Animated focus ring | Pass | Focus outline changes are immediate; new transitions do not include outline; C/G/I. |
| 16 Celebratory success toast | Pass | Inline copy/save feedback, no new celebratory toast; I/F. |
| 17 Equal tooltip delays | NA | No custom tooltip implementation. Browser-native titles do not implement or prove the custom timing recipe. |
| 18 Unpaused auto-rotation | NA | No carousel, rotating banner or decorative statistic rotation. |
| 19 Placeholder/cliché names | Pass | Inspected app/data use product and synthetic lab names, not invented testimonial identities; S1–S6. |
| 20 Missing stamp | Pass | Root tokens start with Workbench, genre/tone/hue and `design-system: design.md`, `designed-as-app`; T. Global stylesheet keeps import first. |
| 21 Specimen fall-through | Pass | No huge serif/numbered editorial skeleton; S1–S6. |
| 22 Untinted neutral | Pass | Declared working surface tokens retain cool chroma; T. Modern-minimal also permits neutral exceptions. |
| 23 Accent flood | Limited | Inspected frames use small action/navigation signals, without large blue bands. No pixel-area calculation proves ≤5% for every viewport/state. |
| 24 Unnamed spacing | Limited | Additive spacing aliases follow the existing four-point system; inherited raw pixel/optical values remain. No blanket spacing-normalization claim. |
| 25 Prose measure | Pass | Main reading measure is 70ch where width permits; C/T, S3. Mobile and functional source/details columns necessarily become narrower; S1/S5. |
| 26 Missing control states | Limited | Eight-state handling is evidenced for applicable new forms/copy/actions as described above. Inline links and all inherited controls are not exhaustively state-certified; C/G/I/F. |
| 27 Missing reduced-motion coverage | Pass | New scoped transform feedback has static reduced-motion overrides; C/G/I. Normal-motion timing was not visually exercised in M. |
| 28 Video/LCP | NA | No video or media enrichment added. |
| 29 Abstract background | NA | No abstract/mesh/animated background. |
| 30 Icon tells | Limited | No new mixed icon library or decorative emoji tiles. Existing Unicode menu/arrows/check marks and legacy Workspace glyphs remain; S1/S6. |
| 31 Lottie shortcut | NA | No illustration or Lottie dependency introduced. |
| 32 Variation knob rotation | NA | App-consistency exemption; shared theme/fonts/navigation intentionally retained. |
| 33 Unnamed decorative SVG | Pass | Telemetry chevron is explicitly `aria-hidden`; no enrichment artwork; I. Not an audit of all historical SVG assets. |
| 34 Horizontal layout | Limited overall; scoped navigation fix: Pass | Earlier rejected Workspace frames exposed clipped Memory/status and glyph/text overlap despite root-width observations. The additive ≤48rem two-row/four-track correction is visually confirmed in accepted S6/S7: all four labels fit, brand/runtime are separate, navigation targets have 44px minimum height; parent reports bounds/target mock checks passed. This resolves the known nav blocker, not every possible clipping case. M samples seven widths through 1440, not the full continuous range through 1920, zoom or every child/scroll state; root clipping alone remains insufficient proof. |
| 35 Decorative text positioning | NA | No highlighter bands or decorative heading strokes introduced. |
| 36 Bar alignment | Pass | Action rows align controls; mobile header deliberately uses a two-row grid rather than forcing title into an action row; C, S1–S5. Not a literal `line-height:1` assertion for every legacy bar. |
| 37 More than three families | Pass | Display/body/technical trio only in the shared system; T. |
| 38 Outlier overuse | Limited | Locked technical mono register covers code/hashes/data, potentially more than two instances. A literal two-slot decorative-outlier rule is not certified for this functional app. |
| 38a Italic heading | Pass | Phase 5 functional/Markdown headings are roman, including heading emphasis override; C/G, S1/S3. |
| 39 Input-state correctness | Limited | Constant field borders, outline focus, 44px floors, native disabled, linked errors and helper minimum in F/C. No exhaustive legacy/browser-native-state or geometry audit. |
| 40 All computed contrast | Limited | Selected declared-token pairs meet thresholds above. Not all dynamic computed foreground/background pairs have been measured. |
| 41 Fill/text mistakes | Limited | New primary dark-accent pair is 6.51:1 with accent-ink; no ink-on-ink in inspected frames. Original bright-accent/legacy/disabled combinations are not universally certified. |
| 42 AI nav | Pass | Real N3 history/task navigation, not marketing wordmark + feature links + CTA; S3/S6. |
| 43 AI footer | Pass | Quiet connection/help footer, no four-column invented sitemap; C and app plan. |
| 44 Hero fold | NA | No hero. Composer below the fold is intentional when reading saved reports, not a missing marketing CTA. |
| 45 Unmotivated decoration | Pass | No decorative hero/cursor/blob/stat ornaments introduced. |
| 46 Fabricated metrics | Pass | Reviewed counts/statuses are saved data with explicit limitations, not promotional claims; S2/S4/S5. UI review does not prove report factual accuracy or research usefulness. |
| 47 Fake chrome | Pass | Actual app UI/native dialog, no imitation browser/phone/terminal frame; S1–S6. |
| 48 Token improvisation | Pass | New colors/fonts use shared token roles; T/C/G/I. Existing core block retained, not shadow-replaced by arbitrary inline colors. |
| 49 Two-line affordances | Limited | Main buttons/outline labels stay single-line in reviewed evidence; source and report links can wrap as content. Inline links may be under 44px, and all widths through 1920 were not continuously reviewed; S4/S5. |
| 50 Image grid min tracks | NA | No image-bearing grid introduced; Markdown images are safe textual labels. New settings/header grids use minmax(0,1fr). |
| 51 Long heading wrap | Pass | Functional headings have min-width:0 and overflow-wrap:anywhere; mobile title now gets its own column; C/G, S1/S2. |
| 52 Section-head collapse | Pass | No theme-specific multi-column eyebrow/heading override introduced. Mobile header actions are separate from the title; C. |
| 53 Radio scroll jump | NA | No CSS-only radio-tab navigation introduced. |
| 54 Hanging eyebrow | Pass | No new tag-left/heading-right numbered section heads. Existing Workspace eyebrow is stacked above its heading; S6. |
| 55 Uppercase cap collision | NA | No uppercase display heading with sub-1.0 leading introduced; functional heads are roman mixed case. |
| 56 Sticky overlap | Pass | No new competing sticky-at-zero headers; inspected report/drawer heads do not bleed over navigation; C, S1/S4/S5. |
| 57 Studied DNA discarded | NA | This task locks the existing app system; no study-DNA adoption was requested. |

## Handoff and remaining limitations

- **Resolved blocker: mobile Workspace rail clipping/overlap.** The earlier
  fragmented Chat title and report-compression defects remain visibly corrected.
  The previously unacceptable 320/375 Workspace rail now places brand/runtime on
  row one and the existing four nav controls on row two. Accepted S6/S7 confirm
  visible labels without overlap; the parent reports the corresponding mock
  checks passed. No component, action or route was changed for this correction.
  No further production edit, browser run or evidence modification was made by
  this acceptance review.
- **Strict gate acceptance remains limited:** rows marked Limited are not passes.
  The 44px inline-link exception and incomplete all-node contrast/state audit
  prevent an honest unconditional “58/58” or “100% accessible” claim.
- Native-title timing, actual mobile virtual keyboards, enlarged text, 1920px
  continuous-width behavior, assistive technology and every normal-motion state
  are not proved by this reduced-motion, 900px-height capture set. Parent-run
  scripted checks must be reported separately with their actual scope.
- A functional app naturally uses narrow data columns and repeated technical
  tokens. Those app constraints do not justify hiding critical state or treating
  saved inference metrics as live; I keeps live and historical sections separate.
- Global CSS remains additive, existing framework/font conventions remain in
  place, and `tokens.css` is imported before rules. The output contract does not
  authorize deleting production routes or rewriting the global entry sheet.
- This audit does not establish that a new Go-embedded build is serving on 8080,
  repair Phase 3 model-RSS limitations, pass the skipped Phase 4 useful-research
  gate, or demonstrate new research/inference/soak success.

**Durable Step 7 summary:** all 58 checklist entries reviewed with explicit
NA/Limited entries; the known Workspace navigation defect is corrected and
confirmed in accepted rebuilt images. No outstanding critical visual blocker
identified in the inspected evidence; no aggregate pass score or blanket
accessibility/all-clipping certification is claimed.
