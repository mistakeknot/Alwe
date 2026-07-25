package sessionsearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSnippetNeedle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Check zklw for **cujgel** repo", "Check zklw for cujgel repo"},
		{"…which session were we working on **cujgel**?", "which session were we working on cujgel?"},
		// The longest un-elided run wins: it is the fragment guaranteed to be
		// contiguous in the source.
		{"short…a much longer contiguous **fragment** here", "a much longer contiguous fragment here"},
		{"multi\n  line   text", "multi line text"},
	}
	for _, c := range cases {
		if got := snippetNeedle(c.in); got != c.want {
			t.Errorf("snippetNeedle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveCassLines_FindsTrueTranscriptLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")

	// Three records; the interesting prose is on line 3.
	var content []byte
	for i, text := range []string{"first message", "second message", "which session were we working on cujgel?"} {
		line, _ := json.Marshal(map[string]any{
			"type":      "user",
			"timestamp": "2026-07-16T15:16:34Z",
			"message":   map[string]any{"role": "user", "content": text},
		})
		content = append(content, line...)
		content = append(content, '\n')
		_ = i
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// cass reports line 1, which is not where the text lives.
	hits := []Hit{{
		FilePath:   path,
		Snippet:    "which session were we working on **cujgel**?",
		LineNumber: 1,
		Source:     "cass",
	}}
	resolveCassLines(hits)

	if hits[0].LineNumber != 3 {
		t.Errorf("LineNumber = %d, want 3 (the true transcript line)", hits[0].LineNumber)
	}
}

func TestResolveCassLines_KeepsOriginalWhenUnfindable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	line, _ := json.Marshal(map[string]any{
		"type":    "assistant",
		"message": map[string]any{"role": "assistant", "content": "nothing relevant"},
	})
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// A synthetic tool-call rendering: cass generates this, so it is not in the
	// transcript and must not be "resolved" to a wrong line.
	hits := []Hit{{
		FilePath:   path,
		Snippet:    "[Tool: Bash - Check zklw for **cujgel** repo]",
		LineNumber: 12,
		Source:     "cass",
	}}
	resolveCassLines(hits)

	if hits[0].LineNumber != 12 {
		t.Errorf("LineNumber = %d, want the original 12 when the snippet is unfindable", hits[0].LineNumber)
	}
}

func TestResolveCassLines_ToleratesMissingFile(t *testing.T) {
	hits := []Hit{{FilePath: "/nonexistent/x.jsonl", Snippet: "some longer snippet text", LineNumber: 5}}
	resolveCassLines(hits) // must not panic
	if hits[0].LineNumber != 5 {
		t.Errorf("LineNumber = %d, want 5 preserved", hits[0].LineNumber)
	}
}
