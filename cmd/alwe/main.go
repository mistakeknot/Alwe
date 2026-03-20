// Command alwe is a universal agent observation tool.
//
// As an MCP server (default): exposes CASS session data as MCP tools.
// As a CLI: search, export, and stream agent sessions.
//
// Usage:
//
//	alwe                          # start MCP server on stdio
//	alwe search "auth bug"        # search sessions
//	alwe search --connector codex "fix"  # search codex sessions
//	alwe timeline --since 2h      # recent activity
//	alwe export <session.jsonl>   # export session to markdown
//	alwe context <file-path>      # sessions that touched a file
//	alwe health                   # check CASS status
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mistakeknot/Alwe/internal/mcpserver"
	"github.com/mistakeknot/Alwe/internal/observer"
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
		if err := s.Run(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("alwe run: %v", err)
		}
		return
	}

	obs, err := observer.New()
	if err != nil {
		log.Fatalf("cass not available: %v", err)
	}

	switch os.Args[1] {
	case "search":
		cmdSearch(ctx, obs, os.Args[2:])
	case "timeline":
		cmdTimeline(ctx, obs, os.Args[2:])
	case "export":
		cmdExport(ctx, obs, os.Args[2:])
	case "context":
		cmdContext(ctx, obs, os.Args[2:])
	case "health":
		cmdHealth(ctx, obs)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func cmdSearch(ctx context.Context, obs *observer.CassObserver, args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	connector := fs.String("connector", "", "Filter by agent connector")
	limit := fs.Int("limit", 10, "Maximum results")
	fs.Parse(args)

	query := fs.Arg(0)
	if query == "" {
		log.Fatal("usage: alwe search [--connector X] <query>")
	}

	results, err := obs.SearchSessions(ctx, query, *connector, *limit)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(results)
}

func cmdTimeline(ctx context.Context, obs *observer.CassObserver, args []string) {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	since := fs.String("since", "1h", "Time range")
	fs.Parse(args)

	tl, err := obs.Timeline(ctx, *since)
	if err != nil {
		log.Fatalf("timeline: %v", err)
	}
	fmt.Print(tl)
}

func cmdExport(ctx context.Context, obs *observer.CassObserver, args []string) {
	if len(args) < 1 {
		log.Fatal("usage: alwe export <session-path>")
	}
	md, err := obs.ExportSession(ctx, args[0])
	if err != nil {
		log.Fatalf("export: %v", err)
	}
	fmt.Print(md)
}

func cmdContext(ctx context.Context, obs *observer.CassObserver, args []string) {
	fs := flag.NewFlagSet("context", flag.ExitOnError)
	limit := fs.Int("limit", 5, "Maximum results")
	fs.Parse(args)

	filePath := fs.Arg(0)
	if filePath == "" {
		log.Fatal("usage: alwe context <file-path>")
	}

	results, err := obs.ContextForFile(ctx, filePath, *limit)
	if err != nil {
		log.Fatalf("context: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(results)
}

func cmdHealth(ctx context.Context, obs *observer.CassObserver) {
	if obs.IsAvailable(ctx) {
		fmt.Println(`{"healthy": true}`)
	} else {
		fmt.Println(`{"healthy": false}`)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `alwe — universal agent observation layer

Commands:
  (default)  Start MCP server on stdio (CASS-backed tools)
  search     Search agent sessions by content
  timeline   Show recent agent activity
  export     Export a session to markdown
  context    Find sessions that touched a file
  health     Check CASS availability

Usage:
  alwe                                # MCP server mode
  alwe search "auth bug"              # search all agents
  alwe search --connector codex "fix" # search codex only
  alwe timeline --since 2h
  alwe export ~/.claude/projects/.../session.jsonl
  alwe context src/main.go
`)
}
