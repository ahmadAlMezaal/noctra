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
- Only acts on PRs **Noctra authored** — identified by the `noctra/<id>` branch prefix (`repo.BranchName`) **plus** a body marker (`github.IsNoctraAuthoredBody`, matching the hidden `github.NoctraPRBodyMarker` embedded by both PR body builders, or the legacy `"by [Noctra]"` footer). **Keep creation and watching in sync** (same prefix + marker) or the watcher silently never finds its own PRs — this is how sweep PRs were once missed (their footer lacked the old marker).
- ⚠️ **Branch-naming guardrail (applies to humans AND any agent — Claude/Codex/Copilot — working in this repo):** NEVER create a branch with the `noctra/` prefix for hand-written work or for a PR you open yourself. That prefix is **reserved** for branches the auto-iterate watcher creates and claims as its own. If `AUTO_ITERATE_PRS` is on, a running Noctra instance will treat any `noctra/*` PR **authored by its own GitHub account** as one it created (`ListNoctraPRs` filters `gh pr list` by both `--author @me` *and* the `noctra/` prefix), auto-iterate on its review feedback, and push follow-up commits that **collide with yours** (this happened on PR #143 — a `noctra/eng-183-…` branch opened under the same account the Pi runs as got grabbed and force-rejected the manual push). So the collision specifically hits when you run Noctra under your personal credentials and also open a `noctra/`-prefixed PR yourself. Use a neutral prefix instead: `fix/`, `feat/`, `docs/`, `chore/`, or `eng-<n>-<slug>`. (Once ENG-212 gives Noctra its own `noctra[bot]` GitHub identity, `--author @me` resolves to the bot, so human-authored PRs won't match regardless of prefix.)
- Feedback captured: conversation comments, review summaries (`CHANGES_REQUESTED` / non-empty `COMMENTED`), and inline review-thread comments (fetched separately via `gh api`, non-fatal on failure). `APPROVED`/`DISMISSED` and empty `COMMENTED` advance the cursor without acting.
- **CI failures** are a second trigger feeding the same `iteratePR`: when every check on the head commit (`statusCheckRollup`) has completed and ≥1 failed, `watch.diff` sets `PRChanges.CIFailure`; `iteratePR` fetches failed-step logs (`gh run view --log-failed`, truncated, best-effort) and folds them into the same fix prompt. When both review feedback and CI are pending, one re-engagement handles both.
- Trusted-reviewer rule (`watch.actionable`): humans always actionable; bots only if their login is in `TRUSTED_REVIEWERS`. (CI is not gated by this — it's not a person.)
- Guards: per-PR `MAX_PR_ITERATIONS` cap, **shared** across review + CI re-engagements (timeouts/rate-limits don't count), restart-safe cursor in `state`, `pipeline.active` dedupe so a ticket can't be freshly-dispatched and iterated at once.
- Cursors: comment/review are **timestamps** (naturally ordered; conversation + inline comments share the comment cursor). CI is keyed by **head commit SHA** (`LastCISHA`) — acted on once per commit, since a fix changes the SHA and makes a fresh failure eligible again.
- After re-engaging, Noctra replies to and resolves the PR's unresolved review threads (`github.ReplyAndResolveThreads`): "Addressed in `<sha>`." when it pushed a follow-up commit, or a "no change needed" note when the iteration produced no diff (so a no-diff review isn't silent on GitHub). It also notifies via Telegram/Linear.
- Commit subjects and the PR title follow Conventional Commits when the target repo uses them (`repo.UsesConventionalCommits` detects commitlint/semantic-release config). The type comes from the agent's release-bump suggestion (`patch`→`fix`, `minor`→`feat`, `major`→`feat!` + `BREAKING CHANGE`), so release tooling like semantic-release versions correctly; otherwise the default `feat: implement <id> — <title>` / `<id>: <title>` format is kept.

## Autonomous maintenance sweeps (optional)

When `SWEEP_ENABLED=true`, a **third** loop runs alongside the Linear and PR-watcher loops:

```
sweep loop → scheduler.DueIn (cron SWEEP_SCHEDULE or fixed SWEEP_INTERVAL) → scheduler.Plan (SWEEP_REPOS or all cloned repos × task catalog)
  → filter by cooldown (per-repo, per-task, from state store)
  → pipeline.processSweepTask (bounded, shares the worker pool + active-set)
  → repo.CreateWorktreeWithBranch → agent.Run (task-specific prompt) → commit/push → gh pr create
```

- **Opt-in**, off by default. Shares the `WaitGroup` so shutdown drains in-flight sweep tasks.
- Runs under the same budget caps as ticket-driven work — if budget is paused or exceeded, sweeps are skipped.
- Each task type has a per-repo **cooldown** persisted in the state file (`state.SweepState`), so the same task doesn't re-run before its cooldown expires (e.g. lint-cleanup has a 7-day cooldown).
- Sweep branches use the `noctra/sweep-<suffix>` prefix (e.g. `noctra/sweep-lint-cleanup`), distinct from ticket-driven `noctra/<identifier>`. Auto-iterate picks them up because sweep PR bodies carry the same hidden `NoctraPRBodyMarker` as ticket PRs (they match the `noctra/*` prefix and `IsNoctraAuthoredBody`). Before the marker fix they were silently skipped — the watcher matched a footer string sweep PRs didn't share.
- Sweep PRs get a `maintenance` label so humans can identify and bulk-close them.
- Sweep identifiers follow the `SWEEP-<repo-slug>-<task-suffix>` pattern for worktree directories and active-set dedup.
- Task catalog lives in `internal/sweep/task_*.go` — each file registers a task at init time. Current tasks: `lint-cleanup` (weekly), `dead-code` (biweekly), `deps-update` (weekly), `test-coverage` (biweekly), `doc-drift` (biweekly), `modernize` (biweekly), and `bug-scan` (biweekly — scoped to high-confidence defects only). Scope sweeps with `SWEEP_TASKS`.

### Config

| Env var | Default | Description |
|---------|---------|-------------|
| `SWEEP_ENABLED` | `false` | Enable the sweep scheduler |
| `SWEEP_SCHEDULE` | (empty) | Cron expression for when to sweep (e.g. `0 0 * * *` = daily midnight); empty = use `SWEEP_INTERVAL`. Parsed by `sweep.ParseCron` (zero-dep, standard 5-field); invalid → warn + fall back to interval |
| `SWEEP_INTERVAL` | `86400` (24h) | Seconds between sweep cycles (fallback when no cron). Interval mode fires immediately on startup; cron mode waits for the next matching time |
| `SWEEP_MAX_TASKS` | `5` | Max tasks per sweep run |
| `SWEEP_TASKS` | (all) | Comma-separated task names to enable (e.g. `lint-cleanup,dead-code`) |
| `SWEEP_REPOS` | (all cloned) | Comma-separated `owner/name` or git URLs to sweep, resolved via `repo.ResolveDirect` (clone-on-demand). When set, **replaces** the `AllRepoPaths()` discovery; unresolvable entries warn and are skipped. Empty = every cloned repo |

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

1. **Invocation** — `claudeArgs` (`claude --print`) vs `codexArgs` (`codex exec --dangerously-bypass-approvals-and-sandbox <prompt>`) vs `copilotArgs` (`copilot --allow-all-tools --no-ask-user -p <prompt>`). All go through the shared `runCLI` (timeout → `ErrTimedOut`, DEBUG header, log streaming).
2. **Rate-limit parsing** — `HasRateLimit` is per-backend (`claudeRateLimitRe` / `codexRateLimitRe` / `copilotRateLimitRe`) since the CLIs phrase usage/quota errors differently.

The required-CLI set is backend-aware: `git` + `gh` + the selected agent CLI (`config.RequiredCLIs` / `CheckCLIs`; `doctor` and the wizard surface it). Codex auth is a one-time `codex login` on the host (or `OPENAI_API_KEY`); Copilot auth is via `gh auth login` (or `GH_TOKEN`). Unlike the others, the Copilot CLI does **not** read gh's credential store when run headless (e.g. under systemd) — it only checks `COPILOT_GITHUB_TOKEN`/`GH_TOKEN`/`GITHUB_TOKEN`. So `copilotEnv` (in `agent/copilot.go`) bridges the gap: when no token env is set it mints one via `gh auth token` and injects `GH_TOKEN` into the child, making copilot work wherever gh is authed. Copilot **rejects classic PATs** (`ghp_`), so the bridge skips a classic-PAT token (warns instead of injecting) — gh must be authed with an OAuth token (`gho_`, via the `gh auth login` web flow) or a fine-grained PAT, or use `copilot /login`. Copilot also requires **Node 22+**.

## Package map

| Package | Purpose |
|---------|---------|
| `cmd/noctra` | Entry point + subcommand dispatch (`run` / `setup` / `cleanup` / `doctor` [`--json`] / `update` / `install-service` [`--start`/`--force`] / `logs` / `start` / `stop` / `restart` / `status` / `completion` / `version`); `start`/`stop`/`restart`/`status` are thin `systemctl --user <verb> noctra.service` wrappers (status also prints the binary version; missing-systemctl hint mirrors `logs`/`journalctl`), `install-service` delegates to `internal/service`, `completion bash\|zsh` prints a static shell-completion script (pure `completionScript` fn, unit-tested); startup banner; `--help` |
| `internal/config` | `.env` parser, validated `Config`, `DefaultConfigDir` (`~/.noctra/`) |
| `internal/linear` | Linear GraphQL client: `ResolveStateIDs`, `FetchTriggerIssues`, `FetchLabeledIssues` (both fetch each issue's `comments` so human clarifications reach the agent — see `Issue.ClarificationComments`, which filters out Noctra's own automated notices; project descriptions are fetched too, parsed by `Project.RepoDirective` for `Repo:`/`Branch:` routing), `ResolveLabelID`, `RemoveLabel`, `SetState`, `Comment`; read queries for Telegram — `ProjectIssueCounts`, `ListProjectIssues`, `SearchIssues`, `GetIssueByIdentifier`. Auth is a personal API key (`New`) sent verbatim, a static app-actor OAuth token (`NewOAuth`, `Bearer`) when `LINEAR_OAUTH_TOKEN` is set, or — preferred — a self-renewing actor=app credential (`oauth.go` `TokenManager`: mints 30-day app tokens from `LINEAR_OAUTH_CLIENT_ID`/`CLIENT_SECRET` via `grant_type=client_credentials&actor=app` — no refresh token or browser flow; if `LINEAR_OAUTH_REFRESH_TOKEN` is also set it uses `grant_type=refresh_token` instead, persisting rotations through `state.Store`). Both OAuth paths set `Client.FallbackAPIKey`, so an expired/revoked app token **degrades to the personal key** (with an `OnDegrade` alert) instead of crash-looping; a *partial* actor=app config (only one of id/secret) is non-fatal — `newLinearClient` warns and falls back. The `ClarificationComments` self-comment filter is body-based (`"**Noctra"` prefix), so it's unaffected by the actor. |
| `internal/repo` | Repo resolution: `ResolveDirect` (explicit `owner/name`/URL from a Linear project's `Repo:` directive or a PR's own repo, with `origin/HEAD` default-branch detection) + `Resolve` (the `REPO_PATH`-only fallback); `AllRepoPaths`/`AllRepoRemotes` (scan `ReposBase`); clone-on-demand; worktree create/cleanup; `BranchName`; `CreateWorktree` (from main) + `ResumeWorktree` (pull existing remote branch) |
| `internal/agent` | Pluggable coding-agent backends behind the `Backend` interface (`agent.New` selects `claude`/`codex`/`copilot` from `AGENT_BACKEND`); shared `exec` plumbing with timeout; per-backend invocation flags + rate-limit parsing (`claude.go` / `codex.go` / `copilot.go`); backend-agnostic implement-prompt builder, `BuildFixPrompt`, `BlockedLine`, and log_offset parsing |
| `internal/review` | Optional Gemini second-model review gate |
| `internal/notify` | Optional fire-and-forget notifiers behind the `Notifier` interface (`Send`/`SendSync`): Telegram, Slack, and Discord webhooks. `Multi` fans out to every configured backend at once (`buildNotifier` in `internal/pipeline`). Slack/Discord are enabled purely by a non-empty webhook URL (no `*_ENABLED` flag); Telegram keeps `TELEGRAM_ENABLED` since it needs both a token and chat ID. Messages are Telegram/Slack mrkdwn (single-`*` bold); the Discord notifier rewrites `*x*`→`**x**` and sends `allowed_mentions:{parse:[]}` so untrusted ticket text can't mass-ping a server |
| `internal/telegram` | Inbound Telegram listener: long-polling `getUpdates`, sender auth, command dispatcher; started inline by `Pipeline.Run` (the `noctra run` process) when Telegram is configured |
| `internal/github` | Thin `gh` CLI wrapper: `ListNoctraPRs`, `GetPR` (comments + reviews + inline review comments via REST + `statusCheckRollup`), `CheckLogs` (failed-step logs via `gh run view`) |
| `internal/state` | File-backed store (`~/.noctra-state.json`): per-PR comment/review timestamps + CI head-SHA + iteration count; sweep task cooldowns (`SweepState`); the rotating actor=app OAuth token (`LoadOAuth`/`SaveOAuth`, satisfies `linear.TokenStore`); atomic, mutex-guarded |
| `internal/watch` | Side-effect-free classifier: diffs a PR's feedback + CI status against the cursor, applies trusted-reviewer rules, emits actionable events + `CIFailure` |
| `internal/pipeline` | Poll loop, bounded worker pool, full per-ticket lifecycle (`process.go`); PR-watch loop + per-PR re-engagement (`iterate.go`); sweep scheduler loop + per-task lifecycle (`sweep.go`); Telegram command handlers — `/status`, `/tickets`, `/ticket`, `/search-tickets` (alias `/find`), `/kill`, `/requeue` (`commands.go`) |
| `internal/sweep` | Task catalog framework + scheduler for autonomous maintenance sweeps (ENG-222); task types registered at init (`task_lint.go`, `task_deadcode.go`); reuses `internal/repo`, `internal/agent`, `internal/state` |
| `internal/setup` | Interactive setup wizard (`./noctra setup`) |
| `internal/cleanup` | Cleanup subcommand: branches, worktrees, old logs |
| `internal/service` | `install-service` subcommand: renders the `systemd --user` unit (pure, unit-tested `unitFile(exePath, pathEnv)`) to `~/.config/systemd/user/noctra.service`, `daemon-reload`s; `--start` enables/starts + `loginctl enable-linger`; refuses without `--force` if the unit exists; non-systemd hosts get a clear error. Pairs with `scripts/install.sh` (the `curl … \| sh` turnkey installer that downloads the release binary) |
| `internal/doctor` | Preflight checks: CLIs on PATH, `gh auth`, Linear API key, repo routing (directive + optional `REPO_PATH`). `gather` collects checks side-effect-free; `Run` renders the human report, `RunJSON` (used by `doctor --json`) emits a `{name, ok, detail, hint}` JSON array + non-zero error on failure |
| `internal/selfupdate` | npm-style in-place upgrade: `Latest`/`IsNewer`/`assetName` (pure, tested) + `Update` (shells `gh` to download the GoReleaser archive matching `.goreleaser.yaml`, verifies SHA-256 vs `checksums.txt`, untars + atomic-swaps the running binary). `noctra run` also fires a best-effort `checkForUpdate` goroutine at startup (logs/pings if a newer release exists; no-op on dev builds) |

## Skills (deeper playbooks)

For deeper playbooks beyond this file, read `.claude/skills/<name>/SKILL.md`:

| Skill | When to use |
|-------|-------------|
| [`architecture`](.claude/skills/architecture/SKILL.md) | Modifying Noctra's own source — invariants, package boundaries, testability conventions |
| [`build-and-release`](.claude/skills/build-and-release/SKILL.md) | Local builds, cross-compiling for Pi, GoReleaser validation, cutting releases |
| [`setup-and-config`](.claude/skills/setup-and-config/SKILL.md) | Installing, configuring, running the setup wizard, `.env` / `Repo:` directive |
| [`troubleshooting`](.claude/skills/troubleshooting/SKILL.md) | Diagnosing failures — tickets not picked up, agent errors, PR creation, auto-iterate |
| [`writing-good-tickets`](.claude/skills/writing-good-tickets/SKILL.md) | Drafting Linear tickets Noctra can implement autonomously |

> `.claude/skills/` is a Claude Code discovery mechanism. Codex and Copilot don't auto-discover skills, but can open these files on demand via the paths above (since `AGENTS.md` is a symlink to this file).

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

Releases are automated with GoReleaser (`.goreleaser.yaml`). Two paths:

### Path 1: Label-driven (default on main)

When a PR is merged to `main`, `.github/workflows/tag-on-merge.yml`:
1. Checks for exactly one `release:major`, `release:minor`, or `release:patch` label
2. No label → no-op (safe default)
3. Multiple labels → fails (prevents mistakes)
4. Computes the next semver from the latest `v*` tag (e.g., `v0.5.2` + `release:patch` → `v0.5.3`)
5. Creates an annotated tag at the merge commit and pushes it
6. Invokes GoReleaser directly to publish the release with cross-compiled binaries and checksums

**Why GoReleaser runs in tag-on-merge:** Tags pushed with `GITHUB_TOKEN` don't trigger other workflows (GitHub policy). So rather than push a tag and hope `release.yml` fires, we call GoReleaser inline.

**Advantages:** Explicit per-PR control, no manual tagging, safe (unlabeled PRs never release).

### Path 2: Manual (always available)

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

`.github/workflows/release.yml` fires on any pushed `v*` tag and publishes a release via GoReleaser. This path is unchanged and always works — useful for hotfixes, backdates, or testing.

### Changelogs

GitHub Release notes are the canonical changelog. GoReleaser auto-generates them from Conventional Commit messages, grouped by category (Features, Bug Fixes, Performance, Refactoring). Commits prefixed `docs:`, `test:`, `chore:`, or `ci:` are excluded. `CHANGELOG.md` in the repo is a pointer to the Releases page — it is not maintained manually.

### Config validation

Validate GoReleaser config locally:
```bash
goreleaser check
goreleaser release --snapshot --clean --skip=publish
```

`main.version` is a `var` (not const) so the tag is stamped in via `-ldflags "-X main.version=..."`.
