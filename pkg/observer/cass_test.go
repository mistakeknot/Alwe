package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJSONLEvent_TextBlock(t *testing.T) {
	msg := map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Hello world"},
			},
		},
	}
	line, _ := json.Marshal(msg)
	ev, ok := ParseJSONLEvent(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Type != "text" || ev.Text != "Hello world" {
		t.Errorf("got %+v", ev)
	}
}

func TestParseJSONLEvent_ToolUse(t *testing.T) {
	msg := map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []map[string]interface{}{
				{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": map[string]string{"command": "ls"}},
			},
		},
	}
	line, _ := json.Marshal(msg)
	ev, ok := ParseJSONLEvent(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Type != "tool_use" || ev.ToolName != "Bash" || ev.ToolID != "tu_1" {
		t.Errorf("got %+v", ev)
	}
}

func TestParseJSONLEvent_ToolResult(t *testing.T) {
	msg := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "file.txt", "is_error": false},
			},
		},
	}
	line, _ := json.Marshal(msg)
	ev, ok := ParseJSONLEvent(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Type != "tool_result" || ev.ToolID != "tu_1" || ev.Text != "file.txt" {
		t.Errorf("got %+v", ev)
	}
}

func TestParseJSONLEvent_Result(t *testing.T) {
	line, _ := json.Marshal(map[string]interface{}{"type": "result"})
	ev, ok := ParseJSONLEvent(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Type != "done" {
		t.Errorf("got type %q, want done", ev.Type)
	}
}

func TestParseJSONLEvent_InvalidJSON(t *testing.T) {
	_, ok := ParseJSONLEvent([]byte("not json"))
	if ok {
		t.Error("expected no event for invalid JSON")
	}
}

func TestParseJSONLEvent_UnknownType(t *testing.T) {
	line, _ := json.Marshal(map[string]interface{}{"type": "unknown"})
	_, ok := ParseJSONLEvent(line)
	if ok {
		t.Error("expected no event for unknown type")
	}
}

func TestParseSearchOutput(t *testing.T) {
	out := []byte(`{
		"query": "kimi",
		"limit": 2,
		"count": 1,
		"total_matches": 1,
		"hits": [
			{
				"title": "some session",
				"snippet": "hello **kimi**",
				"content": "hello kimi",
				"score": 12.5,
				"source_path": "/Users/x/.claude/projects/-w/abc123.jsonl",
				"agent": "claude_code",
				"workspace": "/Users/x/projects",
				"created_at": 1784737086683,
				"line_number": 42,
				"match_type": "exact"
			}
		]
	}`)
	results, err := parseSearchOutput(out)
	if err != nil {
		t.Fatalf("parseSearchOutput: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want abc123", r.SessionID)
	}
	if r.Provider != "claude_code" {
		t.Errorf("Provider = %q, want claude_code", r.Provider)
	}
	if r.FilePath != "/Users/x/.claude/projects/-w/abc123.jsonl" {
		t.Errorf("FilePath = %q", r.FilePath)
	}
	if r.Snippet != "hello **kimi**" {
		t.Errorf("Snippet = %q", r.Snippet)
	}
	if r.Score != 12.5 {
		t.Errorf("Score = %v", r.Score)
	}
	if r.Timestamp != "2026-07-22T16:18:06Z" {
		t.Errorf("Timestamp = %q", r.Timestamp)
	}
}

func TestParseSearchOutput_Empty(t *testing.T) {
	results, err := parseSearchOutput([]byte(`{"query":"x","hits":[]}`))
	if err != nil {
		t.Fatalf("parseSearchOutput: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string: got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("long string: got %q", got)
	}
}

// fakeCass writes a script standing in for the cass binary. It exits with
// exitCode for the first failCount invocations (tracked via a counter file),
// then prints stdout and succeeds.
func fakeCass(t *testing.T, exitCode, failCount int, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "cass")
	counter := filepath.Join(dir, "n")

	body := fmt.Sprintf(`#!/bin/sh
n=0
[ -f %[1]q ] && n=$(cat %[1]q)
n=$((n+1))
echo "$n" > %[1]q
if [ "$n" -le %[2]d ]; then
  echo "index-run.lock held by pid 999" >&2
  exit %[3]d
fi
printf '%%s' %[4]q
`, counter, failCount, exitCode, stdout)

	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake cass: %v", err)
	}
	return script
}

func TestRunCass_RetriesLockBusy(t *testing.T) {
	// Exit 7 ("lock or busy") is retryable: two failures then success.
	o := &CassObserver{cassPath: fakeCass(t, 7, 2, `{"healthy":true}`)}

	out, err := o.runCass(context.Background(), "health", "--json")
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if string(out) != `{"healthy":true}` {
		t.Errorf("got stdout %q", out)
	}
}

