package localindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscript builds a JSONL transcript from raw line objects.
func writeTranscript(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// userPrompt is a plain-string content message — the shape real Claude Code
// transcripts use for typed prompts, and the one live-stream parsing drops.
func userPrompt(ts, text string) map[string]any {
	return map[string]any{
		"type":      "user",
		"timestamp": ts,
		"message":   map[string]any{"role": "user", "content": text},
	}
}

func assistantText(ts, text string) map[string]any {
	return map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
}

func assistantToolUse(ts, name string, input map[string]any) map[string]any {
	return map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "tool_use", "name": name, "input": input}},
		},
	}
}

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(filepath.Join(t.TempDir(), "cat", "sessions.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestIndexAndSearch_FindsUserPrompt(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, filepath.Join(root, "sess-a.jsonl"), []map[string]any{
		userPrompt("2026-07-16T15:16:34Z", "which session was working on cujgel most recently?"),
		assistantText("2026-07-16T15:16:40Z", "Let me check the beads tracker."),
	})

	ix := openTestIndex(t)
	st, err := ix.Index(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if st.Indexed != 1 || st.Messages != 2 {
		t.Fatalf("got indexed=%d messages=%d, want 1/2", st.Indexed, st.Messages)
	}

	hits, err := ix.Search(context.Background(), "cujgel", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected a hit for a user prompt")
	}
	h := hits[0]
	if h.SessionID != "sess-a" {
		t.Errorf("session_id = %q, want sess-a", h.SessionID)
	}
	if h.Timestamp != "2026-07-16T15:16:34Z" {
		t.Errorf("timestamp = %q, want the transcript's own timestamp", h.Timestamp)
	}
	if h.LineNumber != 1 {
		t.Errorf("line_number = %d, want 1", h.LineNumber)
	}
	if !strings.Contains(h.Snippet, "cujgel") {
		t.Errorf("snippet %q should contain the match", h.Snippet)
	}
}

func TestIndexAndSearch_FindsToolInputPaths(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, filepath.Join(root, "sess-b.jsonl"), []map[string]any{
		assistantToolUse("2026-07-16T09:09:51Z", "Read",
			map[string]any{"file_path": "/Users/sma/projects/cujgel/spec/cujgel.schema.json"}),
	})

	ix := openTestIndex(t)
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}

	hits, err := ix.Search(context.Background(), "cujgel.schema.json", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected tool_use input paths to be searchable")
	}
}

