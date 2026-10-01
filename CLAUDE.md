# Noctra

> `AGENTS.md` at the repo root is a symlink to this file, so the Codex backend (`AGENT_BACKEND=codex`) reads the same project guidance Claude does. Edit `CLAUDE.md`; `AGENTS.md` follows.

Autonomous Linear-to-PR agent in Go. Polls Linear for tickets in a trigger state or with a trigger label, dispatches Claude Code to implement them, creates PRs, and moves tickets to review. Optionally (`AUTO_ITERATE_PRS=true`) it also watches the PRs it opened and pushes follow-up commits in response to review feedback and CI failures.

## Architecture

```
poll loop → linear.FetchTriggerIssues / FetchLabeledIssues → pipeline.process (bounded goroutine)
  → repo.Resolve → repo.CreateOrResumeWorktree → agent.Run → check output
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
- ⚠️ **Branch-naming guardrail (applies to humans AND any agent — Claude/Codex/Copilot — working in this repo):** NEVER create a branch with the `noctra/` prefix for hand-written work or for a PR you open yourself. That prefix is **reserved** for branches the auto-iterate watcher creates and claims as its own. If `AUTO_ITERATE_PRS` is on, a running Noctra instance will treat any `noctra/*` PR **authored by its own GitHub account** as one it created (`ListNoctraPRs` filters `gh pr list` by both `--author @me` *and* the `noctra/` prefix), auto-iterate on its review feedback, and push follow-up commits that **collide with yours** (this happened on PR #143 — a `noctra/eng-183-…` branch opened under the same account the Pi runs as got grabbed and force-rejected the manual push). So the collision specifically hits when you run Noctra under your personal credentials and also open a `noctra/`-prefixed PR yourself. Use a neutral prefix instead: `fix/`, `feat/`, `docs/`, `chore/`, or `eng-<n>-<slug>`. In GitHub App mode (below) the watcher filters by `--author app/<slug>` instead, so human-authored PRs never match regardless of prefix; the guardrail still applies to anyone running in personal-token mode.
- Feedback captured: conversation comments, review summaries (`CHANGES_REQUESTED` / non-empty `COMMENTED`), and inline review-thread comments (fetched separately via `gh api`, non-fatal on failure). `APPROVED`/`DISMISSED` and empty `COMMENTED` advance the cursor without acting.
- **CI failures** are a second trigger feeding the same `iteratePR`: when every check on the head commit (`statusCheckRollup`) has completed and ≥1 failed, `watch.diff` sets `PRChanges.CIFailure`; `iteratePR` fetches failed-step logs (`gh run view --log-failed`, truncated, best-effort) and folds them into the same fix prompt. When both review feedback and CI are pending, one re-engagement handles both.
- Trusted-reviewer rule (`watch.actionable`): humans always actionable; bots only if their login is in `TRUSTED_REVIEWERS`. (CI is not gated by this — it's not a person.) Two exceptions, both non-actionable (cursor still advances): (1) a comment whose **whole** body is a bot-directed command (`@codex review`, `@gemini`, `/review`) — it targets another tool, not Noctra (a comment mixing such a command with real feedback still acts); (2) Noctra's **own** thread replies, identified by the hidden `github.NoctraReplyMarker`. Without (2), when Noctra runs under a personal GitHub account its replies look like human comments and get re-read as feedback → re-engage → reply → loop until `MAX_PR_ITERATIONS` (observed before the marker existed).
- Guards: per-PR `MAX_PR_ITERATIONS` cap, **shared** across review + CI re-engagements (timeouts/rate-limits don't count), restart-safe cursor in `state`, `pipeline.active` dedupe so a ticket can't be freshly-dispatched and iterated at once.
- Cursors: comment/review are **timestamps** (naturally ordered; conversation + inline comments share the comment cursor). CI is keyed by **head commit SHA** (`LastCISHA`) — acted on once per commit, since a fix changes the SHA and makes a fresh failure eligible again.
- After re-engaging, Noctra replies **per finding**: the fix prompt asks the agent to emit a JSON array (one entry per numbered review finding — `{finding, addressed, reply}`, wrapped in `agent.FindingsStartMarker`/`EndMarker` and parsed by `agent.ExtractFindingReplies`). Because the prompt's `### N)` findings map 1:1 to `watch.PRChanges.Events[N-1]`, and each inline finding's `Event.CommentID` equals its thread's `FirstCommentDatabaseID` (REST `id` == GraphQL `databaseId`), `pipeline.postIterationReplies` routes each finding's `reply` to the **exact** review thread it came from via `github.ReplyToThreadsByComment`, carrying the hidden `github.NoctraReplyMarker` so the watcher won't re-read its own reply. A thread is **resolved only when its finding is marked `addressed`** — so a finding Noctra pushed back on stays open, and a thread with no corresponding finding gets neither a reply nor a resolution (no more broadcasting one summary to every thread — the bug behind PR #246/#250, where one `gofmt`-fix reply was duplicated onto multiple unrelated Gemini findings). The reply body prefixes "Addressed in `<sha>`." when HEAD advanced (keyed on HEAD movement, **not** branch-ahead-of-remote, since the agent sometimes self-pushes). **Fallback:** when the agent emits no parseable per-finding block (older logs, a backend that didn't comply), Noctra posts a single conversation comment with the run summary (`agent.ExtractSummary`) and leaves every thread open — never the old per-thread broadcast. Conversation-level feedback still gets one comment so a no-diff review isn't silent on GitHub. The summary is persisted to `state.PRState.LastReasoning` and fed into the next iteration's fix prompt (`agent.FixPromptInput.PriorReasoning`) so re-engagements don't re-litigate settled feedback. It also notifies via Telegram/Linear.
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
- **Cost guards on a single run** (the daily `MAX_DAILY_*` caps only gate *between* dispatches — a run already in flight isn't stopped by them). Two per-run bounds close that gap: (1) `SWEEP_TIMEOUT_MINUTES` (default 20m, shorter than the ticket `AGENT_TIMEOUT`) caps a sweep's wall-clock; (2) `AGENT_MAX_TOKENS` (`config.AgentMaxTokens`, wired into every `agent.RunOptions.MaxTokens`) aborts *any* run mid-flight once cumulative token usage crosses the ceiling. The token ceiling is Claude-only — `claudeBackend.runCapped` streams `--output-format stream-json`, sums per-turn usage, and cancels the run's context on breach (returns `agent.ErrTokenCapExceeded`); other backends fall back to the timeout. Sweeps apply a built-in `config.DefaultSweepMaxTokens` (2M) floor when `AGENT_MAX_TOKENS` is unset, so a runaway sweep can't repeat the dead-code incident (one run burned 18.4M tokens / $33 before this guard). The floor was 8M until a bug-scan run burned the whole 8M in 4.5min (~$12) and shipped nothing — a productive sweep lands around 0.6M, so a run that blows past 2M is mis-scoped rather than nearly done.
- ⚠️ **The token ceiling is a guard, not a budget — raising it does not rescue a run.** Observed across three ceilings: every aborted sweep consumed *exactly* whatever ceiling it was given (8M→8,097,478; 2M→2,057,024 / 2,030,738 / 2,042,648; 5M→5,072,881 / 5,049,083 / 5,017,884) while every run that produced something finished under 1M (lint-cleanup 918,811 → `blocked`; doc-drift → PR in 3 min). A task that hits the ceiling is not under-provisioned, it is non-converging — the fix belongs in the task prompt, not in `AGENT_MAX_TOKENS`. The three offenders (`dead-code`, `test-coverage`, `deps-update`) shared an unbounded verify loop over a slow JS test suite (bump → full suite → revert → repeat), so their prompts now cap both the work (≤20 removals, ≤5 bumps) and the verification (≤2–4 full suite runs) and tell the agent to ship what is already green rather than keep iterating. `lint-cleanup` converged on the *same repo* `deps-update` died on twice, which is what ruled the repo out as the cause.
- **Aborted runs are accounted for and announced.** `claudeBackend.runCapped` only learns the true cost from the terminal `result` event, which never arrives when the cap fires `cancel()` — so aborted runs used to record `cost_usd = 0.0` and ~$30 of spend was invisible to `MAX_DAILY_USD`. It now accumulates the per-turn usage breakdown (plus the model name) while streaming and, whenever `result` is missing, derives an estimate from `agent.PricesForModel` (`internal/agent/pricing.go`). Both abort paths (`ErrTimedOut`, `ErrTokenCapExceeded`) run through `pipeline.abortSweepTask`: record usage, record the cooldown, write a `run_history` row with the distinct status **`aborted`** (not `failed` — the run was healthy, we killed it), and notify. Recording the cooldown on abort is deliberate: a task that aborts will abort again identically, so *not* recording it would re-burn the full ceiling every cycle; `/sweep --force` is the escape hatch. Previously the timeout path recorded nothing at all — no usage, no history, no cooldown — so timing-out tasks silently retried every cycle.
- **An aborted run's work is salvaged, not discarded.** Both abort paths used to `return` straight past the commit/push/PR block, and the deferred `repo.CleanupWorktree` then deleted the worktree — so every edit the agent had already made was thrown away. That is what made a ceiling hit feel like pure waste: a run could spend 5M tokens genuinely cleaning code and leave nothing behind. `pipeline.salvageAbortedWork` now checks the worktree for changes and, when there are any, commits them, pushes the branch and opens a **draft** PR titled `<prefix>: <description> (partial)` whose body states in the first line that the diff is **UNVERIFIED** — the agent never reached its own build/test step, so nothing on the branch is known to be green. The draft carries `github.NoctraPRBodyMarker` and the task's `maintenance` label like any sweep PR, so auto-iterate claims it and a CI failure drives a cheap targeted fix rather than a fresh full-cost scan. The `run_history` row stays **`aborted`** (it is not a clean `pr_opened`) but now carries the PR URL, and the abort notification links it. Cooldown behaviour is unchanged — still recorded, for the reason above.
- Every terminal outcome now notifies (`pipeline.notifySweepOutcome`), with token count and estimated cost: aborted, failed, blocked, no-change, PR opened. Before this, *only* a created PR sent a message, so six distinct outcomes were indistinguishable from silence and diagnosis meant opening the state DB by hand. Note the detached context — `markDone` cancels the task context the moment `processSweepTask` returns, so a fire-and-forget notifier send on that context races with cancellation; the helper uses `context.WithoutCancel`.
- Each task type has a per-repo **cooldown** persisted in the state DB (`sweep_states` table, `state.SweepState`), so the same task doesn't re-run before its cooldown expires (e.g. lint-cleanup has a 7-day cooldown).
- Sweep branches use the `noctra/sweep-<suffix>` prefix (e.g. `noctra/sweep-lint-cleanup`), distinct from ticket-driven `noctra/<identifier>`. Auto-iterate picks them up because sweep PR bodies carry the same hidden `NoctraPRBodyMarker` as ticket PRs (they match the `noctra/*` prefix and `IsNoctraAuthoredBody`). Before the marker fix they were silently skipped — the watcher matched a footer string sweep PRs didn't share.
- Sweep PRs get a `maintenance` label so humans can identify and bulk-close them.
- **Base branch** a sweep PR targets (and the worktree branches from) is resolved with precedence: (1) an `@branch` suffix on the `SWEEP_REPOS` entry (`scheduler.parseSweepRepoRef`); (2) the matching Linear project's `Branch:` directive (`linear.ListProjects` → `Project.RepoDirective`, matched by `github.ExtractOwnerRepo`; applies to both `SWEEP_REPOS` entries and discovery-path repos, the latter keyed off their `origin` remote via `repo.OriginRemoteOf`); (3) the repo's GitHub default branch (`origin/HEAD`); (4) `MAIN_BRANCH`. Resolved in `scheduler.repoTargets`/`Plan` into `Job.MainBranch`, which feeds the worktree base, `branchAhead` compare, and `gh pr create` base together. Before this, sweeps discarded the resolved branch and always targeted `origin/HEAD`.
- Sweep identifiers follow the `SWEEP-<repo-slug>-<task-suffix>` pattern for worktree directories and active-set dedup.
- ⚠️ Ticket dispatch goes through `repo.CreateOrResumeWorktree`, **not** `CreateWorktree` directly. A run that pushed its branch but died before `gh pr create` (a restart mid-run does exactly this) leaves an orphaned remote branch with no PR. `CreateWorktree` always branches from `origin/<main>`, so every later attempt built a non-descendant that origin rejected as non-fast-forward — the ticket could never land again and burned a full agent run per retry until `MAX_RETRIES`. Auto-iterate never hit this because it already used `ResumeWorktree`, but that path is only reachable via an existing PR. Keep fresh dispatch on the resume-aware helper.
- ⚠️ One sweep cycle can land **two tasks on the same repo** (`scheduler.roundRobin` spreads jobs across repos, but a second pass revisits them), and every worktree helper drives `git worktree add`/`branch -D` against the **shared clone**. Those all take the same `.git/config` lock, so `internal/repo` serializes them per clone via `lockRepo` — without it the loser fails with `could not lock config file .git/config` and its task is silently dropped. Keep any new `git` call that mutates the shared clone inside that lock.
- **Manual trigger** (ENG-378): sweeps also run on demand, without waiting for the schedule — `/sweep` on Telegram, `noctra sweep` on the CLI, or `POST /api/sweep` on the dashboard (admin token). All three funnel into `Pipeline.TriggerSweep`, which hands a `sweep.PlanOptions` to the **existing** `runSweepLoop` over the buffered `sweepNow` channel; the loop then dispatches through its own `WaitGroup`, active-set and `MAX_CONCURRENT` accounting. ⚠️ Do NOT make a manual trigger plan and dispatch inline — a second dispatcher beside the loop would bypass the worker-pool cap and double-run tasks. A manual sweep deliberately **skips `MarkSwept`**, so an ad-hoc run never shifts the scheduled cadence, and `PlanOptions.IgnoreCooldown` (`--force`) is the only way to re-run a task inside its cooldown. Budget pause/exceeded still refuses a manual sweep. `noctra sweep` is a thin HTTP client over the dashboard admin API, not a second sweeper — two processes planning against one state DB would double-dispatch and race on cooldowns.
- Task catalog lives in `internal/sweep/task_*.go` — each file registers a task at init time. Current tasks: `lint-cleanup` (weekly), `dead-code` (biweekly), `deps-update` (weekly), `test-coverage` (biweekly), `doc-drift` (biweekly), `modernize` (biweekly), and `bug-scan` (biweekly — scoped to high-confidence defects only). Scope sweeps with `SWEEP_TASKS`.

### Config

| Env var | Default | Description |
|---------|---------|-------------|
| `SWEEP_ENABLED` | `false` | Enable the sweep scheduler |
| `SWEEP_SCHEDULE` | (empty) | Cron expression for when to sweep (e.g. `0 0 * * *` = daily midnight); empty = use `SWEEP_INTERVAL`. Parsed by `sweep.ParseCron` (zero-dep, standard 5-field); invalid → warn + fall back to interval |
| `SWEEP_INTERVAL` | `86400` (24h) | Seconds between sweep cycles (fallback when no cron). Interval mode fires immediately on startup; cron mode waits for the next matching time |
| `SWEEP_MAX_TASKS` | `5` | Max tasks per sweep run |
| `SWEEP_TIMEOUT_MINUTES` | `20` | Per-sweep-run timeout, shorter than the ticket `AGENT_TIMEOUT_MINUTES` (45m) — maintenance work is best-effort and shouldn't run to the full wall. Bounds worst-case cost per sweep |
| `SWEEP_TASKS` | (all) | Comma-separated task names to enable (e.g. `lint-cleanup,dead-code`) |
| `SWEEP_REPOS` | (all cloned) | Comma-separated `owner/name` or git URLs to sweep, resolved via `repo.ResolveDirect` (clone-on-demand). When set, **replaces** the `AllRepoPaths()` discovery; unresolvable entries warn and are skipped. Empty = every cloned repo. Each entry may pin a base branch with an `@branch` suffix (e.g. `owner/name@staging`, also on full URLs / scp `git@host:owner/name@staging`) — see the base-branch precedence under the sweep loop |

## Multi-repo

The target repo is chosen **per-ticket**, not from a single global path. Routing is **directive-only** — there is no repo registry. Resolution order:

1. **Linear project directive** — if the ticket's Linear **project content/description** contains a `Repo: <owner/name | git URL>` line (optionally a `Branch: <name>` line), `repo.ResolveDirect` clones that repo directly. `linear.Project.RepoDirective` parses it (preferring the project `content` body, falling back to `description`); the trigger queries fetch `project { name description content }` to make it available. An `owner/name` shorthand is expanded to a GitHub HTTPS URL (full `https://`/`git@` URLs are used verbatim, so SSH and non-GitHub hosts work). With no `Branch:`, the repo's actual default branch is read from `origin/HEAD` after clone (fallback `MAIN_BRANCH`).
2. **`REPO_PATH`** — single-repo `.env`-only fallback for tickets whose project has no `Repo:` directive (`repo.Resolve`); otherwise the ticket is skipped with a Linear comment.

Clones land on demand in `~/.noctra-repos/<slug>` (lock-guarded against concurrent clone races via `mkdir(2)`) and return the local path + base branch. The **auto-iterate** path resolves the same way: `prRepoOwnerRepo` extracts `owner/name` from the PR URL and `ResolveDirect` clones it straight — directive-declared repos iterate without any registry.

Because there's no static registry, the set of repos Noctra knows about is just whatever it has cloned. `repo.Resolver.AllRepoPaths` discovers them by **scanning `ReposBase`** (plus `REPO_PATH`); `AllRepoRemotes` reads each clone's `origin` URL so the PR watcher (`watch.New(..., resolver.AllRepoRemotes, ...)`) can find Noctra-authored PRs across them, re-read on every scan as new repos are cloned.

`./noctra setup` is the interactive wizard that generates `.env` only — repos are routed via the Linear project `Repo:` directive, so there's no registry file to write.

## GitHub identity (`GITHUB_AUTH_MODE`)

Noctra talks to GitHub either as the user whose credentials are on the host (`token` mode, the original behaviour) or as `noctra-agent[bot]`, the public GitHub App owned by `onelastcommit` (`app` mode). `auto` (default) picks `app` once `noctra github login` has linked the host. The app's private key never reaches the host: the token service in [`onelastcommit/noctra-auth`](https://github.com/onelastcommit/noctra-auth) (`NOCTRA_AUTH_URL`, default `https://auth.getnoctra.dev`) holds it and mints installation tokens, each scoped to **one repository** and one of three scopes (`write` for PRs/labels/replies, `git` for push/fetch, `read` for agents). Setup notes for the app itself live in `deploy/github-app/`.

```
noctra github login → device flow (user token, never stored) → Ed25519 keypair in GITHUB_AUTH_DIR (0700/0600) → POST /link
noctra run → ghauthcmd.Activate → process env: git credential helper + bot commit identity; github.SetTokenSource
  git (clone/fetch/push) → `noctra git-credential --scope git` → signed POST /token per operation
  gh (every call) → github.Command(ctx, ownerRepo, …) → GH_TOKEN for that repo (write scope, cached until 10 min before expiry)
  agent run → RunOptions.Env: read-scope GH_TOKEN + read-scope git helper
```

- **Every `gh` call goes through `github.Command` / `CommandInDir`.** It needs the repository so it can pick the right installation token. In app mode it **fails closed**: a failed mint or unknown repository is an error, never a silent fall-back to the host's personal `gh` auth. Adding a raw `exec.CommandContext(ctx, "gh", …)` reintroduces personal-credential use in app mode.
- **Git is configured purely through the process environment** (`GIT_CONFIG_COUNT`/`KEY_n`/`VALUE_n`): an empty `credential.helper` first resets every helper from the user's git config (including `gh auth setup-git`'s URL-scoped one), then Noctra's helper is added, plus `credential.useHttpPath=true` so git sends the repository path. The user's `~/.gitconfig` is never edited. Commit identity comes from `GIT_AUTHOR_*`/`GIT_COMMITTER_*`.
- **Agents get read-only credentials.** Noctra does all pushing and PR work itself, so `Pipeline.agentEnv` hands each run a `read` token for the worktree's repository and a `read`-scope git helper. If minting fails the agent gets the sentinel `GH_TOKEN=noctra-token-unavailable` rather than nothing, because an empty `GH_TOKEN` would let `gh` fall back to the host's personal login. This is a guard against accidental pushes, not a sandbox: an agent runs as the same OS user and could read the instance key or a personal `gh` login from disk.
- **Copilot** needs a *user* token for model access, which an installation token cannot provide, so `copilotEnv` pins the user's token as `COPILOT_GITHUB_TOKEN` (which Copilot reads before `GH_TOKEN`).
- Token service, PR watcher author (`github.Client.Author`), banner (`GitHub as:`) and `doctor` (`github app` check) all reflect the mode. `GITHUB_AUTH_MODE=token` forces the old behaviour even when linked.

| Env var | Default | Description |
|---------|---------|-------------|
| `GITHUB_AUTH_MODE` | `auto` | `auto` (app when linked), `app` (fail if not linked) or `token` (personal credentials) |
| `NOCTRA_AUTH_URL` | `https://auth.getnoctra.dev` | Token service used by `noctra github login`; self-hosters point it at their own deployment. The linked URL is stored with the instance |
| `GITHUB_AUTH_DIR` | `~/.noctra/github` | Where the instance ID and private key live. Deliberately outside the cwd-checkout config override so a key is never written into a repository |

## Coding-agent backend (`AGENT_BACKEND`)

The runner is pluggable behind `agent.Backend` — `AGENT_BACKEND=claude` (default), `codex`, `copilot`, or `antigravity`. `agent.New(name)` returns the implementation; the `Pipeline` holds one instance and routes `Run` / `HasRateLimit` through it.

Almost everything in `internal/agent` is **backend-agnostic** and shared: the prompt builders (`BuildPrompt`, `BuildFixPrompt`), `BlockedLine` (keys off the `BLOCKED:` line our own prompt asks for), the log_offset helpers, and `ExtractSummary`. Only two things differ per backend:

1. **Invocation** — `claudeArgs` (`claude --print`) vs `codexArgs` (`codex exec --dangerously-bypass-approvals-and-sandbox <prompt>`) vs `copilotArgs` (`copilot --allow-all-tools --no-ask-user -p <prompt>`) vs `antigravityArgs` (`agy --dangerously-skip-permissions --print <prompt>`). All go through the shared `runCLI` (timeout → `ErrTimedOut`, DEBUG header, log streaming). ⚠️ Unlike Claude's boolean `--print` (prompt passed separately via `-p`), Antigravity's `--print`/`--prompt`/`-p` is a **string flag whose value is the prompt** — the auto-approve flag must precede it and the prompt must be the token immediately after, or `agy` swallows the next flag as its prompt and improvises.
2. **Rate-limit parsing** — `HasRateLimit` is per-backend (`claudeRateLimitRe` / `codexRateLimitRe` / `copilotRateLimitRe` / `antigravityRateLimitRe`) since the CLIs phrase usage/quota errors differently.

The required-CLI set is backend-aware: `git` + `gh` + the selected agent CLI (`config.RequiredCLIs` / `CheckCLIs`; `doctor` and the wizard surface it). Codex auth is a one-time `codex login` on the host (or `OPENAI_API_KEY`); Copilot auth is via `gh auth login` (or `GH_TOKEN`); Antigravity auth is a one-time `agy` login on the host (Google AI Pro). Unlike the others, the Copilot CLI does **not** read gh's credential store when run headless (e.g. under systemd) — it only checks `COPILOT_GITHUB_TOKEN`/`GH_TOKEN`/`GITHUB_TOKEN`. So `copilotEnv` (in `agent/copilot.go`) bridges the gap: when no token env is set it mints one via `gh auth token` and injects `GH_TOKEN` into the child, making copilot work wherever gh is authed. Copilot **rejects classic PATs** (`ghp_`), so the bridge skips a classic-PAT token (warns instead of injecting) — gh must be authed with an OAuth token (`gho_`, via the `gh auth login` web flow) or a fine-grained PAT, or use `copilot /login`. Copilot also requires **Node 22+** (the Docker image ships Node 24).

## Package map

| Package | Purpose |
|---------|---------|
| `cmd/noctra` | Entry point + subcommand dispatch (`run` / `setup` / `repos` / `sweep` / `github` / `git-credential` / `cleanup` / `doctor` [`--json`] / `update` / `install-service` [`--start`/`--force`] / `logs` / `tail` (alias for `logs -f`) / `start` / `stop` / `restart` / `status` / `completion` / `version`); `start`/`stop`/`restart`/`status` are thin `systemctl --user <verb> noctra.service` wrappers (status also prints the binary version; missing-systemctl hint mirrors `logs`/`journalctl`), `install-service` delegates to `internal/service`, `completion bash\|zsh` prints a static shell-completion script (pure `completionScript` fn, unit-tested); startup banner; `--help` |
| `internal/config` | `.env` parser, validated `Config`, `DefaultConfigDir` (`~/.noctra/`) |
| `internal/linear` | Linear GraphQL client: `ResolveStateIDs`, `FetchTriggerIssues`, `FetchLabeledIssues` (both fetch each issue's `comments` so human clarifications reach the agent — see `Issue.ClarificationComments`, which filters out Noctra's own automated notices; project descriptions are fetched too, parsed by `Project.RepoDirective` for `Repo:`/`Branch:` routing), `ResolveLabelID`, `RemoveLabel`, `SetState`, `Comment`; read queries for Telegram — `ProjectIssueCounts`, `ListProjectIssues`, `SearchIssues`, `GetIssueByIdentifier`; `ListProjects` also fetches each project's `id`/`slugId`/`url` so a pasted linear.app link resolves (`MatchProjects`) and `UpdateProjectContent` can write a `Repo:` directive back (`UpsertRepoDirective`). Auth is a personal API key (`New`) sent verbatim, a static app-actor OAuth token (`NewOAuth`, `Bearer`) when `LINEAR_OAUTH_TOKEN` is set, or — preferred — a self-renewing actor=app credential (`oauth.go` `TokenManager`: mints 30-day app tokens from `LINEAR_OAUTH_CLIENT_ID`/`CLIENT_SECRET` via `grant_type=client_credentials&actor=app` — no refresh token or browser flow; if `LINEAR_OAUTH_REFRESH_TOKEN` is also set it uses `grant_type=refresh_token` instead, persisting rotations through `state.Store`). Both OAuth paths set `Client.FallbackAPIKey`, so an expired/revoked app token **degrades to the personal key** (with an `OnDegrade` alert) instead of crash-looping; a *partial* actor=app config (only one of id/secret) is non-fatal — `linearclient.New` warns and falls back. The `ClarificationComments` self-comment filter is body-based (`"**Noctra"` prefix), so it's unaffected by the actor. |
| `internal/repo` | Repo resolution: `ResolveDirect` (explicit `owner/name`/URL from a Linear project's `Repo:` directive or a PR's own repo, with `origin/HEAD` default-branch detection) + `Resolve` (the `REPO_PATH`-only fallback); `AllRepoPaths`/`AllRepoRemotes` (scan `ReposBase`); clone-on-demand; worktree create/cleanup; `BranchName`; `CreateWorktree` (from main) + `ResumeWorktree` (pull existing remote branch) + `CreateOrResumeWorktree` (picks between them on `RemoteBranchExists`) |
| `internal/agent` | Pluggable coding-agent backends behind the `Backend` interface (`agent.New` selects `claude`/`codex`/`copilot` from `AGENT_BACKEND`); shared `exec` plumbing with timeout; per-backend invocation flags + rate-limit parsing (`claude.go` / `codex.go` / `copilot.go`); backend-agnostic implement-prompt builder, `BuildFixPrompt`, `BlockedLine`, and log_offset parsing |
| `internal/review` | Optional Gemini second-model review gate. In API mode it requests a structured JSON review (`verdict` + `summary` + line-anchored `findings`); `process.go` posts the findings as **inline PR comments** (`github.PostInlineComments`, each carrying `NoctraReplyMarker` so the watcher skips them) and leaves only a concise verdict + summary in the PR body. CLI mode and unparseable JSON fall back to the prose verdict block |
| `internal/notify` | Optional fire-and-forget notifiers behind the `Notifier` interface (`Send`/`SendSync`): Telegram, Slack, and Discord webhooks. `Multi` fans out to every configured backend at once (`buildNotifier` in `internal/pipeline`). Slack/Discord are enabled purely by a non-empty webhook URL (no `*_ENABLED` flag); Telegram keeps `TELEGRAM_ENABLED` since it needs both a token and chat ID. Messages are Telegram/Slack mrkdwn (single-`*` bold); the Discord notifier rewrites `*x*`→`**x**` and sends `allowed_mentions:{parse:[]}` so untrusted ticket text can't mass-ping a server |
| `internal/telegram` | Inbound Telegram listener: long-polling `getUpdates`, sender auth, command dispatcher; started inline by `Pipeline.Run` (the `noctra run` process) when Telegram is configured. Besides one-shot handlers (`Register`), the dispatcher supports guided multi-step flows (`RegisterConversation`): while one is live, plain messages route to it instead of the command table, and it ends on completion, `/cancel`, a 5-minute `sessionTTL`, or any other command (which interrupts it and still runs) |
| `internal/github` | Thin `gh` CLI wrapper: `ListNoctraPRs`, `GetPR` (comments + reviews + inline review comments via REST + `statusCheckRollup`), `CheckLogs` (failed-step logs via `gh run view`); `Command`/`CommandInDir` run every `gh` call with the repository's App token when a `TokenSource` is set |
| `internal/ghauth` | GitHub App client side: instance key storage (`Save`/`Load`, 0700/0600), request signing (`CanonicalString` must match `noctra-auth`'s byte for byte), token-service client, device flow, `Session` (per-repo token cache, process and agent environments), `ResolveMode` |
| `internal/ghauthcmd` | `noctra github login|logout|status`, the `noctra git-credential` helper, and `Activate`, which `runPoll` calls to switch the process into app mode |
| `internal/state` | SQLite store (`STATE_DB`, default `~/.noctra/state.db`; `modernc.org/sqlite`, single-conn): per-PR comment/review cursors + CI head-SHA + iteration count (`pr_states`); sweep cooldowns (`sweep_states`); plan/run-history/usage tables; the rotating actor=app OAuth token (`LoadOAuth`/`SaveOAuth`, satisfies `linear.TokenStore`). `OpenMigrating` one-time-imports the legacy JSON (`STATE_FILE`, `~/.noctra-state.json`) **only when the DB doesn't yet exist** — existing DBs are never clobbered and the JSON is never deleted (kept solely as a migration source) |
| `internal/watch` | Side-effect-free classifier: diffs a PR's feedback + CI status against the cursor, applies trusted-reviewer rules, emits actionable events + `CIFailure` |
| `internal/pipeline` | Poll loop, bounded worker pool, full per-ticket lifecycle (`process.go`); PR-watch loop + per-PR re-engagement (`iterate.go`); sweep scheduler loop + per-task lifecycle (`sweep.go`); Telegram command handlers — `/status`, `/tickets`, `/ticket`, `/search-tickets` (alias `/find`), `/kill`, `/requeue`, `/sweep` (`commands.go`), plus the guided `/addrepo` flow (`addrepo.go`) |
| `internal/sweep` | Task catalog framework + scheduler for autonomous maintenance sweeps (ENG-222); task types registered at init (`task_lint.go`, `task_deadcode.go`); reuses `internal/repo`, `internal/agent`, `internal/state` |
| `internal/repoadd` | Shared "add a repository" core behind every channel: clone via `repo.Resolver.ResolveDirect`, then write the Linear project's `Repo:` directive. Cloning happens **first**, so a directive is never left pointing at a repo the host can't reach; a `Result` with a `Path` plus an error means only the directive failed |
| `internal/reposcmd` | CLI channel for the same flow — `noctra repos add` (prompts for anything not passed as a flag) and `noctra repos list` |
| `internal/sweepcmd` | CLI channel for the manual sweep trigger — `noctra sweep [--task] [--repo] [--force]`, a thin authenticated client over the dashboard's `POST /api/sweep` |
| `internal/linearclient` | Builds an authenticated `linear.Client` from config; shared by the poll loop and CLI subcommands so credential precedence lives in one place |
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

## Code style

### ⚠️ Zero comments

**This codebase contains no comments. Do not add any.** This applies to humans AND any agent (Claude/Codex/Copilot/Antigravity) working in this repo — including doc comments on exported symbols, package doc comments, `TODO`/`FIXME` notes, section banners, and end-of-line asides. If you are editing a file and feel the urge to explain something, that urge is a signal to rename a symbol or extract a function, not to type `//`.

The **only** permitted `//` lines are compiler and tooling directives, which are not comments in any meaningful sense:

- `//go:embed`, `//go:build`, `//go:generate`
- `//nolint:…`
- `// Code generated … DO NOT EDIT.`

Enforced by `make check-comments` (`scripts/check-comments.sh`), which runs in CI and fails on any non-directive comment.

**Where the *why* lives instead:** this file for architecture and cross-cutting behaviour, and [`.claude/skills/architecture`](.claude/skills/architecture/SKILL.md) Invariant 7 for the file-level facts that are non-obvious from the code — lock ordering, cursor semantics, flag quirks, encoding traps. When you make a change whose reasoning is not evident from the diff, add a row there. Do not put it in the source.

Names and structure carry the *what*; the docs carry the *why*.

## Dashboard frontend (Preact + esbuild)

The operations dashboard lives in `internal/dashboard`. The UI is a **Preact + TypeScript** project under `internal/dashboard/web/` (`src/components/` = one component per panel: KPIs, active runs, queue, run history, donut, token/cost, spend, runs-by-repo, throughput, budget, repo cards, sweep matrix, log overlay), bundled with **esbuild** (`web/build.mjs`, ~3 deps — no Vite). It is **built into a single self-contained `index.html`** and committed to `internal/dashboard/static/`, which `dashboard.go` serves via `//go:embed static`.

**Why a single inlined file** (`build.mjs` inlines the esbuild JS + CSS output into the HTML): the page is served behind a read token, and `@font-face`/`<script>`/`<link>` subrequests don't carry the page's `?token=` query param. The old dashboard sidestepped this by being one inline-everything HTML file, with `/fonts/` carved out of auth (`mux.Handle("/fonts/", …)`). Inlining all JS + CSS preserves that exact model: the only subresource is fonts, and they stay unauthenticated. **Do not change the build to emit separate `/assets/*.js|css` chunks** — they'd hit the token gate and 401, and the page would silently fail to boot. Fonts live in `web/public/fonts/` (source of truth) → copied to `static/fonts/` at build, referenced by the absolute `/fonts/` URL (marked `external: ['/fonts/*']` in `build.mjs` so esbuild leaves them alone).

**Release pipeline — built output is committed (option B).** `go build ./...` embeds `static/`, so the directory must contain real files to compile, and `internal/dashboard/dashboard_test.go` serves the page under `go test ./...` with no Node. Building in CI only (option A) would require a Node step in front of every `go build`/`go test`, breaking the no-Node dev/build flow. Committing the build output keeps the release workflow (`tag-on-merge.yml` → GoReleaser) and the Dockerfile **pure Go — no Node step**. The trade-off: generated assets live in git, so **after changing anything under `web/`, rebuild and commit `static/`**. A CI job (`dashboard-bundle` in `.github/workflows/ci.yml`) guards against forgetting: it runs `yarn build` and fails if the committed `static/` differs from a fresh build. So CI doesn't regenerate the bundle for you, but it won't let a stale one merge:

```bash
cd internal/dashboard/web
yarn install --frozen-lockfile   # first time / after dependency changes
yarn build                       # type-checks, then writes internal/dashboard/static/index.html (+ fonts/)
# commit the regenerated internal/dashboard/static/
```

For a quick local preview, `yarn dev` (`web/devserver.mjs`) runs a zero-config dev server — esbuild in watch mode plus a mock `/api` (sample data + SSE) — at `http://localhost:8080/?token=dev&admin_token=dev`, with no Go rebuild or `.env` needed. It builds to a gitignored `web/.dev/`, so it never dirties the committed `static/`. To test against the **real** backend instead, use `yarn watch` (rebuilds `static/` on change) and rebuild the Go binary — `//go:embed` bakes `static/` in at compile time, so an already-built binary won't reflect UI edits until you `go build` again. `web/node_modules/` is gitignored; the committed `static/index.html` is the build artifact and is marked `linguist-generated` in `.gitattributes` so its minified diff is collapsed. Keep the port faithful — the live `index.html` in git history is the source of truth for exact visuals/derivations.

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

`Dockerfile` is a multi-stage build: a `golang` stage compiles the static binary, and a `node:20-bookworm-slim` runtime stage adds `git` + `gh` + all agent CLIs (`@anthropic-ai/claude-code`, `@openai/codex`, `@github/copilot`, all via npm) — Noctra shells out to all of them, so the image can't be `scratch`. `docker-entrypoint.sh` sets a default git identity and wires `GH_TOKEN` into git/gh (a fresh container has neither — both were silently inherited from the dev's machine before). All mutable state is redirected under `/data` (a single volume) via the `REPOS_BASE`/`WORKTREE_BASE`/`LOG_DIR`/`STATE_DB` env overrides (plus legacy `STATE_FILE` as the one-time JSON migration source). `.github/workflows/docker.yml` builds on PRs (validation) and builds+pushes multi-arch (amd64/arm64) to GHCR on `main`/tags. Container auth is API-key based (no interactive login) — see the README "Docker" section. Cloud deploy templates consuming this image live at the repo root: `fly.toml`, `render.yaml`, `railway.json`, and `deploy/digitalocean-cloud-init.yaml` (repos are declared per-project in Linear, so PaaS needs no file mount).

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
