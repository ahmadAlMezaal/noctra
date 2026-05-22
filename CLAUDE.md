# Nightshift

Autonomous Linear-to-PR agent. Polls Linear for tickets in a trigger state, dispatches Claude Code to implement them, creates PRs, and moves tickets to review.

## Architecture

```
main loop (poll) → fetch_trigger_issues → process_ticket (background subshell per ticket)
  → resolve_repo → create_worktree → run_claude → check output → commit/push → gh pr create → linear update
```

Worktrees are created at `~/.nightshift-worktrees/<IDENTIFIER>` so multiple tickets can run concurrently without sharing a working directory.

## Key Functions

| Function | Purpose |
|----------|---------|
| `resolve_repo` | Picks the target repo for a ticket from its `repo:<key>` label; echoes `path<TAB>branch` |
| `repo_key_from_labels` | Extracts the `repo:` key from a ticket's label list |
| `ensure_repo_clone` | Clones a repo on first use (fetches after) into `REPO_CACHE_BASE`, lock-guarded |
| `create_worktree` | Creates git worktree + branch from latest main (takes `repo_path`, `branch` params) |
| `cleanup_worktree` | Removes worktree via `git worktree remove --force` |
| `run_claude` | Invokes `claude --print` with timeout; takes `workdir` as first param |
| `process_ticket` | Full lifecycle: resolve repo → worktree → claude → review → commit → PR → Linear update |
| `build_prompt` | Generates the prompt from ticket metadata |
| `gemini_review` | Optional second-model review gate |
| `fetch_trigger_issues` | Queries Linear GraphQL for tickets in trigger state (includes labels) |

## Multi-Repo Routing

A ticket targets a repo via a Linear label `repo:<key>`. Keys are defined in
`repos.json` (see `repos.example.json`), mapping each key to a git URL and
optional base branch. `resolve_repo` reads the label, and `ensure_repo_clone`
clones the repo on demand into `~/.nightshift-repos/<key>` (reused after).
A ticket with no `repo:` label falls back to `REPO_PATH`, so single-repo
setups are unaffected. Concurrent tickets for the same repo are serialised by
an atomic-`mkdir` lock under `REPO_CACHE_BASE/.locks` (cleared at startup).

## Configuration

Copy `.env.example` to `.env`. All config is documented there. Key variables:
- `REPO_PATH` — absolute path to the default repo (fallback for unlabelled tickets)
- `REPOS_FILE` — path to `repos.json` for label-based multi-repo routing
- `LINEAR_API_KEY`, `LINEAR_TEAM_KEY` — Linear access
- `MAX_CONCURRENT` — number of parallel tickets (each gets its own worktree)

## Log File Structure

Log files at `.agent-logs/<IDENTIFIER>.log` **append across attempts**:

```
--- Attempt 2024-01-01T00:00:00 ---
DEBUG: pwd = /path
<claude output>
--- Attempt 2024-01-01T01:00:00 ---
DEBUG: pwd = /path
<claude output>
```

### IMPORTANT: log_offset pattern

The `log_offset + tail -c` pattern in `process_ticket` is a critical bug fix. It records the file size before Claude runs, then uses `tail -c +$offset` to extract only the current attempt's output for BLOCKED/rate-limit checks. **Do not replace this with a grep over the full file** — that re-detects failures from previous attempts and causes false positives.

## Running Tests

```bash
bash tests/run_tests.bash
```

Tests use plain bash (no bats). The `NIGHTSHIFT_TESTING=true` guard prevents the entrypoint from executing when sourcing `nightshift.sh`.

## Syntax Check

```bash
bash -n nightshift.sh
```
