# Noctra

> `AGENTS.md` at the repo root is a symlink to this file, so the Codex backend (`AGENT_BACKEND=codex`) reads the same project guidance Claude does. Edit `CLAUDE.md`; `AGENTS.md` follows.

Autonomous Linear-to-PR agent in Go. Polls Linear for tickets in a trigger state or with a trigger label, dispatches Claude Code to implement them, creates PRs, and moves tickets to review. Optionally (`AUTO_ITERATE_PRS=true`) it also watches the PRs it opened and pushes follow-up commits in response to review feedback and CI failures.

## Architecture

```
poll loop → linear.FetchTriggerIssues / FetchLabeledIssues → pipeline.process (bounded goroutine)
  → repo.Resolve → repo.CreateWorktree → agent.Run → check output
  → (optional) review.Gate → commit/push → gh pr create → linear update
```

### Trigger modes

* `TRIGGER_MODE=state` (default): polls for tickets in the `TRIGGER_STATE` column (e.g. "Next"). Current behaviour, unchanged.
* `TRIGGER_MODE=label`: polls for tickets carrying the `TRIGGER_LABEL` label (e.g. "noctra"). Tagging a ticket with the label picks it up regardless of its column. The label is **removed** after dispatch so the ticket isn't re-polled; the In-Review state transition still applies. In label mode the trigger-state ID is not resolved (the in-review state is still required).

Worktrees live at `~/.noctra-worktrees/<IDENTIFIER>` so multiple tickets run concurrently without sharing a working directory.

## Auto-iterate on PR feedback (optional)

When `AUTO_ITERATE_PRS=true`, a **second** poll loop runs alongside the Linear one:

```
PR poll loop → github.ListNoctraPRs → github.GetPR (comments+reviews+statusCheckRollup)
  → watch.Scan (diff vs state cursor: new feedback OR failing CI on a new head SHA)
  → pipeline.iteratePR (bounded, shares the worker pool + active-set)
  → repo.ResumeWorktree → [github.CheckLogs for CI] → agent.BuildFixPrompt → agent.Run
  → commit/push (same branch) → state.Update (advance comment/review/CI cursor + bump iteration count)
```

