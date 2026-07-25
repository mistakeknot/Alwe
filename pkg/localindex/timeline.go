package localindex

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SessionActivity is one session's activity inside a timeline window.
type SessionActivity struct {
	SessionID  string `json:"session_id"`
	Provider   string `json:"provider"`
	FilePath   string `json:"file_path"`
	Messages   int    `json:"messages"`
	First      string `json:"first_message,omitempty"`
	Last       string `json:"last_message,omitempty"`
	ModifiedAt string `json:"modified_at"`
}

// TimelineResult is recent activity derived from the local catalog.
type TimelineResult struct {
	Since    string            `json:"since"`
	Cutoff   string            `json:"cutoff"`
	Source   string            `json:"source"`
	Sessions []SessionActivity `json:"sessions"`
	Totals   TimelineTotals    `json:"totals"`
	// Notice states why this came from the catalog rather than cass, when the
	// caller reached the local path as a fallback.
	Notice string `json:"notice,omitempty"`
}

// TimelineTotals summarises a timeline window.
type TimelineTotals struct {
	Sessions int `json:"sessions"`
	Messages int `json:"messages"`
}

// ParseSince converts a window like "1h", "90m", "2d", "1w" into a duration.
//
// time.ParseDuration handles h/m/s but not the d and w units cass accepts, so
// those are expanded here rather than rejected.
func ParseSince(since string) (time.Duration, error) {
	s := strings.TrimSpace(strings.ToLower(since))
	if s == "" {
		return time.Hour, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid window %q", since)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	if n, ok := strings.CutSuffix(s, "w"); ok {
		weeks, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid window %q", since)
		}
		return time.Duration(weeks * 7 * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid window %q", since)
	}
	return d, nil
}

// Timeline reports sessions active within the window, most recent first.
//
// The window is applied to transcript mtime, which is both already numeric and
// indexed and the honest signal for "was this session active recently" — a
// transcript is only written while its session runs. Per-session message
// timestamps are then read from the catalog by rowid range, so this stays a
// handful of indexed lookups rather than a scan.
func (ix *Index) Timeline(ctx context.Context, since string, now time.Time) (TimelineResult, error) {
	d, err := ParseSince(since)
	if err != nil {
		return TimelineResult{}, err
	}
	cutoff := now.Add(-d)

	res := TimelineResult{
		Since:    since,
		Cutoff:   cutoff.UTC().Format(time.RFC3339),
		Source:   "local",
		Sessions: []SessionActivity{},
	}

	rows, err := ix.db.QueryContext(ctx,
		`SELECT path, messages, mtime_ms, rowid_lo, rowid_hi
		   FROM files WHERE mtime_ms >= ? ORDER BY mtime_ms DESC`,
		cutoff.UnixMilli())
	if err != nil {
		return res, fmt.Errorf("local timeline: %w", err)
	}
	defer rows.Close()

	type entry struct {
		path     string
		messages int
		mtime    int64
		lo, hi   int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.path, &e.messages, &e.mtime, &e.lo, &e.hi); err != nil {
			return res, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	for _, e := range entries {
		act := SessionActivity{
			SessionID:  sessionIDFor(e.path),
			Provider:   providerFor(e.path),
			FilePath:   e.path,
			Messages:   e.messages,
			ModifiedAt: time.UnixMilli(e.mtime).UTC().Format(time.RFC3339),
		}
		var first, last sql.NullString
		err := ix.db.QueryRowContext(ctx,
			`SELECT MIN(ts), MAX(ts) FROM messages WHERE rowid BETWEEN ? AND ?`,
			e.lo, e.hi).Scan(&first, &last)
		if err != nil && err != sql.ErrNoRows {
			return res, err
		}
		if first.Valid {
			act.First = first.String
		}
		if last.Valid {
			act.Last = last.String
		}
		res.Sessions = append(res.Sessions, act)
		res.Totals.Messages += e.messages
	}
	res.Totals.Sessions = len(res.Sessions)
	return res, nil
}
