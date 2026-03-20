# Alwe

Universal agent observation layer. Watches any AI coding agent's sessions via CASS, exposes structured data as MCP tools and CLI.

The other half of Zakalwe — [Zaka](https://github.com/mistakeknot/Zaka) steers, Alwe observes. Named from Iain M. Banks' *Use of Weapons*.

## Install

```bash
go install github.com/mistakeknot/Alwe/cmd/alwe@latest
```

Requires [cass](https://github.com/dicklesworthstone/cass) at runtime.

## Usage

### MCP Server (default)

```bash
# Start as MCP server on stdio — use from Skaffen, Claude Code, or any MCP client
alwe
```

Exposes 5 tools: `search_sessions`, `context_for_file`, `export_session`, `timeline`, `health`.

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

# Check CASS health
alwe health
```

## Supported Agents (via CASS)

Claude Code, Codex, Gemini, AMP, Aider, Cline, Cursor, Copilot, ChatGPT, and more — any agent with a CASS connector.

## Part of Demarch

Alwe is an L2 OS component of [Demarch](https://github.com/mistakeknot/Demarch), the autonomous software development agency platform.

## License

MIT
