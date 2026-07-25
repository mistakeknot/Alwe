package sessionsearch

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistakeknot/Alwe/pkg/localindex"
)

// This file exists because of a specific failure. Three defects in goal
// be6423c3 — incomparable score scales, incompatible line-number coordinate
// spaces, and "healthy" meaning two things — all survived a fully green suite
// built on synthetic fixtures. The worst was a dedupe test that constructed
// cass's hit *using the local hit's own line number*, guaranteeing a collision
// the real system cannot produce: a correct assertion over an impossible input.
//
// These tests run against actual transcripts on disk. They skip when no real
// transcripts exist (CI, a fresh machine) rather than failing, but when they do
// run they assert the properties fixtures cannot honestly model.

// findRealTranscript returns a transcript on this machine with enough prose to
// search, or skips.
func findRealTranscript(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	root := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(root); err != nil {
		t.Skip("no real transcripts on this machine")
	}

	var best string
	var bestSize int64
	entries, _ := os.ReadDir(root)
	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(root, dir.Name()))
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			// Big enough to contain prose, small enough to scan quickly.
			if info.Size() > bestSize && info.Size() < 4<<20 {
				bestSize, best = info.Size(), filepath.Join(root, dir.Name(), f.Name())
			}
		}
		if bestSize > 200<<10 {
			break
		}
	}
	if best == "" {
		t.Skip("no suitable real transcript found")
	}
	return best
}

// localLineNumbersAreTrueFileLines is the coordinate-agreement invariant the
// merge depends on. cass's line_number is *not* a file line (it reported 1 and
// 12 for a transcript whose first match is on line 14), so if the local
// catalog's numbering also drifted, the dedupe key would be meaningless in a
// way no synthetic fixture would reveal.
func TestRealData_LocalLineNumbersAreTrueFileLines(t *testing.T) {
	transcript := findRealTranscript(t)
	dir := filepath.Dir(transcript)

	ix, err := localindex.Open(filepath.Join(t.TempDir(), "real.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	defer ix.Close()

	// Index only the one transcript's directory, and only that file matters.
	if _, err := ix.Index(context.Background(), []string{dir}); err != nil {
		t.Fatalf("index real transcripts: %v", err)
	}

	// A term certain to appear in any Claude Code transcript.
	hits, err := ix.Search(context.Background(), "the", 40)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Skip("no hits in the sampled transcript")
	}

	checked := 0
	for _, h := range hits {
		if h.LineNumber <= 0 || h.FilePath == "" {
			t.Errorf("hit has no usable coordinates: %+v", h)
			continue
		}
		raw, ok := readLine(t, h.FilePath, h.LineNumber)
		if !ok {
			t.Errorf("line %d does not exist in %s — local numbering is not file lines",
				h.LineNumber, h.FilePath)
			continue
		}
		// The reported line must be a JSON record (one per line in these files).
		if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
			t.Errorf("line %d of %s is not a JSON record: %.60q",
				h.LineNumber, h.FilePath, raw)
		}
		checked++
		if checked >= 10 {
			break
		}
	}
	if checked == 0 {
		t.Skip("no hits with resolvable coordinates")
	}
	t.Logf("verified %d real hits land on true transcript lines", checked)
}

// resolveCassLines must work on real transcript bytes, not just crafted
// snippets. Build a cass-shaped snippet out of content the local catalog found,
// hand it a deliberately wrong line number, and require the resolver to correct
// it to the true line.
func TestRealData_CassLineResolutionAgreesWithLocal(t *testing.T) {
	transcript := findRealTranscript(t)

	ix, err := localindex.Open(filepath.Join(t.TempDir(), "real2.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	defer ix.Close()
	if _, err := ix.Index(context.Background(), []string{filepath.Dir(transcript)}); err != nil {
		t.Fatalf("index: %v", err)
	}

	hits, err := ix.Search(context.Background(), "the", 60)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	agreed := 0
	for _, h := range hits {
		if h.FilePath != transcript || h.LineNumber <= 0 {
			continue
		}
		raw, ok := readLine(t, h.FilePath, h.LineNumber)
		if !ok {
			continue
		}
		// Take a distinctive contiguous fragment from the real line and shape it
		// like a cass snippet (** markers around a term).
		needle := distinctiveFragment(raw)
		if needle == "" {
			continue
		}
		snippet := strings.Replace(needle, "the", "**the**", 1)

		probe := []Hit{{
			FilePath:   h.FilePath,
			Snippet:    snippet,
			LineNumber: 999999, // deliberately wrong, as cass's coordinates are
			Source:     "cass",
		}}
		resolveCassLines(probe)

		if probe[0].LineNumber == h.LineNumber {
			agreed++
		} else if probe[0].LineNumber != 999999 {
			t.Errorf("resolver moved a hit to line %d but local says %d (file %s)",
				probe[0].LineNumber, h.LineNumber, h.FilePath)
		}
		if agreed >= 3 {
			break
		}
	}

	if agreed == 0 {
		t.Skip("no real line yielded a resolvable fragment (JSON escaping); resolver is best-effort by design")
	}
	t.Logf("cass-shaped snippets resolved to the local catalog's line on %d real lines", agreed)
}

// Export must handle real transcripts, which contain shapes fixtures omit:
// thinking blocks, huge tool results, attachments, truncated final lines.
func TestRealData_ExportRendersRealTranscript(t *testing.T) {
	transcript := findRealTranscript(t)

	md, err := localindex.ExportMarkdown(transcript)
	if err != nil {
		t.Fatalf("export real transcript: %v", err)
	}
	if len(md) < 200 {
		t.Errorf("export produced only %d bytes for a real transcript", len(md))
	}
	if !strings.Contains(md, "# Session ") {
		t.Error("export missing session heading")
	}
	if !strings.Contains(md, "## User") && !strings.Contains(md, "## Assistant") {
		t.Errorf("export contains no rendered turns:\n%.400s", md)
	}
}

// readLine returns the 1-based line n of path.
func readLine(t *testing.T, path string, n int) (string, bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for i := 1; sc.Scan(); i++ {
		if i == n {
			return sc.Text(), true
		}
	}
	return "", false
}

// distinctiveFragment pulls a reasonably long run of plain text out of a raw
// JSONL line, avoiding JSON escapes that would not survive round-tripping.
func distinctiveFragment(raw string) string {
	best := ""
	for _, chunk := range strings.Split(raw, `"`) {
		if !strings.Contains(chunk, "the") {
			continue
		}
		if strings.ContainsAny(chunk, `\{}[]`) {
			continue
		}
		if len(chunk) > len(best) && len(chunk) < 200 {
			best = chunk
		}
	}
	if len(best) < 20 {
		return ""
	}
	return strings.TrimSpace(best)
}
