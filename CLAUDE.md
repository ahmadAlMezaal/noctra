# Nightshift

Autonomous Linear-to-PR agent. Polls Linear for tickets in a trigger state, dispatches Claude Code to implement them, creates PRs, and moves tickets to review.

## Architecture

```
main loop (poll) → fetch_trigger_issues → process_ticket (background subshell per ticket)
  → resolve_repo → create_worktree → run_claude → check output → commit/push → gh pr create → linear update
```

Worktrees are created at `~/.nightshift-worktrees/<IDENTIFIER>` so multiple tickets can run concurrently without sharing a working directory.

## Multi-repo

The target repo is chosen **per-ticket** from the ticket's Linear **project**, not from a single global path. `repos.json` (gitignored) maps a project name → `{ url, main_branch }`. `resolve_repo` looks the project up, clones the repo on demand into `~/.nightshift-repos/<slug>`, and returns the local path + main branch, which are threaded through `create_worktree`, `cleanup_worktree`, and `gh pr create`.

If a ticket's project has no registry entry, Nightshift falls back to `REPO_PATH` from `.env` (if set), otherwise it skips the ticket with a Linear comment. Single-repo `.env`-only setups keep working unchanged.

`./nightshift.sh setup` is an interactive wizard that generates `.env` and `repos.json` — no hand-editing required. `repos.example.json` is the checked-in template.

## Key Functions

| Function | Purpose |
|----------|---------|
| `resolve_repo` | Maps a ticket's Linear project → local repo path + main branch; clones on demand |
| `registry_lookup` | Reads `repos.json`; returns the URL + main branch for a project |
| `repo_slug` | Slugifies a project name into a clone directory name |
| `create_worktree` | Creates git worktree + branch from latest main (takes repo path + main branch) |
| `cleanup_worktree` | Removes worktree via `git worktree remove --force` (takes repo path) |
| `run_claude` | Invokes `claude --print` with timeout; takes `workdir` as first param |
| `process_ticket` | Full lifecycle: resolve repo → worktree → claude → review → commit → PR → Linear update |
| `build_prompt` | Generates the prompt from ticket metadata |
| `gemini_review` | Optional second-model review gate |
| `fetch_trigger_issues` | Queries Linear GraphQL for tickets in trigger state (incl. project name) |
| `run_setup` | Interactive wizard — generates `.env` + `repos.json` |

## Configuration

Run `./nightshift.sh setup`, or copy `.env.example` → `.env` and `repos.example.json` → `repos.json`. Key variables:
- `repos.json` — maps Linear project name → repo URL + optional `main_branch`
- `REPO_PATH` — optional fallback repo for tickets whose project is not in `repos.json`
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
