# Charter: Local session index — make CASS an accelerator, not a dependency

**Status:** open
**Minted:** 2026-07-24
**Project:** `os/Alwe`
**Origin:** CASS outage investigation, 2026-07-24 (see "Diagnosis" below)

## Problem

Alwe's five MCP tools are a thin shell around the `cass` CLI. When cass is
unavailable — for any reason — Alwe returns nothing. On 2026-07-24 that
manifested as `search_sessions` failing with `cass search: exit status 7` and
`health` reporting false, which read as "CASS is down" when cass was in fact
working correctly.

## Diagnosis (what actually failed)

Three findings, only one of which was the reported symptom:

1. **Exit-code contract ignored (fixed, commit `9c02bc1`).** cass declares
   exit 7 as "lock or busy — retryable, remedy: bounded backoff" in
   `cass capabilities --json`. A scheduled `cass index` run (launchd
   `com.arouth.cass-index`, `StartInterval` 900) held the index lock; Alwe
   collapsed every non-zero exit into one error and surfaced it as fatal.

2. **Index rebuild wedge (one occurrence).** 2026-07-22 12:29, phase-2
   `lexical_refresh` deadlocked at 1280/9718 on a memory-budget cycle
   (`inflight_message_bytes` pinned at its 640 MiB cap, producer
   `waiting_turn`, staged-shard-build reserving 16 GiB and allowing 1 job with
   26 pending). cass's stall watchdog aborted it with exit 70. Not recurring —
   the stderr log has been untouched since, and later runs exit 0.

3. **Lexical checkpoint frozen** at `content-v1:9918:9918:492022` while the DB
   sits at 9922 conversations / 492133 messages. Every search since logs
   `stale lexical checkpoint` and takes a
   `deferred-repair-searching-existing-index` path. Persists on 0.6.22.
   Searches still return current content (verified: a `session` query returns a
   hit at `2026-07-25T05:22:51Z`, exactly the newest DB row).

### Root cause of (2) and (3)

Session transcripts are natively **local and append-only** — one JSONL per
session, immutable once written. cass imposes a **single synoptic abstraction**
over them: one global fingerprint for the entire corpus. So a 4-conversation
drift has no local repair path; the only unit of repair is "rebuild all 9922,"
which is the operation that deadlocks.

The defect is not the deadlock. It is that a 4-row drift and a total corpus
loss have the *same* remedy.

## Doctrine grounding

`sylveste/agents/design-doctrine.md` rules this out as a rewrite:

> **Premature abstraction** — cementing wrong patterns is worse than messy
> scripts. **Strangler-fig, never rewrite.**

> **Check prior art first** — if a tool has an "adopt" verdict, default to
> integration over reimplementation.

cass's search, ranking, ingest, and 492k-message corpus all work. Exactly one
component earns the critique: the unit of repair. So this charter is a
strangler-fig around that seam, not a replacement search engine.

It is also Layer 1 / Layer 2 from *Cybernetic Unix*: Alwe becomes genuinely
useful standalone (local index), and lights up further when cass is present
(better ranking, semantic search) — without importing cass's concerns.

## Design

The seam already exists. `pkg/observer/cass.go`'s `TailSession` reads JSONL
directly, documented as "real-time observation while CASS indexes async" — a
cass-independent read path already in the codebase.

Extend it:

- **Local catalog.** SQLite FTS5 over the JSONL transcripts, one row per
  message, keyed per source file on `(path, mtime, size)`. Incremental: a file
  whose mtime is unchanged is skipped; a changed file is re-indexed alone.
  **No global fingerprint, no global lock, no rebuild concept.**
- **Merge + fallback in `search_sessions`.** Query cass and the local catalog;
  merge and dedupe by `(source_path, line_number)`. When cass errors or is
  locked past its retry budget, serve local-only results and say so in the
  response rather than failing.
- **Close the loop.** Per the four-stage calibration pattern: record per-file
  index actuals (message count, duration) so the catalog reports its own
  coverage vs. cass's, making drift observable instead of silent.

## Subgoals

1. `pkg/localindex` — SQLite FTS5 catalog with per-file incremental indexing
   and a `Coverage()` reporter. Unit tests over fixture JSONL.
2. Wire `search_sessions` and `context_for_file` to merge cass + local results,
   dedupe, and degrade to local-only on cass failure.
3. `alwe index` CLI subcommand for explicit/scheduled local catalog refresh;
   verify a drifted single file re-indexes without touching the others.
4. File the upstream cass issue: on 0.6.22, `cass status` reports
   `checkpoint.db_matches: true` while `cass search` reports the storage
   fingerprint no longer matches. That contradiction is why the repair defers
   forever. Not covered by the closed #244 / #258.
   **Filed 2026-07-24:**
   https://github.com/Dicklesworthstone/coding_agent_session_search/issues/353

## Tradeoffs (accepted)

- **Weaker local ranking.** BM25 needs global IDF; a per-file catalog
  approximates it. Local-only results will rank worse than tantivy's. Accepted
  because the goal is availability, not better relevance.
- **No semantic search, `export-html`, or `pages` on the local path.** Those
  stay cass-only; the local path is lexical.
- **A second index to maintain.** Justified only by removing the single point
  of failure — not by search quality.

## Out of scope

- Replacing cass. Its corpus, ingest, semantic search, and ranking stay.
- Forcing the frozen checkpoint to repair (a ~89-minute rebuild that already
  deadlocked once). Deferred pending the upstream report.

## Rollback

Each subgoal is independently revertable. The merge layer is behind a
"prefer cass, fall back to local" branch — deleting `pkg/localindex` and that
branch restores exact current behavior. No cass state is written or migrated.
