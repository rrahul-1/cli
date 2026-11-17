# Codebase map

| Path | Purpose |
| --- | --- |
| `cmd/entire/` | Main entry point and external-command dispatch |
| `cmd/entire/cli/` | Cobra commands, group roots, CLI helpers (`<noun>_group.go`, `<noun>_<verb>.go`) |
| `cmd/entire/cli/agent/` | Built-in agent integrations and external-agent protocol |
| `cmd/entire/cli/strategy/` | Manual-commit strategy and lifecycle/git hooks |
| `cmd/entire/cli/checkpoint/` | Persistent checkpoint storage (written at commit) |
| `cmd/entire/cli/session/` | Session state shared across worktrees |
| `cmd/git-remote-entire/` | Git remote helper binary |
| `e2e/` | Real-agent tests and deterministic Vogon canary (`e2e/vogon`) |
| `internal/coreapi/` | Core API definitions |
| `internal/entireclient/` | API clients, authentication, user directories/token store |
| `internal/remotehelper/` | Remote-helper protocol implementation (git transport) |
| `internal/transcript/` | Agent transcript parsers (codex, opencode, pijsonl) |
| `internal/procsignal/`, `internal/testdirs/` | Small shared helpers |
| `api/checkpoint/` | Checkpoint data/API schemas |
| `tools/complexity/` | Duplication/complexity measurement and the CI complexity gate |
| `docs/` | Development and architecture references (see `AGENTS.md` index table) |
| `mise-tasks/` | Scripts backing `mise.toml` tasks |
| `perf/`, `redact/`, `transcript/` | Root-level test/support packages |
| `scripts/` | Release/install helper scripts |
