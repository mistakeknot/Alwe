# Alwe

Universal agent observation layer. Watches any AI coding agent's sessions, exposes structured data as MCP tools and CLI.

Search works with or without [cass](https://github.com/dicklesworthstone/cass): cass supplies better ranking and a larger corpus, while a local SQLite FTS5 catalog supplies availability. When cass is missing, stale, or lock-busy, searches serve local results and say so.

The other half of Zakalwe — [Zaka](https://github.com/mistakeknot/Zaka) steers, Alwe observes. Named from Iain M. Banks' *Use of Weapons*.

## Install

```bash
go install github.com/mistakeknot/Alwe/cmd/alwe@latest
```

cass is optional. Without it, `alwe search` and `alwe context` run off the local
catalog; `alwe timeline` and `alwe export` require cass and say so plainly.

## Usage

### MCP Server (default)

```bash
# Start as MCP server on stdio — use from Skaffen, Claude Code, or any MCP client
alwe
```

Exposes 5 tools: `search_sessions`, `context_for_file`, `export_session`, `timeline`, `health`.

`search_sessions` and `context_for_file` merge cass and local results, deduped
on `(file_path, line_number)` and ranked by reciprocal rank fusion — the two
backends' raw scores are not comparable, so fusing positions keeps either
backend's top hits from being buried. Responses carry `degraded` and `notice`
fields when a backend is missing.

`health` reports per-backend availability, local catalog coverage, and the
running binary's build id, so a stale MCP server is detectable.

### CLI

```bash
# Search across all agent sessions
alwe search "auth bug"

# Filter by agent
alwe search --connector codex "deployment fix"

# Recent activity timeline
alwe timeline --since 2h

# Export a session to markdown
alwe export ~/.claude/projects/.../session.jsonl

# Find sessions that touched a file
alwe context src/main.go

# Refresh the local catalog (incremental: only changed transcripts are read)
alwe index

# Backend status, catalog coverage, and build id
alwe health
```

### Local catalog

`alwe index` maintains a SQLite FTS5 catalog over session transcripts. The unit
of work is one transcript file, keyed on `(mtime, size)`: unchanged files are
skipped, and a changed file is re-indexed alone. There is no global fingerprint
and no full-rebuild concept, so a single drifted transcript costs milliseconds
rather than a whole-corpus rebuild.

| Environment | Purpose |
|---|---|
| `ALWE_INDEX_DB` | Catalog path (default: `<user cache>/alwe/sessions.db`) |
| `ALWE_CASS_STALE_THRESHOLD` | Seconds of lexical staleness Alwe asks cass to judge itself against (default 1800, matching cass's own `status` threshold; cass `health` defaults to a stricter 300) |

## Supported Agents (via CASS)

Claude Code, Codex, Gemini, AMP, Aider, Cline, Cursor, Copilot, ChatGPT, and more — any agent with a CASS connector.

## Part of Demarch

Alwe is an L2 OS component of [Demarch](https://github.com/mistakeknot/Demarch), the autonomous software development agency platform.

## License

MIT
