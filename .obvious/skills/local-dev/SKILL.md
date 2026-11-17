---
name: local-dev
---

# local-dev — Entire CLI sandbox setup

## Environment

- Debian 13 sandbox, x86_64. System `git` (2.47) and `node` (20) are **too old** for the test suite; prebuilt toolchains live in `$HOME/toolchains` (baked into snapshot `cf0051jt869hvrlf7wo8:default`, captured 2026-11-17T19:54:19.949Z).
- Required PATH export before any test/lint run:
  `export PATH=$HOME/toolchains/git/bin:$HOME/toolchains/node22/bin:$HOME/toolchains/go/bin:$PATH`
- `mise` is not installed; run the underlying commands directly instead of `mise run <task>`.

## Steps to reach a healthy dev stack

1. Export the PATH above.
2. Build: `go build -o /tmp/entire ./cmd/entire/` (~35 s first time; module cache is warm in the snapshot).
3. Unit tests: `go test ./...` — 96 packages, ~45–100 s. All must pass. If reftable tests fail with "migrating reflogs is not supported yet", the wrong `git` is on PATH. If opencode plugin tests fail with `node: bad option: --experimental-strip-types`, the wrong `node` is on PATH.
4. Lint: `golangci-lint run` (binary at `/tmp/golangci-lint-2.15.0-linux-amd64/`; reinstall from GitHub releases v2.15.0 if missing — needs `go` on PATH).
5. Format check: `gofmt -s -l .` (expect empty output).

## Primary user flow evidence

In a scratch git repo:

```
entire enable    # installs hooks, writes .entire/settings.json (telemetry prompt may fail without a TTY — non-blocking)
entire status    # shows "Enabled · branch main"
entire doctor    # all checks OK
```

Plain commits without an agent session produce no checkpoints — that is expected product behavior, not a failure.

## Constraints from AGENTS.md

- Never run real-agent E2E tests (paid API calls). `e2e/vogon` canary is deterministic and free.
- Tests must not touch the developer's real config/cache/keyring; use isolated temp dirs.
- Authenticated commands (trail, etc.) require Entire account credentials — not configured in this sandbox; treat as `TODO(confirm)` if needed.

## Verified 2026-11-17

- `go test ./...`: 96 packages ok, 0 failures.
- `golangci-lint run`: 0 issues.
- `gofmt -s -l .`: clean.
- Build + `entire version` / `--help` / enable / status / doctor: working.
