# Nightshift

Autonomous Linear-to-PR agent in Go. Polls Linear for tickets in a trigger state or with a trigger label, dispatches Claude Code to implement them, creates PRs, and moves tickets to review. Optionally (`AUTO_ITERATE_PRS=true`) it also watches the PRs it opened and pushes follow-up commits in response to review feedback and CI failures.

## Architecture

```
poll loop → linear.FetchTriggerIssues / FetchLabeledIssues → pipeline.process (bounded goroutine)
  → repo.Resolve → repo.CreateWorktree → agent.Run → check output
  → (optional) review.Gate → commit/push → gh pr create → linear update
```

### Trigger modes

* `TRIGGER_MODE=state` (default): polls for tickets in the `TRIGGER_STATE` column (e.g. "Next"). Current behaviour, unchanged.
* `TRIGGER_MODE=label`: polls for tickets carrying the `TRIGGER_LABEL` label (e.g. "nightshift"). Tagging a ticket with the label picks it up regardless of its column. The label is **removed** after dispatch so the ticket isn't re-polled; the In-Review state transition still applies. In label mode the trigger-state ID is not resolved (the in-review state is still required).

Worktrees live at `~/.nightshift-worktrees/<IDENTIFIER>` so multiple tickets run concurrently without sharing a working directory.

## Auto-iterate on PR feedback (optional)

When `AUTO_ITERATE_PRS=true`, a **second** poll loop runs alongside the Linear one:

```
PR poll loop → github.ListNightshiftPRs → github.GetPR (comments+reviews+statusCheckRollup)
  → watch.Scan (diff vs state cursor: new feedback OR failing CI on a new head SHA)
  → pipeline.iteratePR (bounded, shares the worker pool + active-set)
  → repo.ResumeWorktree → [github.CheckLogs for CI] → agent.BuildFixPrompt → agent.Run
  → commit/push (same branch) → state.Update (advance comment/review/CI cursor + bump iteration count)
```

