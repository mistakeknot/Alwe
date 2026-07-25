# Alwe

Universal agent observation layer. Watches any AI coding agent's sessions, exposes structured data as MCP tools and CLI.

cass is an optional accelerator, not a dependency: a local SQLite FTS5 catalog keeps search available when cass is missing, stale, or lock-busy.

The other half of Zakalwe — Zaka steers, Alwe observes.

## Quick Reference

- **Build:** `go build ./cmd/alwe`
- **Test:** `go test ./... -count=1`
- **MCP server:** `./alwe` (default, stdio transport)
- **CLI search:** `./alwe search "query"`
- **CLI index:** `./alwe index` (incremental; only changed transcripts are read)
- **CLI timeline:** `./alwe timeline --since 2h`

## Structure

```
cmd/alwe/              CLI + MCP server entry point
pkg/
  observer/            CASS observer (real-time tail + query) — public API for cross-module import
  localindex/          Local SQLite FTS5 catalog; per-file incremental indexing
  sessionsearch/       Composition layer: merges cass + local, degrades to local-only
internal/
  mcpserver/           MCP server exposing 5 tools over sessionsearch
```

## Design notes

- **Per-file unit of repair.** `localindex` keys transcripts on `(mtime, size)`
  and tracks each file's rowid range, so a drifted transcript is replaced with
  an indexed delete. No global fingerprint, no full rebuild. This is the point
  of the package — see `docs/charter-local-session-index.md`.
- **cass exit codes are a contract.** `cass capabilities --json` marks exits 4
  and 7 retryable; `observer.runCass` retries those with backoff and treats
  everything else as terminal. Do not collapse non-zero exits into one error.
- **cass health != cass usable.** cass calls itself unhealthy when its index is
  merely past a 300s staleness threshold, while still searching fine. Use
  `observer.HealthReport` (reachable vs. self-verdict), not `IsAvailable`, when
  a stale cass should not read as absent. `HealthReport` asks cass to judge
  against 1800s — the threshold cass's own `status` surface uses — because an
  incremental `cass index` measures ~51s and is scheduled every 900s, so the
  300s default marks cass unhealthy for most of every cycle. Tightening the
  schedule to fit 300s would hold the index lock ~51s of every 300s; measure
  before shortening a period. Override with `ALWE_CASS_STALE_THRESHOLD`.
- **Backend scores are not comparable.** cass reports corpus-wide BM25 in the
  tens; FTS5 inverted rank lands near zero. Merge with reciprocal rank fusion,
  never by sorting raw scores together.

## MCP Tools

- `search_sessions` — search agent sessions by content, filter by connector; merges cass + local, degrades to local-only with `degraded`/`notice` set
- `context_for_file` — find sessions that touched a file (same merge/fallback)
- `export_session` — export session to markdown (cass only)
- `timeline` — recent activity across all agents (cass only)
- `health` — per-backend availability, local catalog coverage, and build id

## Git

Alwe has its own git repo at `os/Alwe/`. Commit from here, not the monorepo root.

## Beads

Uses the Demarch monorepo beads tracker at `/home/mk/projects/Demarch/.beads/` (prefix `Demarch-`).
