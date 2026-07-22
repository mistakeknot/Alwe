---
name: alwe
description: Use when you need to search or inspect past AI coding agent sessions — find how a previous agent solved a problem, which sessions touched a file, or recent cross-agent activity. Wraps cass as MCP tools (preferred when registered) and a CLI. For live tmux output use intermux; for steering agents use zaka.
---

# Alwe — observe agent session history

Alwe watches any CLI AI agent's sessions via CASS (session JSONL index) and exposes the data as MCP tools and CLI commands. The complement to Zaka, which steers.

## MCP tools (preferred interface)

When Alwe is registered as an MCP server, use its tools instead of shelling out:

| Tool | Use |
|------|-----|
| `search_sessions` | Search agent sessions by content, filter by connector (claude_code, codex, kimi, ...) |
| `context_for_file` | Find sessions that touched a specific file |
| `export_session` | Export a session to markdown |
| `timeline` | Recent activity across all agents |
| `health` | CASS availability check |

## CLI fallback

```bash
alwe search "auth bug"                  # search all agents' sessions
alwe search --connector codex "fix"     # filter by agent
alwe timeline --since 2h                # recent activity
alwe context src/main.go                # sessions that touched a file
alwe export <session.jsonl>             # export to markdown
alwe health                             # is cass working?
```

## When to use what

- **Alwe**: historical — "has an agent seen this problem/file before?" Check *before* investigating unfamiliar code; past agents may have solved it.
- **intermux MCP**: live — what is a running agent printing right now.
- **raw `cass` CLI**: only when Alwe is unavailable; Alwe is the structured front-end.

## Operational notes

- Requires `cass` at runtime (`~/.local/bin/cass`). If results seem stale, the cass index may need a rebuild (`cass index --full`).
- Repo: `/Users/sma/projects/Sylveste/os/Alwe` (`go build ./cmd/alwe`, `go test ./...`).
