// Package observer watches agent sessions using the cass CLI.
package observer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// retryableCassExits are the exit codes cass declares as transient in its
// capability contract (`cass capabilities --json | jq .exit_codes`). Exit 7 is
// the common one: a scheduled `cass index` run owns the index lock, and cass's
// documented remedy is "retry later with bounded backoff" — not failing the
// caller. Treating it as fatal made routine indexer overlap look like an
// outage.
var retryableCassExits = map[int]string{
	4: "network error",
	7: "lock or busy",
}

const (
	cassMaxAttempts = 4
	cassBaseBackoff = 250 * time.Millisecond
)

// runCass executes cass and returns its stdout, retrying the exit codes cass
// marks retryable with exponential backoff. Other failures return immediately
// with the exit code and stderr attached so callers surface something
// actionable instead of a bare "exit status N".
func (o *CassObserver) runCass(ctx context.Context, args ...string) ([]byte, error) {
	backoff := cassBaseBackoff

	for attempt := 1; ; attempt++ {
		out, err := exec.CommandContext(ctx, o.cassPath, args...).Output()
		if err == nil {
			return out, nil
		}

		_, retryable := retryableCassExit(err)
		if !retryable || attempt == cassMaxAttempts {
			// Return stdout alongside the error: cass still emits a JSON
			// verdict on some non-zero exits (notably exit 1, "unhealthy"),
			// and callers that can use it should not have to re-run cass.
			return out, annotateCassError(err, attempt)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// retryableCassExit reports whether err is a cass exit that is worth retrying.
func retryableCassExit(err error) (string, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "", false
	}
	reason, ok := retryableCassExits[exitErr.ExitCode()]
	return reason, ok
}

// annotateCassError turns an opaque "exit status N" into something a caller
// (or an agent reading the MCP error) can act on.
func annotateCassError(err error, attempts int) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}

	code := exitErr.ExitCode()
	detail := truncate(strings.TrimSpace(string(exitErr.Stderr)), 300)

	if reason, ok := retryableCassExits[code]; ok {
		return fmt.Errorf("exit %d (%s) after %d attempts: %s", code, reason, attempts, detail)
	}
	if detail == "" {
		return fmt.Errorf("exit %d", code)
	}
	return fmt.Errorf("exit %d: %s", code, detail)
}

// SessionResult is a cass search hit.
type SessionResult struct {
	SessionID string  `json:"session_id"`
	Provider  string  `json:"provider"`
	Score     float64 `json:"score"`
	FilePath  string  `json:"file_path"`
	Snippet   string  `json:"snippet"`
	Timestamp string  `json:"timestamp"`
	// LineNumber locates the match within the transcript. Together with
	// FilePath it identifies a hit, which is what lets cass and local results
	// be merged without duplicates.
	LineNumber int `json:"line_number"`
}

// searchResponse mirrors the current `cass search --json` envelope.
type searchResponse struct {
	Hits []searchHit `json:"hits"`
}

// searchHit is one entry in the cass search hits array.
type searchHit struct {
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	SourcePath string  `json:"source_path"`
	Agent      string  `json:"agent"`
	CreatedAt  int64   `json:"created_at"` // ms epoch
	LineNumber int     `json:"line_number"`
}

// toSessionResult maps a cass hit onto Alwe's SessionResult shape.
func (h searchHit) toSessionResult() SessionResult {
	id := h.SourcePath
	if base := filepath.Base(h.SourcePath); base != "" && base != "." && base != string(filepath.Separator) {
		id = strings.TrimSuffix(base, filepath.Ext(base))
	}
	ts := ""
	if h.CreatedAt > 0 {
		ts = time.UnixMilli(h.CreatedAt).UTC().Format(time.RFC3339)
	}
	return SessionResult{
		SessionID:  id,
		Provider:   h.Agent,
		Score:      h.Score,
		FilePath:   h.SourcePath,
		Snippet:    h.Snippet,
		Timestamp:  ts,
		LineNumber: h.LineNumber,
	}
}

