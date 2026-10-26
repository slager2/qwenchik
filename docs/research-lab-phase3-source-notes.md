# Laboratory Phase 3 — compact source notes

Opt-in adaptive/report paths derive bounded extractive notes from active,
task-owned accepted evidence and owned raw/text artifact metadata. Notes persist
in the existing checkpoint and contain source metadata, both hashes, bounded
excerpts/source claims, supported criterion addresses, unresolved criteria,
quality, taint and source state. Raw documents remain in artifacts.

Verifier/outline inputs use bounded notes rather than raw document bodies.
The existing citation pipeline still validates saved excerpt membership and
task ownership. Claims in notes are source observations, not independent truth.

Tests cover a large source whose compact note remains small, foreign/forged
evidence, mismatched raw hashes, and restoration of all four notes after reopen
in the two-round integration scenario. Existing compaction/ownership tests remain.

Limits: note construction is deterministic extraction, not a semantic source
summarizer. Prompt views select at most eight notes; the complete accepted ID
ledger and larger note list remain persisted. Hashes in notes are validated
against artifact metadata; source hashing/file-integrity mechanisms remain owned
by the existing artifact subsystem, not duplicated in the notes builder.
