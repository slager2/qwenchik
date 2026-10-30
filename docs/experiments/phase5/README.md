# Phase 5 frontend evidence

The selected PNGs show real saved conversations through the real application
API. The capture runner blocks Doctor, all mutating requests and external origins
before navigation. Mock-browser test evidence is separate.

## Provenance

- The original pre-edit run produced 35 screenshots at the old embedded UI on
  8080. Those images were opened during the work, but the temporary
  `/tmp/opencode/phase5-before-verified` directory was no longer present at final
  archive verification. Its loss is recorded; it is not represented as recovered.
- `reference-before-manifest.json` is a **new reconstructed visual reference**:
  immutable frontend assets from commit `7e0901c`, served on 4178 with the current
  real API. It is not a database/time snapshot and is not the original capture.
- `after-manifest.json` is the final new production frontend on 4177, proxying
  the real 8080 API. The served asset identities are recorded in each manifest.
- The earlier `/tmp/opencode/phase5-after` and `phase5-after-final` runs preceded
  the final Workspace mobile-nav correction and are superseded for final visuals.

## Retained views

`reference-before-*.png` and `after-*.png` retain selected 375px/1440px paused and
partial-report views; explicit Details/Sources views are retained for the new UI.
The full seven-width matrices were captured locally. Capture timestamps, selected
conversation/run IDs, counts, actual GET response summaries and observational
layout measurements are preserved in the manifests.

Screenshot page-error/Doctor/mutation counts do not certify accessibility,
research usefulness, source truth or successful inference. Read the frontend plan,
completion report and Hallmark audit for their separate verification boundaries.