- **Opt-in**, off by default. Both loops run on the same `WaitGroup` so shutdown drains in-flight iterations.
- Only acts on PRs **Noctra authored** — identified purely by the `noctra/<id>` branch prefix (`repo.BranchName`). `CreateWorktree` and `ResumeWorktree` both use it; `watch`/`iterate` filter on it. **Keep creation and watching on the same prefix** or the watcher silently never finds its own PRs.
- Feedback captured: conversation comments, review summaries (`CHANGES_REQUESTED` / non-empty `COMMENTED`), and inline review-thread comments (fetched separately via `gh api`, non-fatal on failure). `APPROVED`/`DISMISSED` and empty `COMMENTED` advance the cursor without acting.
- **CI failures** are a second trigger feeding the same `iteratePR`: when every check on the head commit (`statusCheckRollup`) has completed and ≥1 failed, `watch.diff` sets `PRChanges.CIFailure`; `iteratePR` fetches failed-step logs (`gh run view --log-failed`, truncated, best-effort) and folds them into the same fix prompt. When both review feedback and CI are pending, one re-engagement handles both.
- Trusted-reviewer rule (`watch.actionable`): humans always actionable; bots only if their login is in `TRUSTED_REVIEWERS`. (CI is not gated by this — it's not a person.)
- Guards: per-PR `MAX_PR_ITERATIONS` cap, **shared** across review + CI re-engagements (timeouts/rate-limits don't count), restart-safe cursor in `state`, `pipeline.active` dedupe so a ticket can't be freshly-dispatched and iterated at once.
- Cursors: comment/review are **timestamps** (naturally ordered; conversation + inline comments share the comment cursor). CI is keyed by **head commit SHA** (`LastCISHA`) — acted on once per commit, since a fix changes the SHA and makes a fresh failure eligible again.
- Noctra does **not** post back to the GitHub PR thread (no replies, no thread resolution); it pushes a follow-up commit and notifies via Telegram/Linear.

## Multi-repo

The target repo is chosen **per-ticket**, not from a single global path. Routing is **directive-only** — there is no repo registry. Resolution order:

1. **Linear project directive** — if the ticket's Linear **project content/description** contains a `Repo: <owner/name | git URL>` line (optionally a `Branch: <name>` line), `repo.ResolveDirect` clones that repo directly. `linear.Project.RepoDirective` parses it (preferring the project `content` body, falling back to `description`); the trigger queries fetch `project { name description content }` to make it available. An `owner/name` shorthand is expanded to a GitHub HTTPS URL (full `https://`/`git@` URLs are used verbatim, so SSH and non-GitHub hosts work). With no `Branch:`, the repo's actual default branch is read from `origin/HEAD` after clone (fallback `MAIN_BRANCH`).
2. **`REPO_PATH`** — single-repo `.env`-only fallback for tickets whose project has no `Repo:` directive (`repo.Resolve`); otherwise the ticket is skipped with a Linear comment.

Clones land on demand in `~/.noctra-repos/<slug>` (lock-guarded against concurrent clone races via `mkdir(2)`) and return the local path + base branch. The **auto-iterate** path resolves the same way: `prRepoOwnerRepo` extracts `owner/name` from the PR URL and `ResolveDirect` clones it straight — directive-declared repos iterate without any registry.

Because there's no static registry, the set of repos Noctra knows about is just whatever it has cloned. `repo.Resolver.AllRepoPaths` discovers them by **scanning `ReposBase`** (plus `REPO_PATH`); `AllRepoRemotes` reads each clone's `origin` URL so the PR watcher (`watch.New(..., resolver.AllRepoRemotes, ...)`) can find Noctra-authored PRs across them, re-read on every scan as new repos are cloned.

`./noctra setup` is the interactive wizard that generates `.env` only — repos are routed via the Linear project `Repo:` directive, so there's no registry file to write.

## Coding-agent backend (`AGENT_BACKEND`)

The runner is pluggable behind `agent.Backend` — `AGENT_BACKEND=claude` (default), `codex`, or `copilot`. `agent.New(name)` returns the implementation; the `Pipeline` holds one instance and routes `Run` / `HasRateLimit` through it.

Almost everything in `internal/agent` is **backend-agnostic** and shared: the prompt builders (`BuildPrompt`, `BuildFixPrompt`), `BlockedLine` (keys off the `BLOCKED:` line our own prompt asks for), the log_offset helpers, and `ExtractSummary`. Only two things differ per backend:

1. **Invocation** — `claudeArgs` (`claude --print`) vs `codexArgs` (`codex exec --dangerously-bypass-approvals-and-sandbox <prompt>`) vs `copilotArgs` (`copilot --allow-all-tools -p <prompt>`). All go through the shared `runCLI` (timeout → `ErrTimedOut`, DEBUG header, log streaming).
2. **Rate-limit parsing** — `HasRateLimit` is per-backend (`claudeRateLimitRe` / `codexRateLimitRe` / `copilotRateLimitRe`) since the CLIs phrase usage/quota errors differently.

The required-CLI set is backend-aware: `git` + `gh` + the selected agent CLI (`config.RequiredCLIs` / `CheckCLIs`; `doctor` and the wizard surface it). Codex auth is a one-time `codex login` on the host (or `OPENAI_API_KEY`); Copilot auth is via `gh auth login` (or `GH_TOKEN`) — Noctra doesn't manage either.

## Package map

| Package | Purpose |
|---------|---------|
| `cmd/noctra` | Entry point + subcommand dispatch (`run` / `setup` / `cleanup` / `doctor` [`--json`] / `update` / `install-service` [`--start`/`--force`] / `logs` / `start` / `stop` / `restart` / `status` / `completion` / `version`); `start`/`stop`/`restart`/`status` are thin `systemctl --user <verb> noctra.service` wrappers (status also prints the binary version; missing-systemctl hint mirrors `logs`/`journalctl`), `install-service` delegates to `internal/service`, `completion bash\|zsh` prints a static shell-completion script (pure `completionScript` fn, unit-tested); startup banner; `--help` |
| `internal/config` | `.env` parser, validated `Config`, `DefaultConfigDir` (`~/.noctra/`) |
| `internal/linear` | Linear GraphQL client: `ResolveStateIDs`, `FetchTriggerIssues`, `FetchLabeledIssues` (both fetch each issue's `comments` so human clarifications reach the agent — see `Issue.ClarificationComments`, which filters out Noctra's own automated notices; project descriptions are fetched too, parsed by `Project.RepoDirective` for `Repo:`/`Branch:` routing), `ResolveLabelID`, `RemoveLabel`, `SetState`, `Comment`; read queries for Telegram — `ProjectIssueCounts`, `ListProjectIssues`, `SearchIssues`, `GetIssueByIdentifier` |
| `internal/repo` | Repo resolution: `ResolveDirect` (explicit `owner/name`/URL from a Linear project's `Repo:` directive or a PR's own repo, with `origin/HEAD` default-branch detection) + `Resolve` (the `REPO_PATH`-only fallback); `AllRepoPaths`/`AllRepoRemotes` (scan `ReposBase`); clone-on-demand; worktree create/cleanup; `BranchName`; `CreateWorktree` (from main) + `ResumeWorktree` (pull existing remote branch) |
| `internal/agent` | Pluggable coding-agent backends behind the `Backend` interface (`agent.New` selects `claude`/`codex`/`copilot` from `AGENT_BACKEND`); shared `exec` plumbing with timeout; per-backend invocation flags + rate-limit parsing (`claude.go` / `codex.go` / `copilot.go`); backend-agnostic implement-prompt builder, `BuildFixPrompt`, `BlockedLine`, and log_offset parsing |
| `internal/review` | Optional Gemini second-model review gate |
| `internal/notify` | Optional Telegram notifier (fire-and-forget) |
| `internal/telegram` | Inbound Telegram listener: long-polling `getUpdates`, sender auth, command dispatcher; started inline by `Pipeline.Run` (the `noctra run` process) when Telegram is configured |
| `internal/github` | Thin `gh` CLI wrapper: `ListNoctraPRs`, `GetPR` (comments + reviews + inline review comments via REST + `statusCheckRollup`), `CheckLogs` (failed-step logs via `gh run view`) |
| `internal/state` | File-backed PR cursor store (`~/.noctra-state.json`): per-PR comment/review timestamps + CI head-SHA + iteration count; atomic, mutex-guarded |
| `internal/watch` | Side-effect-free classifier: diffs a PR's feedback + CI status against the cursor, applies trusted-reviewer rules, emits actionable events + `CIFailure` |
| `internal/pipeline` | Poll loop, bounded worker pool, full per-ticket lifecycle (`process.go`); PR-watch loop + per-PR re-engagement (`iterate.go`); Telegram command handlers — `/status`, `/tickets`, `/ticket`, `/search-tickets` (alias `/find`), `/kill`, `/requeue` (`commands.go`) |
| `internal/setup` | Interactive setup wizard (`./noctra setup`) |
| `internal/cleanup` | Cleanup subcommand: branches, worktrees, old logs |
| `internal/service` | `install-service` subcommand: renders the `systemd --user` unit (pure, unit-tested `unitFile(exePath, pathEnv)`) to `~/.config/systemd/user/noctra.service`, `daemon-reload`s; `--start` enables/starts + `loginctl enable-linger`; refuses without `--force` if the unit exists; non-systemd hosts get a clear error. Pairs with `scripts/install.sh` (the `curl … \| sh` turnkey installer that downloads the release binary) |
| `internal/doctor` | Preflight checks: CLIs on PATH, `gh auth`, Linear API key, repo routing (directive + optional `REPO_PATH`). `gather` collects checks side-effect-free; `Run` renders the human report, `RunJSON` (used by `doctor --json`) emits a `{name, ok, detail, hint}` JSON array + non-zero error on failure |
| `internal/selfupdate` | npm-style in-place upgrade: `Latest`/`IsNewer`/`assetName` (pure, tested) + `Update` (shells `gh` to download the GoReleaser archive matching `.goreleaser.yaml`, verifies SHA-256 vs `checksums.txt`, untars + atomic-swaps the running binary). `noctra run` also fires a best-effort `checkForUpdate` goroutine at startup (logs/pings if a newer release exists; no-op on dev builds) |

## Config directory

Config defaults to `~/.noctra/` (`.env`, `logs/`). This is consistent with the existing `~/.noctra-*` convention (worktrees, repos, state). The **cwd-checkout override** still works: if the current directory contains `.env`, `.env.example`, or `go.mod`, Noctra uses cwd instead — so `go run` during development still works without touching `~/.noctra/`.

`resolveScriptDir()` in `cmd/noctra/main.go` implements this logic. `config.DefaultConfigDir()` returns the per-user path.

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
go build -o noctra ./cmd/noctra

# Raspberry Pi (arm64 — Pi 4 / 5 with 64-bit OS)
GOOS=linux GOARCH=arm64 go build -o noctra ./cmd/noctra

# Raspberry Pi (32-bit, armv7)
GOOS=linux GOARCH=arm GOARM=7 go build -o noctra ./cmd/noctra
```

`go vet ./...` should be clean.

## Docker

`Dockerfile` is a multi-stage build: a `golang` stage compiles the static binary, and a `node:20-bookworm-slim` runtime stage adds `git` + `gh` + all agent CLIs (`@anthropic-ai/claude-code`, `@openai/codex`, `@github/copilot`, all via npm) — Noctra shells out to all of them, so the image can't be `scratch`. `docker-entrypoint.sh` sets a default git identity and wires `GH_TOKEN` into git/gh (a fresh container has neither — both were silently inherited from the dev's machine before). All mutable state is redirected under `/data` (a single volume) via the `REPOS_BASE`/`WORKTREE_BASE`/`LOG_DIR`/`STATE_FILE` env overrides. `.github/workflows/docker.yml` builds on PRs (validation) and builds+pushes multi-arch (amd64/arm64) to GHCR on `main`/tags. Container auth is API-key based (no interactive login) — see the README "Docker" section. Cloud deploy templates consuming this image live at the repo root: `fly.toml`, `render.yaml`, `railway.json`, and `deploy/digitalocean-cloud-init.yaml` (repos are declared per-project in Linear, so PaaS needs no file mount).

## Operating (systemd)

Day-2 operations are wrapped by the `Makefile` (run `make help` to list targets); the README "Operating the service" section documents them for users. The important ones:

- `make update` — pull `main`, rebuild to a side file, **atomic-swap** the binary (safe while the old process is still executing), then `systemctl --user restart noctra`. This is the upgrade path on the Pi.
- `make start` / `stop` / `restart` / `status` / `logs` — thin `systemctl --user` wrappers (`logs` tails `journalctl --user-unit=noctra.service -f`).

The startup banner (`pipeline.banner`) prints the resolved runtime config — repos, watched trigger, **agent backend** (`p.agent.Label()` + CLI), review gate, auto-iterate, notifications — so a restart's `make logs` output shows exactly what's live. Keep new operationally-significant config visible there.

## Releasing

Releases are automated with GoReleaser (`.goreleaser.yaml`) via `.github/workflows/release.yml`, triggered by pushing a `v*` tag. It cross-compiles linux `amd64`/`arm64`/`armv7` + darwin `amd64`/`arm64`, archives them with checksums, and publishes a GitHub Release. `main.version` is a `var` (not const) so the tag is stamped in via `-ldflags "-X main.version=..."`. Validate config changes locally with `goreleaser check` and `goreleaser release --snapshot --clean --skip=publish`.