func TestRunCass_GivesUpAfterMaxAttempts(t *testing.T) {
	// Always busy: error must name the exit code, reason, and attempt count.
	o := &CassObserver{cassPath: fakeCass(t, 7, 99, "")}

	_, err := o.runCass(context.Background(), "search", "q")
	if err == nil {
		t.Fatal("expected error when lock never releases")
	}
	for _, want := range []string{"exit 7", "lock or busy", fmt.Sprintf("%d attempts", cassMaxAttempts)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestRunCass_DoesNotRetryNonRetryable(t *testing.T) {
	// Exit 1 is cass's "unhealthy" verdict — a real answer, not contention.
	// It must be returned on the first attempt, with stderr attached.
	script := fakeCass(t, 1, 99, "")
	o := &CassObserver{cassPath: script}

	_, err := o.runCass(context.Background(), "health", "--json")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("error %q should name exit 1", err)
	}
	if strings.Contains(err.Error(), "attempts") {
		t.Errorf("error %q should not report retries for a non-retryable code", err)
	}

	n, err := os.ReadFile(filepath.Join(filepath.Dir(script), "n"))
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if strings.TrimSpace(string(n)) != "1" {
		t.Errorf("cass ran %s times, want 1", strings.TrimSpace(string(n)))
	}
}

// Regression, carried over from the deleted IsAvailable: a passing `cass index`
// used to make the health probe report false. The surviving path must retry
// through lock contention and land on cass's real verdict.
func TestHealthReport_SurvivesIndexerLock(t *testing.T) {
	o := &CassObserver{cassPath: fakeCass(t, 7, 2, `{"healthy":true}`)}

	got := o.HealthReport(context.Background())
	if !got.Reachable {
		t.Error("HealthReport should retry through lock contention and report reachable")
	}
	if !got.Healthy {
		t.Errorf("cass reported healthy after the lock cleared, got %+v", got)
	}
}

// The property the deleted IsAvailable got wrong: a stale cass is still usable.
// It collapsed cass's verdict into one bool and probed at the strict 300s
// default, so a routinely-stale-but-working cass read as absent.
func TestHealthReport_StaleCassIsStillUsable(t *testing.T) {
	stale := `{"healthy":false,"errors":["index stale"],"recommended_action":"run cass index"}`
	o := &CassObserver{cassPath: fakeCass(t, 0, 0, stale)}

	got := o.HealthReport(context.Background())
	if !got.Reachable {
		t.Fatal("a stale cass that answers is reachable and must be used, not skipped")
	}
	if got.Healthy {
		t.Error("cass's own unhealthy verdict must be preserved, not overwritten")
	}
	if len(got.Errors) == 0 || got.Errors[0] != "index stale" {
		t.Errorf("errors = %v, want cass's stated problem", got.Errors)
	}
}

func TestStaleThresholdArg(t *testing.T) {
	// Default aligns with cass's own `status` threshold, not health's stricter
	// 300s: a ~51s incremental index on a 900s schedule would otherwise mark
	// cass unhealthy for most of every cycle while search works fine.
	t.Setenv("ALWE_CASS_STALE_THRESHOLD", "")
	got := staleThresholdArg()
	if len(got) != 2 || got[0] != "--stale-threshold" || got[1] != "1800" {
		t.Errorf("default = %v, want [--stale-threshold 1800]", got)
	}

	t.Setenv("ALWE_CASS_STALE_THRESHOLD", "600")
	if got := staleThresholdArg(); got[1] != "600" {
		t.Errorf("override = %v, want 600", got)
	}

	// Garbage and non-positive values fall back rather than passing nonsense
	// through to cass.
	for _, bad := range []string{"abc", "0", "-5"} {
		t.Setenv("ALWE_CASS_STALE_THRESHOLD", bad)
		if got := staleThresholdArg(); got[1] != "1800" {
			t.Errorf("value %q = %v, want the default", bad, got)
		}
	}
}

func TestHealthReport_SeparatesReachableFromVerdict(t *testing.T) {
	// cass answered with an unhealthy verdict: reachable, but its own judgement
	// must be preserved rather than collapsed into "unavailable". (The
	// exit-1-with-body variant is covered in pkg/sessionsearch.)
	script := fakeCass(t, 0, 0, `{"healthy":false,"errors":["index stale"],"recommended_action":"run cass index"}`)
	o := &CassObserver{cassPath: script}

	got := o.HealthReport(context.Background())
	if !got.Reachable {
		t.Error("cass answered, so it must be reachable")
	}
	if got.Healthy {
		t.Error("cass's own verdict was unhealthy and must be preserved")
	}
	if len(got.Errors) == 0 || got.Errors[0] != "index stale" {
		t.Errorf("errors = %v, want cass's stated problem", got.Errors)
	}
	if got.RecommendedAction == "" {
		t.Error("cass's recommended action should be surfaced")
	}
}

func TestHealthReport_UnreachableWhenCassCannotRun(t *testing.T) {
	o := &CassObserver{cassPath: filepath.Join(t.TempDir(), "does-not-exist")}
	got := o.HealthReport(context.Background())
	if got.Reachable || got.Healthy {
		t.Errorf("expected unreachable+unhealthy, got %+v", got)
	}
}
