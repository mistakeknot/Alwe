package localindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", time.Hour},
		{"30m", 30 * time.Minute},
		{"2h", 2 * time.Hour},
		{"90s", 90 * time.Second},
		// d and w are cass vocabulary that time.ParseDuration rejects.
		{"1d", 24 * time.Hour},
		{"2d", 48 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"0.5d", 12 * time.Hour},
		{"24h", 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := ParseSince(c.in)
		if err != nil {
			t.Errorf("ParseSince(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"tomorrow", "5x", "d", "--"} {
		if _, err := ParseSince(bad); err == nil {
			t.Errorf("ParseSince(%q) should error", bad)
		}
	}
}

func TestTimeline_WindowsByTranscriptMtime(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	recent := filepath.Join(root, "sess-recent.jsonl")
	writeTranscript(t, recent, []map[string]any{
		userPrompt("2026-07-25T10:00:00Z", "recent work"),
		assistantText("2026-07-25T11:30:00Z", "recent reply"),
	})
	old := filepath.Join(root, "sess-old.jsonl")
	writeTranscript(t, old, []map[string]any{
		userPrompt("2026-06-01T10:00:00Z", "ancient work"),
	})

	// mtime is the window signal: a transcript is only written while its
	// session runs.
	if err := os.Chtimes(recent, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ix := openTestIndex(t)
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}

	res, err := ix.Timeline(context.Background(), "24h", now)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if res.Source != "local" {
		t.Errorf("source = %q, want local", res.Source)
	}
	if res.Totals.Sessions != 1 {
		t.Fatalf("got %d sessions in a 24h window, want 1 (the old one is 40d back)", res.Totals.Sessions)
	}
	s := res.Sessions[0]
	if s.SessionID != "sess-recent" {
		t.Errorf("session_id = %q, want sess-recent", s.SessionID)
	}
	if s.Messages != 2 {
		t.Errorf("messages = %d, want 2", s.Messages)
	}
	// First/last come from the transcript's own timestamps, not mtime.
	if s.First != "2026-07-25T10:00:00Z" || s.Last != "2026-07-25T11:30:00Z" {
		t.Errorf("first/last = %q/%q, want the transcript's own timestamps", s.First, s.Last)
	}
	if res.Totals.Messages != 2 {
		t.Errorf("total messages = %d, want 2", res.Totals.Messages)
	}

	// A wide enough window picks up both.
	wide, err := ix.Timeline(context.Background(), "8w", now)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if wide.Totals.Sessions != 2 {
		t.Errorf("got %d sessions in an 8w window, want 2", wide.Totals.Sessions)
	}
}

func TestTimeline_EmptyWindowIsNotNil(t *testing.T) {
	ix := openTestIndex(t)
	res, err := ix.Timeline(context.Background(), "1h", time.Now())
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	// Must marshal as [] rather than null so consumers can iterate blindly.
	if res.Sessions == nil {
		t.Error("Sessions should be an empty slice, not nil")
	}
	if res.Totals.Sessions != 0 {
		t.Errorf("totals = %+v, want zero", res.Totals)
	}
}

func TestTimeline_RejectsBadWindow(t *testing.T) {
	ix := openTestIndex(t)
	if _, err := ix.Timeline(context.Background(), "next tuesday", time.Now()); err == nil {
		t.Error("expected an error for an unparseable window")
	}
}

func TestExportMarkdown_RendersUserPrompts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-export.jsonl")
	writeTranscript(t, path, []map[string]any{
		userPrompt("2026-07-25T10:00:00Z", "which session was working on cujgel most recently?"),
		assistantText("2026-07-25T10:00:05Z", "Let me check the beads tracker."),
		assistantToolUse("2026-07-25T10:00:06Z", "Bash", map[string]any{"command": "bd show cujgel"}),
	})

	md, err := ExportMarkdown(path)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	for _, want := range []string{
		"# Session sess-export",
		"which session was working on cujgel most recently?", // the user prompt
		"Let me check the beads tracker.",
		"## User — 2026-07-25T10:00:00Z",
		"## Assistant",
		"bd show cujgel", // tool input is preserved
		"local catalog path, no cass",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if !strings.Contains(md, "**Messages:** 3") {
		t.Errorf("expected a message count in the header, got:\n%s", md[:min(400, len(md))])
	}
}

func TestExportMarkdown_MissingFile(t *testing.T) {
	if _, err := ExportMarkdown(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("expected an error for a missing transcript")
	}
}

func TestExportMarkdown_EmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-empty.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	md, err := ExportMarkdown(path)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(md, "No renderable messages") {
		t.Errorf("expected an explicit empty note, got:\n%s", md)
	}
}

func TestExportMarkdownToFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess-w.jsonl")
	writeTranscript(t, src, []map[string]any{userPrompt("2026-07-25T10:00:00Z", "hello quince")})

	dest := filepath.Join(dir, "nested", "out.md")
	if err := ExportMarkdownToFile(src, dest); err != nil {
		t.Fatalf("export to file: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !strings.Contains(string(b), "hello quince") {
		t.Errorf("written file missing content: %s", b)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