// The core guarantee: a drifted transcript is repaired alone. This is the
// property cass's global fingerprint could not offer.
func TestIndex_ReindexesOnlyChangedFile(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeTranscript(t, filepath.Join(root, "sess-"+name+".jsonl"), []map[string]any{
			userPrompt("2026-07-20T10:00:00Z", "hello from "+name),
		})
	}

	ix := openTestIndex(t)
	first, err := ix.Index(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Indexed != 3 {
		t.Fatalf("first pass indexed %d, want 3", first.Indexed)
	}

	// Second pass with nothing changed: everything skipped, nothing read.
	second, err := ix.Index(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Indexed != 0 || second.Skipped != 3 {
		t.Fatalf("second pass indexed=%d skipped=%d, want 0/3", second.Indexed, second.Skipped)
	}

	// Touch exactly one transcript.
	changed := filepath.Join(root, "sess-b.jsonl")
	writeTranscript(t, changed, []map[string]any{
		userPrompt("2026-07-20T10:00:00Z", "hello from b"),
		userPrompt("2026-07-21T11:00:00Z", "a brand new marmalade prompt"),
	})
	if err := os.Chtimes(changed, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	third, err := ix.Index(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if third.Indexed != 1 {
		t.Errorf("third pass indexed %d files, want exactly 1", third.Indexed)
	}
	if third.Skipped != 2 {
		t.Errorf("third pass skipped %d files, want 2", third.Skipped)
	}
	// The per-file actuals log must name the one file processed.
	var indexedPaths []string
	for _, f := range third.Files {
		if f.Action == "indexed" {
			indexedPaths = append(indexedPaths, f.Path)
		}
	}
	if len(indexedPaths) != 1 || indexedPaths[0] != changed {
		t.Errorf("actuals log reported %v, want just %s", indexedPaths, changed)
	}

	// New content is findable and the old row did not survive duplicated.
	hits, err := ix.Search(context.Background(), "marmalade", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits for new content, want 1", len(hits))
	}

	hits, err = ix.Search(context.Background(), "hello", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("got %d hits for unchanged content, want 3 (no duplicates from re-index)", len(hits))
	}
}

func TestIndex_RemovesDeletedTranscripts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sess-gone.jsonl")
	writeTranscript(t, path, []map[string]any{
		userPrompt("2026-07-20T10:00:00Z", "ephemeral zarquon content"),
	})

	ix := openTestIndex(t)
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	st, err := ix.Index(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if st.Removed != 1 {
		t.Errorf("removed = %d, want 1", st.Removed)
	}

	hits, _ := ix.Search(context.Background(), "zarquon", 5)
	if len(hits) != 0 {
		t.Errorf("deleted transcript still returns %d hits", len(hits))
	}
}

func TestCoverage(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, filepath.Join(root, "sess-cov.jsonl"), []map[string]any{
		userPrompt("2026-07-20T10:00:00Z", "first"),
		assistantText("2026-07-25T05:22:51Z", "last"),
	})

	ix := openTestIndex(t)
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}

	cov, err := ix.Coverage(context.Background())
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if cov.Files != 1 || cov.Messages != 2 {
		t.Errorf("coverage files=%d messages=%d, want 1/2", cov.Files, cov.Messages)
	}
	if cov.NewestTS != "2026-07-25T05:22:51Z" {
		t.Errorf("newest = %q, want the latest transcript timestamp", cov.NewestTS)
	}
	if cov.LastIndexed == "" {
		t.Error("last_indexed_at should be set after a pass")
	}
}

// FTS5's expression grammar rejects bare hyphens and colons; user queries
// must not become syntax errors.
func TestSearch_HandlesQueriesFTS5WouldReject(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, filepath.Join(root, "sess-q.jsonl"), []map[string]any{
		userPrompt("2026-07-20T10:00:00Z", "the cass-index launchd job wedged at phase-2"),
	})

	ix := openTestIndex(t)
	if _, err := ix.Index(context.Background(), []string{root}); err != nil {
		t.Fatalf("index: %v", err)
	}

	for _, q := range []string{"cass-index", "phase-2", "launchd job", "a:b"} {
		if _, err := ix.Search(context.Background(), q, 5); err != nil {
			t.Errorf("query %q returned error: %v", q, err)
		}
	}

	hits, err := ix.Search(context.Background(), "cass-index", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Error("hyphenated term should still match")
	}
}

func TestSearch_EmptyQueryRejected(t *testing.T) {
	ix := openTestIndex(t)
	if _, err := ix.Search(context.Background(), "   ", 5); err == nil {
		t.Error("expected an error for an empty query")
	}
}

func TestExtractFile_ToleratesTruncatedTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-trunc.jsonl")
	good, _ := json.Marshal(userPrompt("2026-07-20T10:00:00Z", "complete line kumquat"))
	// A crashed session leaves a half-written final line.
	if err := os.WriteFile(path, append(append(good, '\n'), []byte(`{"type":"user","messa`)...), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, err := extractFile(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (the complete line)", len(msgs))
	}
	if !strings.Contains(msgs[0].Body, "kumquat") {
		t.Errorf("body = %q", msgs[0].Body)
	}
}

func TestExtractLine_SkipsBookkeepingRecords(t *testing.T) {
	// Real transcripts carry hundreds of these per session; indexing them
	// would bloat the catalog with no searchable prose.
	for _, typ := range []string{"attachment", "bridge-session", "last-prompt", "mode", "queue-operation"} {
		raw, _ := json.Marshal(map[string]any{"type": typ, "timestamp": "2026-07-20T10:00:00Z"})
		if _, ok := extractLine(raw, 1); ok {
			t.Errorf("type %q should not be indexed", typ)
		}
	}
}
