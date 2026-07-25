---
artifact_type: reflection
goal: be6423c3
stage: reflect
date: 2026-07-24
project: os/Alwe
---

# Reflection: local session index (goal be6423c3)

Ran as a `/goal`, not a `/sprint`, so there is no sprint id to register against;
this artifact is keyed to the goal.

## What shipped

Four commits on `os/Alwe` (`9c02bc1`, `e99a7f6`, `bd1ab59`, `249a280`):

- `observer.runCass` honours cass's retryable exit codes (4, 7) with bounded
  backoff instead of collapsing every non-zero exit into one error.
- `pkg/localindex`: SQLite FTS5 catalog whose unit of work is one transcript
  file, keyed on `(mtime, size)`, with each file's rows in a contiguous rowid
  range so replacement is an indexed delete.
- `pkg/sessionsearch`: composes cass and the local catalog; neither is required.
  Merges by reciprocal rank fusion, degrades to local-only with `degraded` and
  `notice` set.
- `alwe index` CLI, build-id reporting in `health`, upstream issue
  [#353](https://github.com/Dicklesworthstone/coding_agent_session_search/issues/353).
- cass upgraded 0.6.19 → 0.6.22.

## Learning 1: three defects surfaced only against real data

All three passed a green suite built on synthetic fixtures. Each was two systems
disagreeing about an abstraction they appeared to share.

| Defect | Why fixtures missed it |
|---|---|
| Incomparable score scales — cass BM25 ≈ 35, inverted FTS5 rank ≈ 1e-6. Sorting them together buried every local hit whenever cass was up. | Fixtures used hand-chosen scores in the same range, so ordering looked sane. |
| Incompatible coordinate spaces — cass's `line_number` is not a file line (it reported 1 and 12 for a transcript whose first match is line 14), so the dedupe key could never collide. | The fixture *constructed* cass hits using the local hit's own line number, guaranteeing a collision that cannot happen in production. |
| "Healthy" meaning two things — cass calls itself unhealthy past a 300s staleness threshold while searching fine. | No fixture modelled a *stale but working* backend; only up/down. |

The dedupe test was the worst of these: it passed, and asserted the right
property, while testing a scenario the real system cannot produce. A green
assertion over an impossible input is worse than no test, because it retires
suspicion.

**Carry forward:** when a test constructs the coupling it means to verify
(here: reusing one backend's identifier to build the other's), that is a smell.
Derive the fixture from each side independently, or assert against a real
sample.

## Learning 2: measurement changed the design twice

Both times the intuitive choice was wrong and cheap measurement caught it.

- **Per-file repair validated.** Cold index of the real corpus: 9,582
  transcripts / 348,663 messages in 2m7s. Warm no-op: 1.5s. One touched
  transcript: 7–12ms with a one-entry actuals log. Against cass's ~89-minute
  all-or-nothing rebuild that never completed, this is the whole thesis of the
  charter, measured rather than argued.
- **Cadence tightening ruled out.** The obvious fix for the health mismatch was
  lowering the launchd interval below cass's 300s threshold. Measuring an
  incremental `cass index` at **51.5s** killed that: a 300s interval means a 17%
  duty cycle holding the index lock ~51s of every 300s, trading a cosmetic
  signal for real contention. Aligning Alwe's probe threshold to 1800s — the
  value cass's own `status` surface already uses — costs nothing.

**Carry forward:** measure the cost of the periodic job before shortening its
period. "Run it more often" reads as free and is not.

## Learning 3: the doctrine filter did real work

The session began as "fix CASS" and mk asked whether to build a better CASS from
scratch. `agents/design-doctrine.md`'s anti-pattern — *premature abstraction:
strangler-fig, never rewrite* — redirected that into a boundary argument: which
single component earns replacement. The answer was narrow (the unit of repair),
and the resulting charter is ~250 lines of new code rather than a search engine.

Notably the diagnosis supported the doctrine rather than the reverse: cass's
search, ranking, ingest, and 500k-message corpus were all fine. Only the global
fingerprint was wrong, and it was wrong in a specific way — a four-conversation
drift and a total corpus loss had the same remedy.

**Carry forward:** when a rewrite is proposed, make the deliverable a boundary
argument first. "Which component earns this, and what stays" is reviewable in a
way that "build a better X" is not.

## What I would do differently

- Index a real transcript directory in the *first* test pass, not after the
  synthetic suite was green. Two of the three defects would have surfaced
  immediately.
- Check the two backends' identifier semantics before designing a dedupe key
  around them. I assumed `line_number` meant the same thing on both sides
  because the field names matched.

## Residual

`timeline` and `export_session` are still cass-only, so "cass optional" holds
for search but not for all five MCP tools. Called out in the goal report rather
than left implicit; the local catalog can serve both (timeline from `ts`
aggregates, export by reading the transcript directly) if it becomes worth doing.
