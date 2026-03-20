# Alwe

Universal agent observation layer. Watches any AI coding agent's sessions via CASS, exposes structured data as MCP tools and CLI.

The other half of Zakalwe — Zaka steers, Alwe observes.

## Quick Reference

- **Build:** `go build ./cmd/alwe`
- **Test:** `go test ./... -count=1`
- **MCP server:** `./alwe` (default, stdio transport)
- **CLI search:** `./alwe search "query"`
- **CLI timeline:** `./alwe timeline --since 2h`

## Structure

```
cmd/alwe/              CLI + MCP server entry point
internal/
  observer/            CASS observer (real-time tail + query)
  mcpserver/           MCP server exposing 5 CASS-backed tools
```

## MCP Tools

- `search_sessions` — search agent sessions by content, filter by connector
- `context_for_file` — find sessions that touched a file
- `export_session` — export session to markdown
- `timeline` — recent activity across all agents
- `health` — CASS availability check

## Git

Alwe has its own git repo at `os/Alwe/`. Commit from here, not the monorepo root.

## Beads

Uses the Demarch monorepo beads tracker at `/home/mk/projects/Demarch/.beads/` (prefix `Demarch-`).
