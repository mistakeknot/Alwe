// Package localindex maintains a SQLite FTS5 catalog over agent session
// transcripts, independent of cass.
//
// The unit of work is one transcript file. Files are keyed on (mtime, size),
// so an unchanged file is skipped and a changed file is re-indexed alone.
// There is deliberately no global fingerprint and no notion of a full rebuild:
// that design is what let a four-conversation drift in cass's lexical index
// require rebuilding all 9922 conversations, an operation that deadlocked.
// Here the worst case for any single drifted transcript is re-reading that
// transcript.
package localindex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Index is a local catalog of session transcripts.
type Index struct {
	db   *sql.DB
	path string
}

// Hit is one matching message. Field names mirror observer.SessionResult so
// callers can merge cass and local results without a translation table.
type Hit struct {
	SessionID  string  `json:"session_id"`
	Provider   string  `json:"provider"`
	Score      float64 `json:"score"`
	FilePath   string  `json:"file_path"`
	Snippet    string  `json:"snippet"`
	Timestamp  string  `json:"timestamp"`
	LineNumber int     `json:"line_number"`
}

// FileStat is the per-file actuals record for one indexing pass. Emitting
// these is what makes "only the changed file was touched" observable rather
// than asserted.
type FileStat struct {
	Path     string `json:"path"`
	Action   string `json:"action"` // "indexed", "skipped", "removed", "failed"
	Messages int    `json:"messages"`
	Duration string `json:"duration,omitempty"`
	Err      string `json:"error,omitempty"`
}

// Stats summarises an indexing pass.
type Stats struct {
	Scanned  int        `json:"scanned"`
	Indexed  int        `json:"indexed"`
	Skipped  int        `json:"skipped"`
	Removed  int        `json:"removed"`
	Failed   int        `json:"failed"`
	Messages int        `json:"messages"`
	Duration string     `json:"duration"`
	Files    []FileStat `json:"files,omitempty"`
}

// Coverage reports what the catalog currently holds.
type Coverage struct {
	Files       int    `json:"files"`
	Messages    int    `json:"messages"`
	NewestTS    string `json:"newest_timestamp,omitempty"`
	LastIndexed string `json:"last_indexed_at,omitempty"`
	// LastIndexedUnix is the same instant in epoch seconds, so callers can age
	// it without reparsing. Zero when nothing has been indexed yet.
	LastIndexedUnix int64  `json:"last_indexed_unix,omitempty"`
	DBPath          string `json:"db_path"`
}

const schema = `
CREATE TABLE IF NOT EXISTS files (
	path        TEXT PRIMARY KEY,
	mtime_ms    INTEGER NOT NULL,
	size        INTEGER NOT NULL,
	messages    INTEGER NOT NULL,
	rowid_lo    INTEGER NOT NULL,
	rowid_hi    INTEGER NOT NULL,
	indexed_at  INTEGER NOT NULL,
	duration_ms INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages USING fts5(
	body,
	path  UNINDEXED,
	line  UNINDEXED,
	ts    UNINDEXED,
	kind  UNINDEXED
);
`

// DefaultPath returns the catalog location, honouring ALWE_INDEX_DB.
func DefaultPath() string {
	if p := os.Getenv("ALWE_INDEX_DB"); p != "" {
		return p
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "alwe", "sessions.db")
}

// Open opens (creating if needed) the catalog at path.
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("catalog dir: %w", err)
	}
	// WAL keeps readers unblocked while an index pass writes, so a search
	// never waits on indexing — the failure mode this package exists to avoid.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open catalog: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("catalog schema: %w", err)
	}
	return &Index{db: db, path: path}, nil
}

// Path returns the catalog's location on disk.
func (ix *Index) Path() string { return ix.path }

// Close releases the catalog.
func (ix *Index) Close() error { return ix.db.Close() }

// DefaultRoots returns the transcript directories worth indexing.
func DefaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var roots []string
	for _, rel := range []string{
		".claude/projects", // Claude Code
		".codex/sessions",  // Codex
	} {
		p := filepath.Join(home, rel)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			roots = append(roots, p)
		}
	}
	return roots
}

