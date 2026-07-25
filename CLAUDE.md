# Alwe

Universal agent observation layer. Watches any AI coding agent's sessions, exposes structured data as MCP tools and CLI.

cass is an optional accelerator, not a dependency: a local SQLite FTS5 catalog keeps **all five MCP tools** available when cass is missing, stale, or lock-busy.

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
  merely past a 300s staleness threshold, while still searching fine. Decide
  from `observer.HealthReport`'s `Reachable` vs. `Healthy`; there is
  deliberately no `IsAvailable`-style bool, because one bit cannot express that
  state without lying (the old one also probed at cass's strict 300s default and
  so returned false for most of every cycle). `HealthReport` asks cass to judge
  against 1800s — the threshold cass's own `status` surface uses — because an
  incremental `cass index` measures ~51s and is scheduled every 900s, so the
  300s default marks cass unhealthy for most of every cycle. Tightening the
  schedule to fit 300s would hold the index lock ~51s of every 300s; measure
  before shortening a period. Override with `ALWE_CASS_STALE_THRESHOLD`.
- **Backend scores are not comparable.** cass reports corpus-wide BM25 in the
  tens; FTS5 inverted rank lands near zero. Merge with reciprocal rank fusion,
  never by sorting raw scores together.
- **`Degraded` means capability lost, not backend missing.** Every tool is
  servable from either backend alone, so losing one sets `ReducedRanking`, not
  `Degraded`. Only losing both is a degradation.
- **Export takes no catalog.** `localindex.ExportMarkdown` is a package-level
  function on purpose: requiring an open index would mean an unindexed session
  could not be exported, which is the freshness gap the local path closes.
- **Timeline windows on transcript mtime**, which is numeric, indexed, and the
  honest signal — a transcript is only written while its session runs. Message
  timestamps then come from the catalog by rowid range.
- **The catalog needs a scheduler to be trustworthy.** Nothing in the binary
  keeps it fresh; `ops/com.arouth.alwe-index.plist` runs `alwe index` every 300s.
  The interval is measured (~80ms no-op, ~83ms per changed file over 9,647
  transcripts = 0.4% duty cycle), not assumed — re-measure before changing it.
  `health` reports `local_stale` past 600s (2x the interval) so a stopped
  indexer is visible; one missed run is not an alarm.
- **Real-data tests are load-bearing.** `pkg/sessionsearch/realdata_test.go`
  asserts coordinate agreement against actual transcripts. Three defects once
  survived a green synthetic suite; do not delete these because they skip on a
  fresh machine.

## MCP Tools

- `search_sessions` — search agent sessions by content, filter by connector; merges cass + local, degrades to local-only with `degraded`/`notice` set
- `context_for_file` — find sessions that touched a file (same merge/fallback)
- `export_session` — export session to markdown; falls back to rendering the transcript directly, so an unindexed session still exports
- `timeline` — recent activity across all agents; falls back to catalog aggregates with `source:"local"` and a notice
- `health` — per-backend availability, local catalog coverage, and build id

## Git

Alwe has its own git repo at `os/Alwe/`. Commit from here, not the monorepo root.

## Beads

Uses the Demarch monorepo beads tracker at `/home/mk/projects/Demarch/.beads/` (prefix `Demarch-`).