- **Opt-in**, off by default. Both loops run on the same `WaitGroup` so shutdown drains in-flight iterations.
- Only acts on PRs **Nightshift authored** — identified purely by the `nightshift/<id>` branch prefix (`repo.BranchName`). `CreateWorktree` and `ResumeWorktree` both use it; `watch`/`iterate` filter on it. **Keep creation and watching on the same prefix** or the watcher silently never finds its own PRs.
- Feedback captured: conversation comments, review summaries (`CHANGES_REQUESTED` / non-empty `COMMENTED`), and inline review-thread comments (fetched separately via `gh api`, non-fatal on failure). `APPROVED`/`DISMISSED` and empty `COMMENTED` advance the cursor without acting.
- **CI failures** are a second trigger feeding the same `iteratePR`: when every check on the head commit (`statusCheckRollup`) has completed and ≥1 failed, `watch.diff` sets `PRChanges.CIFailure`; `iteratePR` fetches failed-step logs (`gh run view --log-failed`, truncated, best-effort) and folds them into the same fix prompt. When both review feedback and CI are pending, one re-engagement handles both.
- Trusted-reviewer rule (`watch.actionable`): humans always actionable; bots only if their login is in `TRUSTED_REVIEWERS`. (CI is not gated by this — it's not a person.)
- Guards: per-PR `MAX_PR_ITERATIONS` cap, **shared** across review + CI re-engagements (timeouts/rate-limits don't count), restart-safe cursor in `state`, `pipeline.active` dedupe so a ticket can't be freshly-dispatched and iterated at once.
- Cursors: comment/review are **timestamps** (naturally ordered; conversation + inline comments share the comment cursor). CI is keyed by **head commit SHA** (`LastCISHA`) — acted on once per commit, since a fix changes the SHA and makes a fresh failure eligible again.
- Nightshift does **not** post back to the GitHub PR thread (no replies, no thread resolution); it pushes a follow-up commit and notifies via Telegram/Linear.

## Multi-repo

The target repo is chosen **per-ticket** from the ticket's Linear **project**, not from a single global path. `repos.json` (gitignored) maps a project name → `{ url, main_branch }`. `repo.Resolve` looks the project up, clones the repo on demand into `~/.nightshift-repos/<slug>` (lock-guarded against concurrent clone races via `mkdir(2)`), and returns the local path + main branch.

If a ticket's project has no registry entry, Nightshift falls back to `REPO_PATH` from `.env` if set, otherwise it skips the ticket with a Linear comment.

`./nightshift setup` is the interactive wizard that generates `.env` and `repos.json`. `repos.example.json` is the checked-in template.

## Package map

| Package | Purpose |
|---------|---------|
| `cmd/nightshift` | Entry point + subcommand dispatch (`run` / `setup` / `cleanup` / `doctor` / `version`); startup banner; `--help` |
| `internal/config` | `.env` parser, `repos.json` loader, validated `Config`, `DefaultConfigDir` (`~/.nightshift/`) |
| `internal/linear` | Linear GraphQL client: `ResolveStateIDs`, `FetchTriggerIssues`, `FetchLabeledIssues`, `ResolveLabelID`, `RemoveLabel`, `SetState`, `Comment` |
| `internal/repo` | Project → repo slug + registry; clone-on-demand; worktree create/cleanup; `BranchName`; `CreateWorktree` (from main) + `ResumeWorktree` (pull existing remote branch) |
| `internal/agent` | Claude Code runner (`exec`) with timeout; implement-prompt builder; `BuildFixPrompt` (review feedback + failing-CI prompt); log_offset parsing |
| `internal/review` | Optional Gemini second-model review gate |
| `internal/notify` | Optional Telegram notifier (fire-and-forget) |
| `internal/github` | Thin `gh` CLI wrapper: `ListNightshiftPRs`, `GetPR` (comments + reviews + inline review comments via REST + `statusCheckRollup`), `CheckLogs` (failed-step logs via `gh run view`) |
| `internal/state` | File-backed PR cursor store (`~/.nightshift-state.json`): per-PR comment/review timestamps + CI head-SHA + iteration count; atomic, mutex-guarded |
| `internal/watch` | Side-effect-free classifier: diffs a PR's feedback + CI status against the cursor, applies trusted-reviewer rules, emits actionable events + `CIFailure` |
| `internal/pipeline` | Poll loop, bounded worker pool, full per-ticket lifecycle (`process.go`); PR-watch loop + per-PR re-engagement (`iterate.go`) |
| `internal/setup` | Interactive setup wizard (`./nightshift setup`) |
| `internal/cleanup` | Cleanup subcommand: branches, worktrees, old logs |
| `internal/doctor` | Preflight checks: CLIs on PATH, `gh auth`, Linear API key, repos.json |

## Config directory

Config defaults to `~/.nightshift/` (`.env`, `repos.json`, `logs/`). This is consistent with the existing `~/.nightshift-*` convention (worktrees, repos, state). The **cwd-checkout override** still works: if the current directory contains `.env`, `repos.json`, `.env.example`, or `go.mod`, Nightshift uses cwd instead — so `go run` during development still works without touching `~/.nightshift/`.

`resolveScriptDir()` in `cmd/nightshift/main.go` implements this logic. `config.DefaultConfigDir()` returns the per-user path.

## Log file structure

Logs at `logs/<IDENTIFIER>.log` (under the config dir) **append across attempts**:

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

## Releasing

Releases are automated with GoReleaser (`.goreleaser.yaml`) via `.github/workflows/release.yml`, triggered by pushing a `v*` tag. It cross-compiles linux `amd64`/`arm64`/`armv7` + darwin `amd64`/`arm64`, archives them with checksums, and publishes a GitHub Release. `main.version` is a `var` (not const) so the tag is stamped in via `-ldflags "-X main.version=..."`. Validate config changes locally with `goreleaser check` and `goreleaser release --snapshot --clean --skip=publish`.
