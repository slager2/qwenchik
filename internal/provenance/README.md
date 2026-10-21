# Accepted source provenance

`BuildEvidence` selects one contiguous excerpt from the exact extracted text saved
as the text artifact. New quote spans use `coordinate_space: extracted_text_runes`:
`start` is inclusive and `end` exclusive, both measured in Unicode runes from the
beginning of that full artifact. Slicing the artifact at those coordinates yields
the quote text exactly. Entity decoding and whitespace normalization belong to
extraction; the provenance selector does not normalize the stored text again.
`SelectExcerpt` remains the string-only wrapper around `SelectExcerptWithSpan`.
An unmatched query falls back to the actual bounded document prefix.

Legacy quote spans without `coordinate_space` retain their original coordinates.
They are not backfilled or relabeled as full-text positions. Citation validation
continues to use exact excerpt membership.

After successful extraction and validation, the fetch tool writes complete raw
and text files through the immutable artifact store and builds evidence from their
computed IDs. The raw artifact's SHA-256 is the evidence content hash. Production
SQLite then uses `SaveResearchSource` to commit artifact metadata, evidence,
creation events and the evidence counter in one fenced transaction. Duplicate
source identity returns the persisted evidence record; conflicting ownership,
artifact references or hashes fail. Evidence capacity is checked before inserting
new metadata. Existing repository fixtures can use the legacy individual saves.
There are no schema changes or new tables, and existing public `SaveArtifact` and
`SaveEvidence` contracts are unchanged.

## Filesystem and database failure boundary

The filesystem and SQLite are **not** one atomic transaction. A failure writing the
text file can leave a complete unreferenced raw file. A database failure or crash
after file creation can leave both complete raw and text files without accepted
metadata, even though the metadata/events/counter transaction rolls back fully.
Temporary file cleanup is handled by the artifact store; complete immutable files
are retained. A retry can reuse identical task/MIME/content-derived files.

This slice does not reclaim those files. Cleanup must account for concurrent
writers and all durable references before deleting a positively identified orphan;
an unknown or currently unreferenced filename alone is not deletion authority.
Do not remove unknown files, shared immutable content, checkpoints or prior source
metadata to force recovery. Rolling back the code requires no SQL rollback; old
quote JSON remains readable because the coordinate-space field is optional.