// Index walks roots and brings the catalog up to date. Files whose mtime and
// size are unchanged since the last pass are skipped without being read.
func (ix *Index) Index(ctx context.Context, roots []string) (Stats, error) {
	start := time.Now()
	st := Stats{}

	known, err := ix.knownFiles(ctx)
	if err != nil {
		return st, err
	}

	seen := make(map[string]bool, len(known))
	for _, root := range roots {
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, don't abort the pass
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			st.Scanned++
			seen[path] = true

			fi, err := d.Info()
			if err != nil {
				st.Failed++
				st.Files = append(st.Files, FileStat{Path: path, Action: "failed", Err: err.Error()})
				return nil
			}
			mtime, size := fi.ModTime().UnixMilli(), fi.Size()

			if prev, ok := known[path]; ok && prev.mtime == mtime && prev.size == size {
				st.Skipped++
				st.Messages += prev.messages
				return nil
			}

			fileStart := time.Now()
			n, err := ix.indexFile(ctx, path, mtime, size, fileStart)
			if err != nil {
				st.Failed++
				st.Files = append(st.Files, FileStat{Path: path, Action: "failed", Err: err.Error()})
				return nil
			}
			st.Indexed++
			st.Messages += n
			st.Files = append(st.Files, FileStat{
				Path:     path,
				Action:   "indexed",
				Messages: n,
				Duration: time.Since(fileStart).Round(time.Millisecond).String(),
			})
			return nil
		})
		if walkErr != nil && ctx.Err() != nil {
			return st, ctx.Err()
		}
	}

	// Transcripts that vanished should stop appearing in results.
	for path := range known {
		if seen[path] {
			continue
		}
		if err := ix.forget(ctx, path); err != nil {
			continue
		}
		st.Removed++
		st.Files = append(st.Files, FileStat{Path: path, Action: "removed"})
	}

	st.Duration = time.Since(start).Round(time.Millisecond).String()
	return st, nil
}

type fileRecord struct {
	mtime    int64
	size     int64
	messages int
}

func (ix *Index) knownFiles(ctx context.Context) (map[string]fileRecord, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, mtime_ms, size, messages FROM files`)
	if err != nil {
		return nil, fmt.Errorf("read file table: %w", err)
	}
	defer rows.Close()

	out := map[string]fileRecord{}
	for rows.Next() {
		var p string
		var rec fileRecord
		if err := rows.Scan(&p, &rec.mtime, &rec.size, &rec.messages); err != nil {
			return nil, err
		}
		out[p] = rec
	}
	return out, rows.Err()
}

// indexFile replaces one transcript's rows in a single transaction. Rows for a
// file occupy a contiguous rowid range, so replacing them is an indexed delete
// rather than a scan of the whole catalog.
func (ix *Index) indexFile(ctx context.Context, path string, mtime, size int64, start time.Time) (int, error) {
	msgs, err := extractFile(path)
	if err != nil {
		return 0, err
	}

	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := deleteRange(ctx, tx, path); err != nil {
		return 0, err
	}

	var lo, hi int64
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages(body, path, line, ts, kind) VALUES(?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, m := range msgs {
		res, err := stmt.ExecContext(ctx, m.Body, path, m.Line, m.TS, m.Kind)
		if err != nil {
			return 0, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if lo == 0 {
			lo = id
		}
		hi = id
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO files(path, mtime_ms, size, messages, rowid_lo, rowid_hi, indexed_at, duration_ms)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(path) DO UPDATE SET
		   mtime_ms=excluded.mtime_ms, size=excluded.size, messages=excluded.messages,
		   rowid_lo=excluded.rowid_lo, rowid_hi=excluded.rowid_hi,
		   indexed_at=excluded.indexed_at, duration_ms=excluded.duration_ms`,
		path, mtime, size, len(msgs), lo, hi,
		time.Now().UnixMilli(), time.Since(start).Milliseconds())
	if err != nil {
		return 0, err
	}
	return len(msgs), tx.Commit()
}

func deleteRange(ctx context.Context, tx *sql.Tx, path string) error {
	var lo, hi int64
	err := tx.QueryRowContext(ctx, `SELECT rowid_lo, rowid_hi FROM files WHERE path = ?`, path).Scan(&lo, &hi)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if lo == 0 && hi == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE rowid BETWEEN ? AND ?`, lo, hi)
	return err
}

func (ix *Index) forget(ctx context.Context, path string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteRange(ctx, tx, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, path); err != nil {
		return err
	}
	return tx.Commit()
}

// Search runs an FTS5 query over the catalog, best match first.
func (ix *Index) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("empty query")
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := ix.db.QueryContext(ctx,
		`SELECT path, line, ts, kind, rank, snippet(messages, 0, '**', '**', '…', 20)
		   FROM messages WHERE messages MATCH ? ORDER BY rank LIMIT ?`,
		ftsQuery(query), limit)
	if err != nil {
		return nil, fmt.Errorf("local search: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var (
			path, ts, kind, snip string
			line                 int
			rank                 float64
		)
		if err := rows.Scan(&path, &line, &ts, &kind, &rank, &snip); err != nil {
			return nil, err
		}
		hits = append(hits, Hit{
			SessionID:  sessionIDFor(path),
			Provider:   providerFor(path),
			Score:      -rank, // FTS5 rank is negative-better; invert so higher is better
			FilePath:   path,
			Snippet:    strings.TrimSpace(snip),
			Timestamp:  ts,
			LineNumber: line,
		})
	}
	return hits, rows.Err()
}

// ftsQuery makes a user query safe for FTS5's expression grammar. Bare input
// like `foo-bar` or `a:b` is a syntax error there, so unless the caller is
// clearly writing FTS5 syntax we quote each term and AND them together.
func ftsQuery(q string) string {
	q = strings.TrimSpace(q)
	if strings.ContainsAny(q, `"`) {
		return q // caller is quoting deliberately
	}
	fields := strings.Fields(q)
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, ``)+`"`)
	}
	if len(quoted) == 0 {
		return `""`
	}
	return strings.Join(quoted, " AND ")
}

