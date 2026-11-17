# Entire CLI (rrahul-1/cli)

Go command-line application (`github.com/entireio/cli`): the `entire` CLI built with Cobra and Charmbracelet (bubbletea/huh/lipgloss). Tracks agent sessions and checkpoints in git repositories via git hooks.

## Stack

- Go 1.27.3 (pinned in `go.mod` and `mise.toml`)
- Cobra for commands; Charmbracelet for interactive TUIs
- No external runtime services (no database, no Redis); control-plane API is remote and only needed for authenticated commands
- Task runner: mise (`mise.toml`) — not installed in this sandbox; run the underlying commands directly

## Sandbox toolchain setup

Toolchains are pre-installed outside the repo (snapshot `cf0051jt869hvrlf7wo8:default`, captured 2026-11-17T19:54:19.949Z):

```
export PATH=$HOME/toolchains/git/bin:$HOME/toolchains/node22/bin:$HOME/toolchains/go/bin:$PATH
```

- `$HOME/toolchains/go` — Go 1.27.3 (system git/node are too old: git 2.47 cannot migrate reflogs to reftable, node 20 lacks `--experimental-strip-types`; tests need newer)
- `$HOME/toolchains/git` — git 2.52.0 (required by reftable tests)
- `$HOME/toolchains/node22` — Node 22.20.0 (required by opencode plugin tests)
- golangci-lint 2.15.0 prebuilt binary at `/tmp/golangci-lint-2.15.0-linux-amd64/golangci-lint` (re-install from GitHub releases if /tmp was wiped)

## Commands

| Task | Command |
| --- | --- |
| Build CLI | `go build -o /tmp/entire ./cmd/entire/` |
| All unit tests | `go test ./...` |
| Integration tests | `go test -tags=integration ./cmd/entire/cli/integration_test/... ./cmd/entire/cli/auth/...` |
| Format | `gofmt -s -w .` |
| Lint | `golangci-lint run` (config: `.golangci.yaml`) |
| Full CI check | `mise run check` (fmt + lint + unit + integration + e2e canary) |
| E2E canary (deterministic, free) | `go test ./e2e/vogon` |

Never run real-agent E2E tests — they make paid API calls. Vogon canary is the safe exception.

## Local verification

Quick smoke test of a build:

```
/tmp/entire version && /tmp/entire --help
```

Primary user flow (safe, no auth): in a scratch repo, `entire enable`, `entire status`, `entire doctor`. Verified working in this sandbox.

## Codebase map

See [codebase-map.md](codebase-map.md).

## Repo policy

- Default branch: `main`
- `AGENTS.md` is the canonical agent-instructions file (documents project layout, testing rules, and filesystem/git safety); keep it authoritative and follow its references before changing an area
