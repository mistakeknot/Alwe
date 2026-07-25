package sessionsearch

import (
	"bufio"
	"os"
	"strings"
	"unicode"
)

// maxResolveBytes caps how much of a transcript is scanned when locating a
// snippet. Transcripts are usually well under this; anything larger is not
// worth the I/O for a ranking nicety.
const maxResolveBytes = 32 << 20

// resolveCassLines rewrites cass hits' LineNumber to true transcript line
// numbers, in place.
//
// cass's line_number is not a file line: for one transcript where the first
// occurrence of a term is on line 14, cass reported hits at lines 1 and 12.
// The local catalog, by contrast, counts JSONL records, which are one per line.
// Left alone, the two coordinate spaces never coincide, so deduping on
// (file_path, line_number) could not fire and the same passage found by both
// backends would consume two result slots and lose its corroboration boost.
//
// Locating cass's snippet text in the file puts both backends in the same
// coordinate space. When the snippet cannot be found the original value is kept
// and the hit simply does not merge, which is the pre-existing behaviour.
//
// Resolution is therefore best-effort, and known not to work for one class of
// hit: cass renders tool calls as synthetic text ("[Tool: Bash - <desc>]") that
// never appears literally in the transcript, so those keep cass's coordinates.
// Prose hits — user prompts and assistant text, the ones a duplicate is most
// annoying for — do resolve.
func resolveCassLines(hits []Hit) {
	// One pass per file, since several hits usually share a transcript.
	byFile := map[string][]int{}
	for i, h := range hits {
		if h.FilePath == "" || h.Snippet == "" {
			continue
		}
		byFile[h.FilePath] = append(byFile[h.FilePath], i)
	}

	for path, idxs := range byFile {
		needles := make([]string, len(idxs))
		for n, i := range idxs {
			needles[n] = snippetNeedle(hits[i].Snippet)
		}
		lines := findLines(path, needles)
		for n, i := range idxs {
			if lines[n] > 0 {
				hits[i].LineNumber = lines[n]
			}
		}
	}
}

// snippetNeedle reduces a snippet to a distinctive plain-text fragment that can
// be searched for in the raw transcript. cass wraps matches in ** and elides
// with an ellipsis; neither appears in the source.
func snippetNeedle(snippet string) string {
	s := strings.ReplaceAll(snippet, "**", "")
	s = strings.ReplaceAll(s, "…", "\x00")
	s = strings.ReplaceAll(s, "...", "\x00")

	// Use the longest un-elided run: the most specific fragment guaranteed to
	// be contiguous in the source.
	best := ""
	for _, part := range strings.Split(s, "\x00") {
		part = strings.TrimSpace(part)
		if len(part) > len(best) {
			best = part
		}
	}
	// Collapse internal whitespace runs to single spaces so the needle matches
	// regardless of how the snippet was wrapped.
	return strings.Join(strings.FieldsFunc(best, unicode.IsSpace), " ")
}

// findLines scans path once, returning the 1-based line number where each
// needle first appears (0 when not found).
func findLines(path string, needles []string) []int {
	out := make([]int, len(needles))

	fi, err := os.Stat(path)
	if err != nil || fi.Size() > maxResolveBytes {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20) // transcript lines can be large

	remaining := 0
	for _, n := range needles {
		if n != "" {
			remaining++
		}
	}

	for line := 1; sc.Scan() && remaining > 0; line++ {
		// Normalise the raw line the same way the needle was normalised, so
		// whitespace differences do not defeat the match. JSON escapes remain,
		// which is why short needles are skipped below.
		norm := ""
		for i, n := range needles {
			if out[i] != 0 || n == "" || len(n) < 12 {
				continue
			}
			if norm == "" {
				norm = strings.Join(strings.FieldsFunc(sc.Text(), unicode.IsSpace), " ")
			}
			if strings.Contains(norm, n) {
				out[i] = line
				remaining--
			}
		}
	}
	return out
}
