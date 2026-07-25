package localindex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExportMarkdown renders a transcript as markdown.
//
// This is a package-level function, not a method: exporting needs the
// transcript file and nothing else. Requiring an open catalog would invent a
// dependency, and would mean a session that has not been indexed yet could not
// be exported — exactly the freshness gap the local path exists to close.
func ExportMarkdown(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("export %s: %w", path, err)
	}

	msgs, err := extractFile(path)
	if err != nil {
		return "", fmt.Errorf("export %s: %w", path, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Session %s\n\n", sessionIDFor(path))
	fmt.Fprintf(&b, "- **Source:** `%s`\n", path)
	fmt.Fprintf(&b, "- **Agent:** %s\n", providerFor(path))
	fmt.Fprintf(&b, "- **Messages:** %d\n", len(msgs))
	if len(msgs) > 0 {
		if ts := firstNonEmptyTS(msgs); ts != "" {
			fmt.Fprintf(&b, "- **First message:** %s\n", ts)
		}
		if ts := lastNonEmptyTS(msgs); ts != "" {
			fmt.Fprintf(&b, "- **Last message:** %s\n", ts)
		}
	}
	fmt.Fprintf(&b, "- **Rendered by:** alwe (local catalog path, no cass)\n\n")

	if len(msgs) == 0 {
		b.WriteString("_No renderable messages found in this transcript._\n")
		return b.String(), nil
	}

	b.WriteString("---\n\n")
	for _, m := range msgs {
		heading := roleHeading(m.Kind)
		if m.TS != "" {
			fmt.Fprintf(&b, "## %s — %s\n\n", heading, m.TS)
		} else {
			fmt.Fprintf(&b, "## %s\n\n", heading)
		}
		b.WriteString(strings.TrimSpace(m.Body))
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

// ExportMarkdownToFile renders a transcript and writes it to dest.
func ExportMarkdownToFile(path, dest string) error {
	md, err := ExportMarkdown(path)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("export dir: %w", err)
		}
	}
	return os.WriteFile(dest, []byte(md), 0o644)
}

func roleHeading(kind string) string {
	switch kind {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	default:
		return kind
	}
}

func firstNonEmptyTS(msgs []message) string {
	for _, m := range msgs {
		if m.TS != "" {
			return m.TS
		}
	}
	return ""
}

func lastNonEmptyTS(msgs []message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].TS != "" {
			return msgs[i].TS
		}
	}
	return ""
}
