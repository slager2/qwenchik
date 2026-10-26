# Laboratory Phase 4 — bounded section reports

`RESEARCH_SECTION_REPORT_ENABLED=false` is the compatibility default. Opt-in is
snapshotted with the existing research policy. Production injects the existing
artifact store into the existing worker; no additional queue/database/client.

The writer creates a bounded outline (at most four sections), pins each local
writer/critic request, supplies only that section's owned excerpts and criterion
labels, and verifies claim IDs and exact supporting quotes. One persisted
correction is allowed per section, with bounded rejected-draft/reason input.
Supported/fallback sections checkpoint independently and survive normal recovery.
The immutable citation-claim allowance is distributed across sections, not raised.

Backend assembly adds title, deterministic executive summary, research question,
contents, sections, limitations, methodology, source links, coverage, generated
timestamp and run ID. Unrepresented criterion IDs and supplementary gaps are
reported separately; supported claims alone do not declare criterion coverage.
Model-authored links/HTML/headings are rejected. Infrastructure/recovery/resource
errors propagate; they are not treated as unsupported content.

Deliverables: `research_report.json` (type research_report, Markdown, metadata,
sections, coverage and actual byte/section/source counts) and `research_report.md`.
Report persistence remains fenced and does not consume an extra evidence slot.
Content-addressed artifacts and persisted generation time make retries idempotent.
Nested JSON is canonicalized before hashing, including persisted critic reviews.
Repeated model evidence IDs are deduplicated before checkpointing. Discovery and
fixed backend continuation execution criteria are listed separately; external
quotes are not asked to prove that backend operations ran.

Tests exercise supported sections, one correction, excerpt fallback, omitted
coverage, infrastructure failure, reopen after section commit and artifact commit,
no repeated source/model work, and artifact persistence at MaxEvidence.
Full Go suite and vet, frontend 77 unit tests/TypeScript/Chrome smoke/build passed.

Limits: same-model critique is not an independent factual judge. Report size and
coverage depend on real evidence and budgets, not a promised page count. Ordinary
tests use scripted inference and fixture sources; they are not live report-quality
or overnight acceptance. Missing/corrupt provenance is rejected, not reconstructed
from model text. A real deployment still needs its search/PDF capabilities.
