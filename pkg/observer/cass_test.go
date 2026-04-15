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

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string: got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("long string: got %q", got)
	}
}
