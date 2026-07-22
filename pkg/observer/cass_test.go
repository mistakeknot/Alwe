package observer

import (
	"encoding/json"
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
