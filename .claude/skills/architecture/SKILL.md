---
name: architecture
description: Use when modifying Noctra's own source code to understand non-obvious invariants, package boundaries, testability conventions, and the patterns an agent must respect to avoid breaking subtle runtime behaviour.
---

# Architecture & Contributing

Use this playbook before changing Noctra's core code. The invariants below are easy to violate in a plausible-looking patch; each has caused a real bug or regression.

## Package map (navigation aid)

| Package | Purpose |
|---------|---------|
| `cmd/noctra` | Entry point, subcommand dispatch, startup banner |
| `internal/config` | `.env` parser, validated `Config`, `DefaultConfigDir` |
| `internal/linear` | Linear GraphQL client (trigger queries, state/label mutations, comments, Telegram read queries) |
| `internal/repo` | Repo resolution (`ResolveDirect` / `Resolve`), clone-on-demand, worktree create/resume/cleanup, `BranchName` |
| `internal/agent` | Pluggable coding-agent backends, shared prompt builders, log_offset helpers, `BlockedLine`, `ExtractSummary` |
| `internal/review` | Optional Gemini second-model review gate |
| `internal/notify` | Optional Telegram notifier (fire-and-forget) |
| `internal/telegram` | Inbound Telegram listener (long-polling, command dispatch) |
| `internal/github` | Thin `gh` CLI wrapper (`ListNoctraPRs`, `GetPR`, `CheckLogs`) |
| `internal/state` | File-backed PR cursor store (`~/.noctra-state.json`) |
| `internal/watch` | Side-effect-free PR classifier (diffs feedback + CI against cursor) |
| `internal/pipeline` | Poll loop, worker pool, per-ticket lifecycle (`process.go`), PR-watch + iterate (`iterate.go`), Telegram commands (`commands.go`) |
| `internal/setup` | Interactive setup wizard |
| `internal/cleanup` | Cleanup subcommand (branches, worktrees, old logs) |
| `internal/service` | `install-service` subcommand (systemd unit rendering) |
| `internal/doctor` | Preflight checks (CLIs, auth, repo routing) |
| `internal/selfupdate` | In-place binary upgrade via GoReleaser archives |

## Invariant 1: the `log_offset` pattern

Agent logs append across attempts. `agent.OffsetBefore` records the file size *before* the agent runs; `agent.ReadAfter` reads only the new tail. `BlockedLine` and `HasRateLimit` operate on that tail.

**Rule:** never scan the full log file to detect failures. That re-detects failures from previous attempts and causes false positives (e.g. a ticket that was rate-limited on attempt 1 would be falsely detected as rate-limited on attempt 2 even when the agent succeeded).

**Where it lives:** `internal/agent/exec.go` (`OffsetBefore`, `ReadAfter`), consumed in `internal/pipeline/process.go` and `internal/pipeline/iterate.go`.

## Invariant 2: the `noctra/` branch-prefix guardrail

The auto-iterate watcher identifies its own PRs by the `noctra/<id>` branch prefix. `repo.BranchName` generates it; `github.ListNoctraPRs` filters on it; `watch` and `iterate` depend on it.

**Rules:**
- Keep `CreateWorktree` and `ResumeWorktree` on the same prefix or the watcher silently never finds its own PRs.
- Never create a `noctra/`-prefixed branch for manual work or agent-assisted PRs in this repo. The watcher will claim it and push conflicting follow-up commits.
- Use `fix/`, `feat/`, `docs/`, `chore/`, or `eng-<n>-<slug>` prefixes for non-Noctra branches.

## Invariant 3: backend-agnostic `internal/agent` split

Almost everything in `internal/agent` is shared across all backends (Claude, Codex, Copilot): prompt builders (`BuildPrompt`, `BuildFixPrompt`), `BlockedLine`, log_offset helpers, and `ExtractSummary`.

**Only two things differ per backend:**
1. **Invocation args** — `claudeArgs` / `codexArgs` / `copilotArgs`. All go through the shared `runCLI`.
2. **Rate-limit parsing** — `HasRateLimit` with per-backend regexes (`claudeRateLimitRe` / `codexRateLimitRe` / `copilotRateLimitRe`).

**Rule:** when adding shared agent logic, put it in the common code (`exec.go`, `prompt.go`). Only add to a backend-specific file (`claude.go`, `codex.go`, `copilot.go`) if the behaviour genuinely differs per backend. If adding a new backend, implement the `Backend` interface and add the name to `agent.New`.

## Invariant 4: cursor semantics

The PR-watch system tracks what feedback has been acted on via cursors in `internal/state`:

- **Comment/review cursors** are **timestamps** (naturally ordered). Conversation comments and inline review-thread comments share the comment cursor.
- **CI cursor** is keyed by **head commit SHA** (`LastCISHA`) — acted on once per commit. A fix push changes the SHA, making a fresh failure on the new commit eligible again.

**Rule:** don't change cursor types or comparison logic without understanding that timestamps work for comments (monotonically increasing) but not for CI (where the same SHA can have multiple check runs; what matters is "have we already acted on this SHA?").

## Invariant 5: pure functions for testability

Noctra follows a convention of extracting logic into **pure, side-effect-free functions** that are unit-tested independently from the I/O that calls them:

| Pure function | Package | What it does |
|--------------|---------|-------------|
| `claudeArgs` / `codexArgs` / `copilotArgs` | `internal/agent` | Build CLI argument slices |
| `unitFile(exePath, pathEnv)` | `internal/service` | Render the systemd unit file |
| `completionScript(shell)` | `cmd/noctra` | Generate shell-completion scripts |
| `watch.diff` / `watch.actionable` | `internal/watch` | Classify PR changes, apply trusted-reviewer rules |
| `selfupdate.IsNewer` / `assetName` | `internal/selfupdate` | Version comparison, archive name selection |

**Rule:** when adding new logic, prefer extracting the decision/formatting into a pure function with table-driven tests, then call it from the I/O layer. This keeps tests fast and deterministic.

## Invariant 6: golangci-lint v2 syntax

The repo uses `.golangci.yml` with `version: "2"` syntax. This means:
- Linters are configured under `linters.default` and `linters.settings`, not the v1 `linters.enable` list.
- Formatters are under `formatters.enable` (e.g. `gofmt`), not mixed with linters.

**Rule:** if adding linter config, use v2 syntax. v1 keys will cause a parse error.

## Quality gates

Before submitting changes:

```bash
go test ./...       # all tests must pass
go vet ./...        # must be clean
```

If `golangci-lint` is available:

```bash
golangci-lint run
```

These are the same checks CI runs. A PR that fails either will not be merged.
