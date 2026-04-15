// Package observer watches agent sessions using the cass CLI.
package observer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Event represents a structured observation from an agent session.
type Event struct {
	Type      string    `json:"type"` // "text", "tool_use", "tool_result", "error", "done"
	Text      string    `json:"text,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	ToolID    string    `json:"tool_id,omitempty"`
	IsError   bool      `json:"is_error,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// CassObserver watches agent sessions using the cass CLI.
// Two modes: real-time (tail JSONL) and query (cass search/context/export).
type CassObserver struct {
	cassPath string
}

// New creates a CassObserver. Returns an error if cass is not available.
func New() (*CassObserver, error) {
	path, err := exec.LookPath("cass")
	if err != nil {
		return nil, fmt.Errorf("cass not found in PATH: %w", err)
	}
	return &CassObserver{cassPath: path}, nil
}

// SessionResult is a cass search hit.
type SessionResult struct {
	SessionID string  `json:"session_id"`
	Provider  string  `json:"provider"`
	Score     float64 `json:"score"`
	FilePath  string  `json:"file_path"`
	Snippet   string  `json:"snippet"`
	Timestamp string  `json:"timestamp"`
}

// SearchSessions finds sessions matching a query, scoped to a connector.
func (o *CassObserver) SearchSessions(ctx context.Context, query string, connector string, limit int) ([]SessionResult, error) {
	args := []string{"search", query, "--robot", "--json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	if connector != "" {
		args = append(args, "--provider", connector)
	}

	cmd := exec.CommandContext(ctx, o.cassPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cass search: %w", err)
	}

	var results []SessionResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing cass search output: %w", err)
	}
	return results, nil
}

// ContextForFile finds sessions that touched a specific file path.
func (o *CassObserver) ContextForFile(ctx context.Context, filePath string, limit int) ([]SessionResult, error) {
	args := []string{"context", filePath, "--json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}

	cmd := exec.CommandContext(ctx, o.cassPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cass context: %w", err)
	}

	var results []SessionResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing cass context output: %w", err)
	}
	return results, nil
}

// ExportSession exports a session to structured text.
func (o *CassObserver) ExportSession(ctx context.Context, sessionPath string) (string, error) {
	cmd := exec.CommandContext(ctx, o.cassPath, "export", sessionPath, "--format", "markdown")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cass export: %w", err)
	}
	return string(out), nil
}

// TailSession tails a session JSONL file and sends parsed events to the
// channel. Provides real-time observation while CASS indexes async.
// Blocks until ctx is cancelled.
func (o *CassObserver) TailSession(ctx context.Context, jsonlPath string, events chan<- Event) error {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek to end: %w", err)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}
				ev, ok := ParseJSONLEvent(line)
				if !ok {
					continue
				}
				select {
				case events <- ev:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("scanner error: %w", err)
			}
		}
	}
}

// ParseJSONLEvent extracts an Event from a raw JSONL line.
// Handles Claude Code's stream-json format and generic agent JSONL.
func ParseJSONLEvent(line []byte) (Event, bool) {
	var envelope struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				ToolUseID string          `json:"tool_use_id"`
				Content   string          `json:"content"`
				IsError   bool            `json:"is_error"`
				Input     json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return Event{}, false
	}

	switch envelope.Type {
	case "assistant":
		for _, block := range envelope.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					return Event{Type: "text", Text: block.Text, Timestamp: time.Now()}, true
				}
			case "tool_use":
				return Event{Type: "tool_use", ToolName: block.Name, ToolID: block.ID, Timestamp: time.Now()}, true
			}
		}
	case "user":
		for _, block := range envelope.Message.Content {
			if block.Type == "tool_result" {
				return Event{
					Type:      "tool_result",
					ToolID:    block.ToolUseID,
					Text:      truncate(block.Content, 4096),
					IsError:   block.IsError,
					Timestamp: time.Now(),
				}, true
			}
		}
	case "result":
		return Event{Type: "done", Timestamp: time.Now()}, true
	}

	return Event{}, false
}

// Timeline returns recent activity across all agents.
func (o *CassObserver) Timeline(ctx context.Context, since string) (string, error) {
	args := []string{"timeline", "--json"}
	if since != "" {
		args = append(args, "--since", since)
	}

	cmd := exec.CommandContext(ctx, o.cassPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cass timeline: %w", err)
	}
	return string(out), nil
}

// IsAvailable reports whether cass is installed and healthy.
func (o *CassObserver) IsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, o.cassPath, "health", "--json")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"healthy":true`)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