// parseSearchOutput decodes `cass search --json` output into SessionResults.
func parseSearchOutput(out []byte) ([]SessionResult, error) {
	var resp searchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	results := make([]SessionResult, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		results = append(results, h.toSessionResult())
	}
	return results, nil
}

// SearchSessions finds sessions matching a query, scoped to a connector.
func (o *CassObserver) SearchSessions(ctx context.Context, query string, connector string, limit int) ([]SessionResult, error) {
	args := []string{"search", query, "--json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	if connector != "" {
		args = append(args, "--agent", connector)
	}

	out, err := o.runCass(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("cass search: %w", err)
	}

	results, err := parseSearchOutput(out)
	if err != nil {
		return nil, fmt.Errorf("parsing cass search output: %w", err)
	}
	return results, nil
}

// ContextForFile finds sessions that touched a specific file path.
// Current `cass context` expects a session file and errors on arbitrary
// paths, so this runs a content search for the path instead.
func (o *CassObserver) ContextForFile(ctx context.Context, filePath string, limit int) ([]SessionResult, error) {
	args := []string{"search", filePath, "--json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}

	out, err := o.runCass(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("cass context: %w", err)
	}

	results, err := parseSearchOutput(out)
	if err != nil {
		return nil, fmt.Errorf("parsing cass context output: %w", err)
	}
	return results, nil
}

// ExportSession exports a session to structured text.
func (o *CassObserver) ExportSession(ctx context.Context, sessionPath string) (string, error) {
	out, err := o.runCass(ctx, "export", sessionPath, "--format", "markdown")
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

	out, err := o.runCass(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("cass timeline: %w", err)
	}
	return string(out), nil
}

// CassHealth is cass's self-report, separating two things that matter
// independently.
//
// cass judges itself unhealthy when its lexical index is merely older than a
// 300-second threshold, yet searches keep working fine in that state. Callers
// need to know whether cass can answer (Reachable) apart from whether cass
// considers itself fresh (Healthy), or a routinely-stale index reads as an
// outage.
type CassHealth struct {
	// Reachable means the binary ran and produced a verdict.
	Reachable bool `json:"reachable"`
	// Healthy is cass's own readiness verdict.
	Healthy bool `json:"healthy"`
	// Errors carries cass's stated problems, e.g. "index stale".
	Errors []string `json:"errors,omitempty"`
	// RecommendedAction is cass's own suggested remedy, when it offers one.
	RecommendedAction string `json:"recommended_action,omitempty"`
}

// HealthReport probes cass and reports both reachability and its self-verdict.
func (o *CassObserver) HealthReport(ctx context.Context) CassHealth {
	out, err := o.runCass(ctx, "health", "--json")

	var status struct {
		Healthy           bool     `json:"healthy"`
		Errors            []string `json:"errors"`
		RecommendedAction string   `json:"recommended_action"`
	}
	// cass prints its verdict even when exiting non-zero, so parse first and
	// let a successful parse establish reachability.
	if jsonErr := json.Unmarshal(out, &status); jsonErr == nil {
		return CassHealth{
			Reachable:         true,
			Healthy:           status.Healthy,
			Errors:            status.Errors,
			RecommendedAction: status.RecommendedAction,
		}
	}
	if err == nil {
		// Ran cleanly but produced output we could not parse.
		return CassHealth{Reachable: true}
	}
	return CassHealth{Reachable: false, Errors: []string{err.Error()}}
}

// IsAvailable reports whether cass is installed and considers itself healthy.
// Prefer HealthReport when a stale-but-usable cass should not read as absent.
func (o *CassObserver) IsAvailable(ctx context.Context) bool {
	out, err := o.runCass(ctx, "health", "--json")
	if err != nil {
		return false
	}
	var status struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return false
	}
	return status.Healthy
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