// Coverage reports catalog contents, so drift against cass is observable.
func (ix *Index) Coverage(ctx context.Context) (Coverage, error) {
	cov := Coverage{DBPath: ix.path}
	var newest sql.NullString
	var lastIdxMS sql.NullInt64

	err := ix.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(messages),0), MAX(indexed_at) FROM files`).
		Scan(&cov.Files, &cov.Messages, &lastIdxMS)
	if err != nil {
		return cov, err
	}
	if err := ix.db.QueryRowContext(ctx, `SELECT MAX(ts) FROM messages`).Scan(&newest); err != nil {
		return cov, err
	}
	if newest.Valid {
		cov.NewestTS = newest.String
	}
	if lastIdxMS.Valid {
		cov.LastIndexed = time.UnixMilli(lastIdxMS.Int64).UTC().Format(time.RFC3339)
		cov.LastIndexedUnix = lastIdxMS.Int64 / 1000
	}
	return cov, nil
}

// sessionIDFor derives the session id from a transcript filename, matching how
// cass reports it (basename without extension).
func sessionIDFor(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// providerFor infers the agent from the transcript location.
func providerFor(path string) string {
	switch {
	case strings.Contains(path, "/.claude/"):
		return "claude_code"
	case strings.Contains(path, "/.codex/"):
		return "codex"
	default:
		return "unknown"
	}
}

// message is one indexable unit of a transcript.
type message struct {
	Line int
	TS   string
	Kind string
	Body string
}

// extractFile pulls searchable text out of one JSONL transcript.
//
// This is deliberately separate from observer.ParseJSONLEvent, which serves
// live streaming: that returns only the first interesting block per line,
// stamps time.Now() rather than the transcript's own timestamp, and ignores
// plain user prompts. Indexing needs every block, real timestamps, and user
// prompts above all — they are what people search for.
func extractFile(path string) ([]message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.UseNumber()

	var out []message
	line := 0
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break // truncated or trailing garbage: index what we could read
		}
		line++
		if m, ok := extractLine(raw, line); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func extractLine(raw json.RawMessage, line int) (message, bool) {
	var env struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return message{}, false
	}

	switch env.Type {
	case "user", "assistant", "system":
	default:
		return message{}, false // bookkeeping records carry no searchable prose
	}

	body := strings.TrimSpace(contentText(env.Message.Content))
	if body == "" {
		return message{}, false
	}
	return message{Line: line, TS: env.Timestamp, Kind: env.Type, Body: body}, true
}

// contentText flattens a message content field, which is either a bare string
// (a plain user prompt) or an array of typed blocks.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var blocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Name     string          `json:"name"`
		Thinking string          `json:"thinking"`
		Content  json.RawMessage `json:"content"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "thinking":
			parts = append(parts, b.Thinking)
		case "tool_use":
			// Tool name plus its input: this is where file paths and commands
			// live, and they are heavily searched.
			parts = append(parts, "[tool: "+b.Name+"] "+flatten(b.Input, 2000))
		case "tool_result":
			parts = append(parts, flatten(b.Content, 4000))
		}
	}
	return strings.TrimSpace(strings.Join(nonEmpty(parts), "\n"))
}

// flatten renders arbitrary JSON as searchable text, capped at max bytes.
func flatten(raw json.RawMessage, max int) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			s = string(raw)
		} else {
			s = flattenValue(v)
		}
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func flattenValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, flattenValue(e))
		}
		return strings.Join(nonEmpty(parts), " ")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable output so re-indexing is deterministic
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, flattenValue(t[k]))
		}
		return strings.Join(nonEmpty(parts), " ")
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
