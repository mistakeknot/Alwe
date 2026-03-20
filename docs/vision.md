# Alwe: Vision and Philosophy

## What Alwe Is

Alwe is a universal agent observation layer. It watches any AI coding agent's sessions via CASS and exposes structured data as MCP tools and CLI commands. It answers the question: what did the agents do?

The name comes from Zakalwe in Iain M. Banks' *Use of Weapons*. [Zaka](https://github.com/mistakeknot/Zaka) is the acting half; Alwe is the observing half. Together they form Zakalwe.

## Core Conviction

**Observation is independent of steering.** You might want to observe agents you didn't spawn. CASS indexes sessions from 15+ agent providers regardless of who started them — Claude Code, Codex, Gemini, AMP, Aider, Cursor, Copilot, and more. Alwe makes that data accessible without requiring Zaka.

**CASS is the universal session backend.** Rather than building per-agent JSONL parsers, Alwe delegates parsing to CASS, which already has connectors for every major coding agent. This means Alwe automatically supports new agents when CASS adds connectors — no code changes needed.

**Dual-mode is the right interface.** Running `alwe` with no args starts an MCP server for programmatic access by orchestrators. Running `alwe search` gives humans the same data through a CLI. One binary, two interfaces, same backend.

## Architecture

```
Claude Code / Codex / Gemini / AMP / Aider / ...
  │
  ▼ (session JSONL files)
CASS (indexes 15+ agent providers)
  │
  ▼
Alwe
  ├── MCP server (stdio) ──→ Skaffen / other orchestrators
  └── CLI ──→ humans
```

### Two observation modes

1. **Real-time tail** — `TailSession()` polls a JSONL file at 100ms intervals, parses events (text, tool_use, tool_result, done), sends to channel. For live observation of running agents.

2. **Historical query** — `SearchSessions()`, `ContextForFile()`, `ExportSession()`, `Timeline()` wrap CASS CLI calls. For cross-session analysis, file provenance, and activity timelines.

### Five MCP tools

| Tool | Use case |
|------|----------|
| `search_sessions` | "What sessions discussed auth?" |
| `context_for_file` | "Who touched src/main.go recently?" |
| `export_session` | "Show me that session as markdown" |
| `timeline` | "What happened in the last 2 hours?" |
| `health` | "Is CASS available?" |

## Design Bets

1. **CASS outlives custom parsers.** CASS is a Rust binary that indexes sessions at sub-60ms latency with hybrid search. Building our own would be slower, buggier, and perpetually incomplete. The "adopt, don't build" principle from PHILOSOPHY.md applies directly.

2. **MCP is the right exposure layer.** MCP gives orchestrators structured, typed access to observation data without stdout parsing. Any MCP client (Skaffen, Claude Code, custom scripts) can connect.

3. **Observation compounds.** The more agents run, the more session data accumulates, the more valuable Alwe's queries become. Cross-session search reveals patterns invisible to single-session agents — duplicate work, recurring failures, successful approaches worth repeating.

4. **File provenance is the killer query.** "Which sessions touched this file?" answers the question every developer asks during debugging and code review. `context_for_file` makes this a one-liner.

## Non-Goals

- **Not an agent runtime.** Alwe doesn't spawn or steer agents — that's Zaka's job.
- **Not a dashboard.** Alwe exposes data; rendering is the UI layer's job (Autarch, or whatever frontend connects to the MCP server).
- **Not a replacement for CASS.** Alwe is a thin wrapper, not a reimplementation. If CASS adds features, Alwe exposes them.

## Relationship to Zaka

Zaka steers agents. Alwe observes them. They share no code and have no dependency on each other. An orchestrator composes them:

```
Skaffen OODARC loop:
  Act     → Zaka spawns agent, sends prompt
  Observe → Alwe reads session output via CASS
  Reflect → Skaffen evaluates results
```

## Relationship to Skaffen

Skaffen connects to Alwe's MCP server (or uses the observer package directly) to watch what its spawned agents are doing. The `internal/mcpsidecar/` in Skaffen was the original prototype; Alwe is the extracted, standalone implementation.

## Relationship to CASS

CASS is the session intelligence backend — a Rust binary at `~/.local/bin/cass` that indexes 10K+ sessions from 15+ agent providers. Alwe wraps CASS CLI calls as Go functions and exposes them as MCP tools. Alwe does not duplicate CASS's indexing, search, or parsing logic.
