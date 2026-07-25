// Command alwe is a universal agent observation tool.
//
// As an MCP server (default): exposes session data as MCP tools.
// As a CLI: search, index, export, and stream agent sessions.
//
// Search works with or without cass. cass supplies better ranking and a larger
// corpus; the local FTS5 catalog supplies availability. Commands that only cass
// can serve (timeline, export) say so plainly when it is missing.
//
// Usage:
//
//	alwe                          # start MCP server on stdio
//	alwe search "auth bug"        # search sessions
//	alwe search --connector codex "fix"  # search codex sessions
//	alwe index                    # refresh the local catalog
//	alwe timeline --since 2h      # recent activity (cass)
//	alwe export <session.jsonl>   # export session to markdown (cass)
//	alwe context <file-path>      # sessions that touched a file
//	alwe health                   # backend status and build id
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mistakeknot/Alwe/internal/mcpserver"
	"github.com/mistakeknot/Alwe/pkg/localindex"
	"github.com/mistakeknot/Alwe/pkg/sessionsearch"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// No subcommand = MCP server mode.
	if len(os.Args) < 2 || os.Args[1] == "serve" {
		s, err := mcpserver.New()
		if err != nil {
			log.Fatalf("alwe init: %v", err)
		}
		defer s.Close()
		if err := s.Run(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("alwe run: %v", err)
		}
		return
	}

	if os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
		usage()
		return
	}

	svc, err := sessionsearch.New()
	if err != nil {
		log.Fatalf("alwe: %v", err)
	}
	defer svc.Close()

	switch os.Args[1] {
	case "search":
		cmdSearch(ctx, svc, os.Args[2:])
	case "index":
		cmdIndex(ctx, svc, os.Args[2:])
	case "timeline":
		cmdTimeline(ctx, svc, os.Args[2:])
	case "export":
		cmdExport(ctx, svc, os.Args[2:])
	case "context":
		cmdContext(ctx, svc, os.Args[2:])
	case "health":
		cmdHealth(ctx, svc)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

// parseFlags parses args while allowing flags after the positional argument
// (the standard flag package stops at the first non-flag argument).
func parseFlags(fs *flag.FlagSet, args []string) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			positional = append(positional, a)
		}
	}
	fs.Parse(append(flags, positional...))
}

// emit writes v as indented JSON to stdout.
func emit(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Fatalf("encode: %v", err)
	}
}

func cmdSearch(ctx context.Context, svc *sessionsearch.Service, args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	connector := fs.String("connector", "", "Filter by agent connector")
	limit := fs.Int("limit", 10, "Maximum results")
	parseFlags(fs, args)

	query := fs.Arg(0)
	if query == "" {
		log.Fatal("usage: alwe search [--connector X] [--limit N] <query>")
	}

	res, err := svc.Search(ctx, query, *connector, *limit)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	// The notice goes to stderr so it reaches a human without corrupting the
	// JSON on stdout; it is also carried in the payload for programmatic use.
	if res.Notice != "" {
		fmt.Fprintf(os.Stderr, "notice: %s\n", res.Notice)
	}
	emit(res)
}

func cmdIndex(ctx context.Context, svc *sessionsearch.Service, args []string) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "Omit the per-file actuals log")
	parseFlags(fs, args)

	roots := fs.Args()
	if len(roots) == 0 {
		roots = localindex.DefaultRoots()
	}
	if len(roots) == 0 {
		log.Fatal("index: no transcript roots found (pass them explicitly)")
	}

	st, err := svc.Index(ctx, roots)
	if err != nil {
		log.Fatalf("index: %v", err)
	}
	if *quiet {
		st.Files = nil
	}
	emit(st)
}

func cmdTimeline(ctx context.Context, svc *sessionsearch.Service, args []string) {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	since := fs.String("since", "1h", "Time range")
	parseFlags(fs, args)

	tl, err := svc.Timeline(ctx, *since)
	if err != nil {
		log.Fatalf("timeline: %v", err)
	}
	fmt.Print(tl)
}

func cmdExport(ctx context.Context, svc *sessionsearch.Service, args []string) {
	if len(args) < 1 {
		log.Fatal("usage: alwe export <session-path>")
	}
	md, err := svc.ExportSession(ctx, args[0])
	if err != nil {
		log.Fatalf("export: %v", err)
	}
	fmt.Print(md)
}

func cmdContext(ctx context.Context, svc *sessionsearch.Service, args []string) {
	fs := flag.NewFlagSet("context", flag.ExitOnError)
	limit := fs.Int("limit", 5, "Maximum results")
	parseFlags(fs, args)

	filePath := fs.Arg(0)
	if filePath == "" {
		log.Fatal("usage: alwe context <file-path>")
	}

	res, err := svc.ContextForFile(ctx, filePath, *limit)
	if err != nil {
		log.Fatalf("context: %v", err)
	}
	if res.Notice != "" {
		fmt.Fprintf(os.Stderr, "notice: %s\n", res.Notice)
	}
	emit(res)
}

func cmdHealth(ctx context.Context, svc *sessionsearch.Service) {
	h := svc.Health(ctx)
	emit(h)
	if !h.Healthy {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `alwe — universal agent observation layer

Commands:
  (default)  Start MCP server on stdio
  search     Search agent sessions by content (cass + local catalog)
  index      Refresh the local FTS5 catalog over session transcripts
  timeline   Show recent agent activity (requires cass)
  export     Export a session to markdown (requires cass)
  context    Find sessions that touched a file
  health     Report backend availability, catalog coverage, and build id

Usage:
  alwe                                # MCP server mode
  alwe search "auth bug"              # search all agents
  alwe search --connector codex "fix" # search codex only
  alwe index                          # index ~/.claude/projects and ~/.codex/sessions
  alwe index /path/to/transcripts     # index specific roots
  alwe timeline --since 2h
  alwe export ~/.claude/projects/.../session.jsonl
  alwe context src/main.go
  alwe health

Environment:
  ALWE_INDEX_DB   Local catalog path (default: <user cache>/alwe/sessions.db)
`)
}
