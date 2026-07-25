package sessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistakeknot/Alwe/pkg/localindex"
	"github.com/mistakeknot/Alwe/pkg/observer"
)

// fakeCass writes a stand-in cass binary. exitCode != 0 makes every
// invocation fail with that code; otherwise it prints stdout.
func fakeCass(t *testing.T, exitCode int, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "cass")
	body := fmt.Sprintf(`#!/bin/sh
if [ %d -ne 0 ]; then
  echo "index-run.lock held by pid 999" >&2
  exit %d
fi
printf '%%s' %q
`, exitCode, exitCode, stdout)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// cassObserverAt builds an observer pointed at a stand-in binary by putting it
// on PATH under the name cass.
func cassObserverAt(t *testing.T, script string) *observer.CassObserver {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(script))
	obs, err := observer.New()
	if err != nil {
		t.Fatalf("observer.New: %v", err)
	}
	return obs
}

// localWith builds a local catalog containing one transcript with the given
// user prompts.
func localWith(t *testing.T, sessionName string, prompts ...string) *localindex.Index {
	t.Helper()
	root := t.TempDir()
	var sb strings.Builder
	for _, p := range prompts {
		line, _ := json.Marshal(map[string]any{
			"type":      "user",
			"timestamp": "2026-07-25T05:22:51Z",
			"message":   map[string]any{"role": "user", "content": p},
		})
		sb.Write(line)
		sb.WriteByte('\n')
	}
	path := filepath.Join(root, sessionName+".jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := localindex.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return ix
}

// fakeCassVerdict writes a stand-in cass that prints stdout AND exits with the
// given code — the shape `cass health` takes when it reports itself unhealthy.
func fakeCassVerdict(t *testing.T, exitCode int, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "cass")
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s' %q
exit %d
`, stdout, exitCode)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// localWithMtime builds a catalog over one transcript with a controlled mtime,
// and returns the catalog plus the transcript path.
func localWithMtime(t *testing.T, name string, mtime time.Time, prompts ...string) (*localindex.Index, string) {
	t.Helper()
	root := t.TempDir()
	var sb strings.Builder
	for _, p := range prompts {
		line, _ := json.Marshal(map[string]any{
			"type":      "user",
			"timestamp": mtime.UTC().Format(time.RFC3339),
			"message":   map[string]any{"role": "user", "content": p},
		})
		sb.Write(line)
		sb.WriteByte('\n')
	}
	path := filepath.Join(root, name+".jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	ix, err := localindex.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}
	return ix, path
}

// cassPayload renders a `cass search --json` envelope.
func cassPayload(hits ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"hits": hits})
	return string(b)
}

// The headline guarantee: cass wedged on its lock, search still answers.
func TestSearch_ServesLocalOnlyWhenCassLockBusy(t *testing.T) {
	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 7, ""))),
		WithLocal(localWith(t, "sess-local", "which session was working on cujgel most recently?")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := svc.Search(context.Background(), "cujgel", "", 5)
	if err != nil {
		t.Fatalf("search should degrade, not fail: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected local hits while cass is lock-busy")
	}
	if !res.Degraded {
		t.Error("result should be marked degraded")
	}
	if got := res.Sources; len(got) != 1 || got[0] != "local" {
		t.Errorf("sources = %v, want [local]", got)
	}
	if !strings.Contains(res.Notice, "local-only") {
		t.Errorf("notice %q should state results are local-only", res.Notice)
	}
	if res.Hits[0].Source != "local" {
		t.Errorf("hit source = %q, want local", res.Hits[0].Source)
	}
}

func TestSearch_ServesLocalOnlyWhenCassAbsent(t *testing.T) {
	svc, err := New(WithLocal(localWith(t, "sess-nocass", "cujgel spec validation")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.HasCass() {
		t.Skip("a real cass is installed on this machine; covered by the lock-busy case")
	}

	res, err := svc.Search(context.Background(), "cujgel", "", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected local hits with no cass at all")
	}
	if !res.Degraded || !strings.Contains(res.Notice, "local-only") {
		t.Errorf("degraded=%v notice=%q", res.Degraded, res.Notice)
	}
}

// Both backends up: overlapping hits collapse on (file_path, line_number).
func TestSearch_MergesAndDedupes(t *testing.T) {
	local := localWith(t, "shared", "cujgel appears here")
	// Discover the local hit's coordinates so cass can report the same line.
	locHits, err := local.Search(context.Background(), "cujgel", 5)
	if err != nil || len(locHits) == 0 {
		t.Fatalf("local search setup: %v (%d hits)", err, len(locHits))
	}
	dup := locHits[0]

	payload := cassPayload(
		map[string]any{ // same transcript line as the local hit
			"snippet": "cujgel appears here", "score": 9.0,
			"source_path": dup.FilePath, "agent": "claude_code",
			"created_at": 1784957006272, "line_number": dup.LineNumber,
		},
		map[string]any{ // cass-only hit
			"snippet": "cujgel elsewhere", "score": 8.0,
			"source_path": "/elsewhere/other.jsonl", "agent": "claude_code",
			"created_at": 1784957006272, "line_number": 42,
		},
	)

	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 0, payload))),
		WithLocal(local),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := svc.Search(context.Background(), "cujgel", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Degraded {
		t.Errorf("both backends answered, should not be degraded: %s", res.Notice)
	}
	if len(res.Sources) != 2 {
		t.Errorf("sources = %v, want both", res.Sources)
	}

	// No duplicate (file_path, line_number) pairs.
	seen := map[string]bool{}
	for _, h := range res.Hits {
		k := fmt.Sprintf("%s:%d", h.FilePath, h.LineNumber)
		if seen[k] {
			t.Errorf("duplicate hit for %s", k)
		}
		seen[k] = true
	}
	if len(res.Hits) != 2 {
		t.Fatalf("got %d hits, want 2 (one merged, one cass-only)", len(res.Hits))
	}

	// The overlapping hit must be attributed to both backends.
	var merged *Hit
	for i := range res.Hits {
		if res.Hits[i].FilePath == dup.FilePath && res.Hits[i].LineNumber == dup.LineNumber {
			merged = &res.Hits[i]
		}
	}
	if merged == nil {
		t.Fatal("merged hit missing")
	}
	if merged.Source != "cass+local" {
		t.Errorf("merged hit source = %q, want cass+local", merged.Source)
	}
	// Corroboration by both backends must outrank a single-backend hit, even
	// though the cass-only hit had a higher raw cass score than nothing.
	if res.Hits[0].Source != "cass+local" {
		t.Errorf("first hit source = %q, want the corroborated hit to rank first", res.Hits[0].Source)
	}
	// Fused scores are reciprocal-rank sums, not raw backend scores.
	if merged.Score >= 9.0 {
		t.Errorf("merged score %v looks like a raw cass score; expected an RRF score", merged.Score)
	}
}

// A hit only the local catalog knows about must still be able to surface when
// cass is up. Before rank fusion, cass's tens-scale BM25 buried every local hit
// and the merge was decorative.
func TestSearch_LocalOnlyHitSurfacesAlongsideCass(t *testing.T) {
	local := localWith(t, "fresh", "a freshly indexed pomegranate prompt")
	payload := cassPayload(map[string]any{
		"snippet": "unrelated cass material", "score": 35.0,
		"source_path": "/old/other.jsonl", "agent": "claude_code",
		"created_at": 1784957006272, "line_number": 7,
	})

	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 0, payload))),
		WithLocal(local),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := svc.Search(context.Background(), "pomegranate", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var sawLocal bool
	for _, h := range res.Hits {
		if strings.Contains(h.Source, "local") {
			sawLocal = true
		}
	}
	if !sawLocal {
		t.Errorf("a local-only hit was buried by cass's score scale: %+v", res.Hits)
	}
}

// Rank fusion must preserve each backend's own ordering. cass already returns
// hits best-first, so its first hit must stay ahead of its second regardless of
// the raw score values attached to them.
func TestSearch_PreservesBackendRankOrder(t *testing.T) {
	payload := cassPayload(
		map[string]any{"snippet": "cass rank 1", "score": 1.0, "source_path": "/a.jsonl", "agent": "claude_code", "line_number": 1},
		map[string]any{"snippet": "cass rank 2", "score": 50.0, "source_path": "/b.jsonl", "agent": "claude_code", "line_number": 2},
	)
	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 0, payload))),
		WithLocal(localWith(t, "unrelated", "nothing relevant")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := svc.Search(context.Background(), "anything", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) < 2 {
		t.Fatalf("got %d hits, want at least 2", len(res.Hits))
	}
	if res.Hits[0].FilePath != "/a.jsonl" {
		t.Errorf("first hit = %s, want /a.jsonl (cass's rank 1) — rank order not preserved",
			res.Hits[0].FilePath)
	}
	// Output is ordered by the fused score.
	for i := 1; i < len(res.Hits); i++ {
		if res.Hits[i-1].Score < res.Hits[i].Score {
			t.Errorf("hits not ordered by fused score descending: %+v", res.Hits)
			break
		}
	}
}

func TestSearch_ConnectorFilterAppliesToLocalHits(t *testing.T) {
	// Local transcripts in a temp dir are provider "unknown", so filtering on
	// claude_code must exclude them rather than leaking through.
	svc, err := New(WithLocal(localWith(t, "sess-filter", "cujgel content")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.HasCass() {
		svc.cass = nil // isolate the local path
	}

	res, err := svc.Search(context.Background(), "cujgel", "claude_code", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, h := range res.Hits {
		if h.Provider != "claude_code" {
			t.Errorf("connector filter leaked a %q hit", h.Provider)
		}
	}
}

func TestSearch_ErrorsOnlyWhenEveryBackendFails(t *testing.T) {
	// cass broken, and a local catalog that has nothing indexed still counts
	// as answering — so construct the all-failed case by giving cass a bad
	// exit and detaching local entirely.
	svc, err := New(WithCass(cassObserverAt(t, fakeCass(t, 7, ""))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.local = nil

	if _, err := svc.Search(context.Background(), "cujgel", "", 5); err == nil {
		t.Error("expected an error when no backend can answer")
	}
}

func TestHealth_LocalOnlyIsStillHealthy(t *testing.T) {
	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 7, ""))),
		WithLocal(localWith(t, "sess-health", "content")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := svc.Health(context.Background())
	if !h.Healthy {
		t.Error("a working local catalog means healthy, even with cass down")
	}
	if h.Cass {
		t.Error("cass should report unavailable")
	}
	// Capability is intact (every tool is servable from the catalog), so this
	// is reduced ranking rather than degradation.
	if h.Degraded {
		t.Errorf("no capability lost, so degraded must be false: %+v", h)
	}
	if !h.ReducedRanking || h.Notice == "" {
		t.Errorf("expected a reduced-ranking notice, got %+v", h)
	}
	if h.Coverage == nil || h.Coverage.Files != 1 {
		t.Errorf("expected local coverage to be reported, got %+v", h.Coverage)
	}
}

// Condition 5: a stale MCP server must be detectable from health output.
func TestHealth_ReportsBuildID(t *testing.T) {
	svc, err := New(WithLocal(localWith(t, "sess-build", "content")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := svc.Health(context.Background())
	if h.BuildModule != "github.com/mistakeknot/Alwe" {
		t.Errorf("build_module = %q, want the Alwe module path", h.BuildModule)
	}
	// Under `go test` the main module is the test binary, so a revision may be
	// absent; the field must exist and be populated in a real build. Assert the
	// helper does not panic and returns the module at minimum.
	t.Logf("build_id=%q module=%q", h.BuildID, h.BuildModule)
}

func TestTimeline_FallsBackToLocalCatalog(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	local, transcript := localWithMtime(t, "sess-tl", now.Add(-2*time.Hour), "recent local activity")

	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 7, ""))), // cass lock-busy
		WithLocal(local),
		WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := svc.Timeline(context.Background(), "24h")
	if err != nil {
		t.Fatalf("timeline should fall back, not fail: %v", err)
	}

	var res localindex.TimelineResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("timeline output is not the local JSON shape: %v\n%s", err, out)
	}
	if res.Source != "local" {
		t.Errorf("source = %q, want local so callers can tell the paths apart", res.Source)
	}
	if res.Totals.Sessions != 1 {
		t.Fatalf("got %d sessions, want the one recent transcript", res.Totals.Sessions)
	}
	if res.Sessions[0].FilePath != transcript {
		t.Errorf("file_path = %q, want %q", res.Sessions[0].FilePath, transcript)
	}
}

func TestTimeline_PrefersCassWhenItAnswers(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	local, _ := localWithMtime(t, "sess-pref", now.Add(-1*time.Hour), "local activity")

	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 0, `{"source":"cass","events":[]}`))),
		WithLocal(local),
		WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := svc.Timeline(context.Background(), "24h")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if !strings.Contains(out, `"source":"cass"`) {
		t.Errorf("expected cass output when cass answers, got: %s", out)
	}
}

func TestExportSession_FallsBackToDirectTranscriptRender(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	local, transcript := localWithMtime(t, "sess-exp", now, "a distinctive tangerine prompt")

	svc, err := New(
		WithCass(cassObserverAt(t, fakeCass(t, 7, ""))), // cass lock-busy
		WithLocal(local),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	md, err := svc.ExportSession(context.Background(), transcript)
	if err != nil {
		t.Fatalf("export should fall back, not fail: %v", err)
	}
	if !strings.Contains(md, "a distinctive tangerine prompt") {
		t.Errorf("export missing the session's user prompt:\n%s", md)
	}
	if !strings.Contains(md, "no cass") {
		t.Errorf("export should state it used the local path:\n%s", md)
	}
}

// Export must not need the catalog: an unindexed session still exports, which
// is the freshness gap the local path exists to close.
func TestExportSession_WorksForUnindexedTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never-indexed.jsonl")
	line, _ := json.Marshal(map[string]any{
		"type":      "user",
		"timestamp": "2026-07-25T10:00:00Z",
		"message":   map[string]any{"role": "user", "content": "brand new nectarine session"},
	})
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := New(WithCass(cassObserverAt(t, fakeCass(t, 7, ""))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.local = nil // no catalog at all

	md, err := svc.ExportSession(context.Background(), path)
	if err != nil {
		t.Fatalf("export with no catalog and no cass: %v", err)
	}
	if !strings.Contains(md, "brand new nectarine session") {
		t.Errorf("export missing content:\n%s", md)
	}
}

// Condition 4: with cass absent every tool is still servable, so health must
// not call that a degradation. Ranking quality is reported separately.
func TestHealth_CassAbsentIsNotDegraded(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	local, _ := localWithMtime(t, "sess-h", now, "content")

	svc, err := New(WithLocal(local))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.cass = nil

	h := svc.Health(context.Background())
	if !h.Healthy {
		t.Error("a working local catalog means healthy")
	}
	if h.Degraded {
		t.Error("no capability is lost with cass absent, so degraded must be false")
	}
	if !h.ReducedRanking {
		t.Error("ranking quality is reduced and should be reported as such")
	}
	if !strings.Contains(h.Notice, "local-only ranking") {
		t.Errorf("notice %q should explain the ranking reduction", h.Notice)
	}
}

func TestHealth_DegradedOnlyWhenNoBackend(t *testing.T) {
	svc, err := New(WithCass(cassObserverAt(t, fakeCass(t, 7, ""))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.local = nil

	h := svc.Health(context.Background())
	if h.Healthy || !h.Degraded {
		t.Errorf("no reachable backend must be unhealthy and degraded, got %+v", h)
	}
}

// A stale-but-working cass must read as usable, not absent. cass exits 1 with
// `"errors":["index stale"]` whenever its index is past a 300-second threshold,
// which on a 900-second indexing cadence is most of the time — yet searches
// keep working. Treating that as "cass down" would discard the better backend.
func TestHealth_StaleCassIsReachableNotAbsent(t *testing.T) {
	verdict := `{"healthy":false,"errors":["index stale"],"recommended_action":"run cass index"}`
	stub := fakeCassVerdict(t, 1, verdict) // exit 1, but still prints its verdict

	svc, err := New(
		WithCass(cassObserverAt(t, stub)),
		WithLocal(localWith(t, "sess-stale", "content")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := svc.Health(context.Background())
	if !h.Cass {
		t.Error("a stale cass that still answers must report reachable")
	}
	if h.CassSelfReport == nil {
		t.Fatal("expected cass's self-report to be surfaced")
	}
	if h.CassSelfReport.Healthy {
		t.Error("cass's own verdict was unhealthy and should be preserved")
	}
	if len(h.CassSelfReport.Errors) == 0 || h.CassSelfReport.Errors[0] != "index stale" {
		t.Errorf("errors = %v, want cass's stated problem", h.CassSelfReport.Errors)
	}
	if !strings.Contains(h.Notice, "index stale") {
		t.Errorf("notice %q should relay cass's complaint", h.Notice)
	}
	if !h.Healthy {
		t.Error("both backends usable means healthy")
	}
}

// Condition 5's point: a stopped or wedged indexer must be detectable. Without
// this signal, searches keep succeeding against progressively older data and
// nothing says so.
func TestHealth_DetectsStaleLocalCatalog(t *testing.T) {
	local, _ := localWithMtime(t, "sess-stale-cat", time.Now(), "content")

	// Fresh: the catalog was just indexed.
	fresh, err := New(WithLocal(local), WithClock(time.Now))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fresh.cass = nil
	h := fresh.Health(context.Background())
	if h.LocalStale {
		t.Errorf("a just-indexed catalog must not be stale: age=%ds threshold=%ds",
			h.LocalAgeSeconds, h.LocalStaleThresholdSeconds)
	}
	if h.LocalStaleThresholdSeconds != defaultLocalStaleThresholdSecs {
		t.Errorf("threshold = %d, want %d", h.LocalStaleThresholdSeconds, defaultLocalStaleThresholdSecs)
	}

	// Advance the clock past the window: the indexer has evidently stopped.
	future := time.Now().Add(time.Duration(defaultLocalStaleThresholdSecs+100) * time.Second)
	stale, err := New(WithLocal(local), WithClock(func() time.Time { return future }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stale.cass = nil
	h = stale.Health(context.Background())
	if !h.LocalStale {
		t.Fatalf("catalog %ds old should be stale past a %ds window",
			h.LocalAgeSeconds, h.LocalStaleThresholdSeconds)
	}
	if h.LocalAgeSeconds < int64(defaultLocalStaleThresholdSecs) {
		t.Errorf("age = %ds, expected more than the threshold", h.LocalAgeSeconds)
	}
	if !strings.Contains(h.Notice, "alwe index") {
		t.Errorf("notice %q should name the remedy", h.Notice)
	}
	if !strings.Contains(h.Notice, "wedged") && !strings.Contains(h.Notice, "stopped") {
		t.Errorf("notice %q should say the indexer may have stopped", h.Notice)
	}
	// Staleness is not a capability loss: the catalog still answers.
	if h.Degraded {
		t.Error("a stale-but-readable catalog is not a capability loss")
	}
	if !h.Healthy {
		t.Error("a stale catalog still answers queries, so healthy stays true")
	}
}

func TestHealth_NeverIndexedCatalogIsStale(t *testing.T) {
	ix, err := localindex.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })

	svc, err := New(WithLocal(ix))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.cass = nil

	h := svc.Health(context.Background())
	if !h.LocalStale {
		t.Error("a catalog that has never been indexed must report stale")
	}
	if !strings.Contains(h.Notice, "never been indexed") {
		t.Errorf("notice %q should distinguish never-indexed from merely old", h.Notice)
	}
}

func TestLocalStaleThresholdSecs_Override(t *testing.T) {
	t.Setenv("ALWE_INDEX_STALE_THRESHOLD", "")
	if got := localStaleThresholdSecs(); got != defaultLocalStaleThresholdSecs {
		t.Errorf("default = %d, want %d", got, defaultLocalStaleThresholdSecs)
	}
	t.Setenv("ALWE_INDEX_STALE_THRESHOLD", "120")
	if got := localStaleThresholdSecs(); got != 120 {
		t.Errorf("override = %d, want 120", got)
	}
	for _, bad := range []string{"soon", "0", "-1"} {
		t.Setenv("ALWE_INDEX_STALE_THRESHOLD", bad)
		if got := localStaleThresholdSecs(); got != defaultLocalStaleThresholdSecs {
			t.Errorf("value %q = %d, want the default", bad, got)
		}
	}
}

// Both signals can fire at once and must both survive into the notice.
func TestHealth_StaleCatalogAndCassComplaintBothReported(t *testing.T) {
	local, _ := localWithMtime(t, "sess-both", time.Now(), "content")
	verdict := `{"healthy":false,"errors":["index stale"]}`
	future := time.Now().Add(time.Duration(defaultLocalStaleThresholdSecs+100) * time.Second)

	svc, err := New(
		WithCass(cassObserverAt(t, fakeCassVerdict(t, 1, verdict))),
		WithLocal(local),
		WithClock(func() time.Time { return future }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := svc.Health(context.Background())
	if !h.LocalStale {
		t.Error("catalog should be stale")
	}
	if !strings.Contains(h.Notice, "cass is reachable but reports") {
		t.Errorf("notice %q lost cass's complaint", h.Notice)
	}
	if !strings.Contains(h.Notice, "local catalog last refreshed") {
		t.Errorf("notice %q lost the catalog staleness", h.Notice)
	}
}

// A just-refreshed catalog is age 0, which must serialise as 0 rather than be
// omitted — otherwise the healthiest possible answer renders as null and reads
// as "not computed".
func TestHealth_ZeroAgeSerialisesAsZero(t *testing.T) {
	local, _ := localWithMtime(t, "sess-zero", time.Now(), "content")
	fixed := time.Now()

	svc, err := New(WithLocal(local), WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.cass = nil

	h := svc.Health(context.Background())
	// Force the just-indexed case regardless of test timing.
	h.LocalAgeSeconds = 0

	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, present := decoded["local_age_seconds"]
	if !present {
		t.Fatal("local_age_seconds omitted at age 0; it must render as 0")
	}
	if v == nil {
		t.Errorf("local_age_seconds = null at age 0, want 0")
	}
	if f, ok := v.(float64); !ok || f != 0 {
		t.Errorf("local_age_seconds = %v, want 0", v)
	}
}
