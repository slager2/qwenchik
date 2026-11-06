# Accepted source provenance

`BuildEvidence` selects one contiguous excerpt from the exact extracted text saved
as the text artifact. New quote spans use `coordinate_space: extracted_text_runes`:
`start` is inclusive and `end` exclusive, both measured in Unicode runes from the
beginning of that full artifact. Slicing the artifact at those coordinates yields
the quote text exactly. Entity decoding and whitespace normalization belong to
extraction; the provenance selector does not normalize the stored text again.
`SelectExcerpt` remains the string-only wrapper around `SelectExcerptWithSpan`.
An unmatched query falls back to the actual bounded document prefix.
When the search provider (agentSearcher) already returned reranked passages for
the page, `BuildEvidenceWithPassages` first locates the densest region of the
extracted text that shares word 4-grams with those passages and returns that
region (plus a little sentence context) as the span; the excerpt is still an
exact slice of the extracted text. Without a passage match the lexical query
selector is used.

When agentSearcher's `/extract` endpoint reads the page (tried before the
direct fetch), `BuildExtractEvidence` builds the evidence: provider
`agentsearcher`, extraction method `agentsearcher_extract` or
`agentsearcher_extract_pdf`, HTTP status 200. The text artifact is the
service's cleaned page text (`raw_content` without a query, cut by the service
at `MAX_RAW_CHARS`); only when none of its query-ranked chunks occur in that
text are the chunks appended after a `[...]` line (`ExtractEvidenceText`). The
raw artifact is a JSON record of the request parameters and both service
responses, and its SHA-256 is the content hash. The excerpt is one contiguous
verbatim span of at most `ExtractExcerptRunes` (6000) runes, located by the
ranked chunks, then by the search passages, then by the lexical selector; the
quote span uses the same `extracted_text_runes` coordinates. Other evidence
keeps `DefaultExcerptRunes` (4000). Downstream views are prefixes of the stored
excerpt: tool results keep 6000 runes, the citation catalogue 8000 bytes and
working-set facts 2000 runes (with adjusted exact coordinates).

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
