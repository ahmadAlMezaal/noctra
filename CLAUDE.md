# Nightshift

Autonomous Linear-to-PR agent in Go. Polls Linear for tickets in a trigger state, dispatches Claude Code to implement them, creates PRs, and moves tickets to review.

## Architecture

```
poll loop → linear.FetchTriggerIssues → pipeline.process (bounded goroutine)
  → repo.Resolve → repo.CreateWorktree → agent.Run → check output
  → (optional) review.Gate → commit/push → gh pr create → linear update
```

Worktrees live at `~/.nightshift-worktrees/<IDENTIFIER>` so multiple tickets run concurrently without sharing a working directory.

## Multi-repo

The target repo is chosen **per-ticket** from the ticket's Linear **project**, not from a single global path. `repos.json` (gitignored) maps a project name → `{ url, main_branch }`. `repo.Resolve` looks the project up, clones the repo on demand into `~/.nightshift-repos/<slug>` (lock-guarded against concurrent clone races via `mkdir(2)`), and returns the local path + main branch.

If a ticket's project has no registry entry, Nightshift falls back to `REPO_PATH` from `.env` if set, otherwise it skips the ticket with a Linear comment.

`./nightshift setup` is the interactive wizard that generates `.env` and `repos.json`. `repos.example.json` is the checked-in template.

## Package map

| Package | Purpose |
|---------|---------|
| `cmd/nightshift` | Entry point + subcommand dispatch (`run` / `setup` / `cleanup` / `version`) |
| `internal/config` | `.env` parser, `repos.json` loader, validated `Config` |
| `internal/linear` | Linear GraphQL client: `ResolveStateIDs`, `FetchTriggerIssues`, `SetState`, `Comment` |
| `internal/repo` | Project → repo slug + registry; clone-on-demand; worktree create/cleanup |
| `internal/agent` | Claude Code runner (`exec`) with timeout; prompt builder; log_offset parsing |
| `internal/review` | Optional Gemini second-model review gate |
| `internal/notify` | Optional Telegram notifier (fire-and-forget) |
| `internal/pipeline` | Poll loop, bounded worker pool, full per-ticket lifecycle |
| `internal/setup` | Interactive setup wizard (`./nightshift setup`) |
| `internal/cleanup` | Cleanup subcommand: branches, worktrees, old logs |

## Log file structure

Logs at `.agent-logs/<IDENTIFIER>.log` **append across attempts**:

```
--- Attempt 2026-01-01T00:00:00Z ---
DEBUG: pwd = /path
<claude output>
--- Attempt 2026-01-01T01:00:00Z ---
DEBUG: pwd = /path
<claude output>
```

### IMPORTANT: log_offset pattern

`agent.OffsetBefore` records the file size *before* Claude runs; `agent.ReadAfter` reads only the new tail. `agent.BlockedLine` and `agent.HasRateLimit` operate on that tail so failures from previous attempts don't get re-detected. **Do not replace this with a scan over the full file** — that re-detects failures from previous attempts and causes false positives.

## Running tests

```bash
go test ./...
```

## Building

```bash
# Local
go build -o nightshift ./cmd/nightshift

# Raspberry Pi (arm64 — Pi 4 / 5 with 64-bit OS)
GOOS=linux GOARCH=arm64 go build -o nightshift ./cmd/nightshift

# Raspberry Pi (32-bit, armv7)
GOOS=linux GOARCH=arm GOARM=7 go build -o nightshift ./cmd/nightshift
```

`go vet ./...` should be clean.
