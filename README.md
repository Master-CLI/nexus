# Nexus

Multi-agent terminal orchestration hub. Spawn, monitor, and coordinate AI coding agents (Claude, Codex, Gemini) through a unified MCP interface with profile-based automation.

<!-- TODO: replace with actual recording
## Demo

### Create an agent and get a response
![Create agent demo](docs/demo-hero.gif)

### Multi-agent broadcast
![Multi-agent broadcast](docs/demo-broadcast.gif)

### Profile-based automation
![Profile automation](docs/demo-profile.gif)
-->

## Features

- **Multi-agent sessions** — spawn Claude, Codex, Gemini, or plain shell terminals in managed PTY sessions
- **MCP server** — 12 tools for agent orchestration (create, list, read, ask, broadcast, send, profiles)
- **Read Guard protocol** — prevents blind input; agents must observe peer state before writing
- **Profile system** — declarative YAML templates with cron scheduling and loop mode
- **Intent routing** — optional local Ollama/Gemma4 integration for fast request classification
- **Desktop GUI** — Wails v3 + React frontend with tabbed terminal layout (xterm.js)
- **Headless mode** — MCP-only server for CI/automation (no GUI required)
- **Cross-platform** — Windows (ConPTY) and Unix (creack/pty)

## Quick Start

### Headless mode (MCP server only)

```bash
go build -o nexus .
./nexus --headless
```

The MCP server starts on port 9400 (configurable in `nexus-config.yaml`).

### Desktop mode (requires Wails v3)

```bash
# Install Wails v3 CLI first: https://v3alpha.wails.io/getting-started/installation/
cd frontend && npm install && cd ..
wails3 build
./build/bin/nexus
```

### Using Make

```bash
make build-headless   # MCP-only binary
make build            # Full desktop app (needs Wails v3)
make test             # Run tests
make run-headless     # Build and run headless
```

## Configuration

Copy and edit the config file:

```yaml
# nexus-config.yaml
mcp_base_port: 9400
max_sessions: 8

pty:
  # shell: auto-detected (powershell on Windows, $SHELL on Unix)
  cols: 120
  rows: 30

# Optional: external MCP servers available to spawned agents
external_mcp_servers:
  my-rag: "http://localhost:9090/mcp"
  my-browser: "http://localhost:9500/mcp"

# Optional: bearer token authentication
mcp:
  auth_enabled: true
  # bearer_token: auto-generated if empty
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `create_session` | Spawn a new agent terminal (shell/claude/codex/gemini) |
| `destroy_session` | Close an existing terminal session |
| `list_peers` | List all active terminal sessions |
| `read_peer` | Read a peer's terminal state (status, output, prompt detection) |
| `send_to_peer` | Fire-and-forget stdin injection (requires prior `read_peer`) |
| `ask_peer` | Send command to peer and wait for response |
| `broadcast` | Fan-out message to all peers, collect responses |
| `list_profiles` | List available agent profiles |
| `run_profile` | Launch agent from a profile template |
| `stop_profile` | Stop a running profile session |
| `classify_intent` | Classify user message intent (requires local Ollama) |
| `get_time` | Get current system time |

## Profiles

Profiles are declarative YAML templates in `nexus-profiles/`. Example:

```yaml
name: research-assistant
description: "Research assistant"
provider: claude
model: sonnet

init_message: |
  You are a Research Assistant. Investigate topics and summarize findings.

schedule:
  loop: true  # restart after session ends

limits:
  max_runtime_sec: 600
  max_sessions: 1
```

## Architecture

```
nexus
├── main.go                  # Entry point (GUI + headless modes)
├── service.go               # Wails-bound RPC service
├── nexus-config.yaml        # Configuration
├── nexus-profiles/          # Agent profile templates
├── frontend/                # React + xterm.js UI
│   └── src/
└── internal/
    ├── config/              # YAML config loader
    ├── mcp/                 # MCP server, tools, resources, intent routing
    ├── session/             # PTY management, agent launcher, output capture
    └── embed/               # Embedded frontend assets
```

## Requirements

- Go 1.22+
- Node.js 18+ (for frontend build)
- [Wails v3](https://v3alpha.wails.io/) (for desktop mode only)
- One or more AI CLI agents on PATH: `claude`, `codex`, `gemini`
- Optional: [Ollama](https://ollama.ai/) with gemma4 model (for intent routing)

## License

Apache 2.0 — see [LICENSE](LICENSE).
