---
artifact_type: reflection
goal: 31686951
stage: reflect
date: 2026-07-25
project: os/Alwe
---

# Reflection: scheduled catalog refresh + staleness signal (goal 31686951)

Third successor in the line from `be6423c3`. The previous two goals built a local
catalog and made every tool fall back to it — while nothing kept the catalog
current. It was fresh only because I had been running `alwe index` by hand during
the session, which made the fallback incidentally rather than dependably
trustworthy.

## What shipped

`6dfc412` and `0b8b82a` on `os/Alwe`:

- `ops/com.arouth.alwe-index.plist` — launchd agent, `alwe index --quiet` every
  300s at `Nice 5` / `LowPriorityIO`, with the interval's justification inline.
- `health` gains `local_stale`, `local_age_seconds`, and
  `local_stale_threshold_seconds`; `Coverage` gains `LastIndexedUnix`.
- Five tests covering the staleness signal, including never-indexed and the
  both-signals-firing case.

## Learning 1: a scheduler without a staleness signal is half a fix

The obvious read of this goal was "add a cron job". That alone would have been
the weaker half, and would have felt complete.

A scheduled job that silently stops looks exactly like one that is working:
searches keep succeeding, just against progressively older data. That is the same
failure shape as the cass bug that started this whole line of work — a repair
deferred forever with nothing reporting that it was never happening. Building the
scheduler without the signal would have reproduced the original defect one layer
up.

**Carry forward:** whenever you add something that runs unattended, add the
signal that says it stopped. The question to ask is not "does this work?" but "if
this quietly died, what would tell me?"

## Learning 2: threshold choice is a real design decision

The staleness threshold is 600s against a 300s interval — deliberately 2×, not 1×.

At 1× every ordinary scheduling jitter fires the alarm, and an alarm that fires
routinely is one you learn to ignore. That reproduces silence by a different
route: the signal is present, technically correct, and useless. 2× tolerates one
missed run while still catching a dead indexer within about ten minutes.

The same reasoning appears one level down: cass's own health uses a 300s
staleness threshold against indexing cadences far longer than that, which is
precisely why `cass health` exits 1 most of the time and why we stopped trusting
it. Having criticised that in `a5e8bd5c`, it would have been careless to make the
mirror-image mistake here.

**Carry forward:** an alarm threshold has two failure modes, not one. Too loose
misses the fault; too tight trains the operator to ignore it.

## Learning 3: measurement made the interval defensible rather than plausible

Five warm runs: ~80ms for a full 9,647-file no-op scan, ~83ms per changed file,
6.71s to clear an 80-file backlog. At the observed ~2.7 changed transcripts per
minute a 300s run costs ~1.2s — a 0.4% duty cycle, against cass's 5.7%.

Without those numbers 300s would have been a guess that happened to be fine. With
them, the plist carries the reasoning inline so the next person to change the
interval sees the measurement instead of re-deriving it. This is the second time
in this line of work that measuring changed a decision rather than confirming it
(the first ruled out tightening cass's cadence).

## Learning 4: a meaningful zero must not be omitempty

`local_age_seconds` was `omitempty`, so a catalog refreshed seconds ago rendered
as `null` — the *healthiest* possible state, indistinguishable from "not
computed". Caught only by looking at live output after the tests were green.

This is the same defect class as everything else this line of work has chased:
two distinct states collapsing into one representation. It has now appeared as
incomparable score scales, incompatible coordinate spaces, "healthy" meaning two
things, and a zero value that could not be expressed.

**Carry forward:** for any numeric field, ask what its zero means. If zero is a
real answer, `omitempty` is a bug.

## What I would do differently

- Look at live JSON output as a routine step after adding fields, not as an
  afterthought. The `omitempty` bug was invisible to tests because the tests
  asserted on struct fields, not on the serialised form. The fix included a test
  that marshals and inspects the JSON.
- I verified the agent had run by reading its log, which was right, but I should
  have checked `launchctl list`'s exit-status column *before* kickstarting it
  rather than after — a bad plist would have shown up as a non-zero status.

## Residual

Two indexers now run on this machine (`com.arouth.cass-index` at 900s,
`com.arouth.alwe-index` at 300s). They do not contend — SQLite WAL keeps catalog
readers unblocked during writes — but nothing coordinates them either, and the
cass one still wedges occasionally per upstream #353.
