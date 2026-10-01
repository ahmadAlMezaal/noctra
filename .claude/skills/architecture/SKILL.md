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
| `internal/ghauth` / `internal/ghauthcmd` | GitHub App mode: instance key, signed token-service client, device flow, git credential helper, `github login/logout/status` |
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

## Invariant 7: the codebase carries no comments

Source files hold **zero comments** — the only exception is a compiler/tooling directive (`//go:embed`,
`//go:build`, `//nolint`, `// Code generated`). Do not reintroduce explanatory comments, doc comments on
exported symbols included. Names and structure carry the *what*; this file and `CLAUDE.md` carry the *why*.

The non-obvious facts that previously lived in comments, kept here so removing them lost nothing:

| Where | Invariant |
|---|---|
| `repo/worktree.go` | Worktree mutations serialize per clone via `lockRepo` — concurrent `git worktree add` on one clone collides on the `.git/config` lock. Remove the worktree **before** the branch: git won't delete a branch still checked out, and a leftover branch fails `worktree add -b`. |
| `repo/worktree.go` | Ticket dispatch uses `CreateOrResumeWorktree`, never `CreateWorktree` directly: a run that pushed its branch but died before `gh pr create` leaves an orphaned remote branch, and re-branching from main produces a non-descendant origin rejects as non-fast-forward. |
| `repo/worktree.go` | `clearWorktreePath` falls back to `os.RemoveAll` + `git worktree prune` when `worktree remove` fails. A directory git no longer tracks as a worktree (e.g. a half-deleted `node_modules` left by an interrupted cleanup) makes `worktree remove` a no-op and `worktree add` fail with "already exists" — forever, since nothing else deletes it. |
| `pipeline/sweep_branch.go` | Sweep branch names are fixed per repo+task, so they are reused across runs. A sweep skips while its previous PR is still open (a fresh run would collide with it), and otherwise pushes with `--force-with-lease` pinned to the SHA `ls-remote` just reported: the only remote branch left at that point belongs to a merged or closed PR, and a fresh branch from main is never its descendant. |
| `pipeline/iterate.go` | Every failure path must record the iteration before returning, or the cursor never advances and the same feedback loops forever. Two deliberate exceptions: infra failures (timeout / rate-limit) don't increment — they weren't real attempts — and a shutdown cancellation isn't recorded at all, since it would bump the count and could fire the cap warning. |
| `lessons/lessons.go` | Lessons come only from the PR branch's own (`--first-parent`, `--no-merges`) commits after `LastPushedSHA` that are neither `[bot]`-authored nor Noctra's. Noctra commits under the host's git identity, so it is recognised by the commit-body lines `Implemented by Noctra` / `Follow-up commit by Noctra` / `Autonomous maintenance by Noctra` (`noctraCommitRe`) — reword those in `pipeline` and Noctra starts learning from itself. A plain `git diff` also swept in everything an "Update branch" merge pulled from main, which is how feature descriptions ended up stored as conventions. |
| `pipeline/iterate.go` | Push whenever the branch is ahead, not just when the worktree is dirty: the agent sometimes self-commits, and gating on dirtiness alone silently drops those commits (ENG-182). |
| `pipeline/plan.go` | `hasPendingPlan` takes `p.mu` itself — callers must **not** already hold it. |
| `pipeline/sweep.go` | A manual sweep deliberately skips `MarkSwept` so an ad-hoc run never shifts the scheduled cadence. |
| `pipeline/sweep.go` | Both abort paths (timeout, token cap) **do** record the cooldown. A task that aborts will abort again identically, so skipping it would re-burn the full ceiling every cycle; `/sweep --force` is the escape hatch. Their `run_history` status is `aborted`, not `failed` — the run was healthy, we killed it. |
| `pipeline/sweep.go` | Outcome notifications send on `context.WithoutCancel(ctx)`: `markDone` cancels the task context the instant `processSweepTask` returns, and `notifier.Send` is fire-and-forget, so a plain `ctx` races with its own cancellation. |
| `agent/claude.go` | `runCapped` learns the real cost only from the terminal `result` event, which never arrives when the cap fires `cancel()`. It accumulates the per-turn usage breakdown plus the model name while streaming and estimates from `PricesForModel` whenever `result` is missing — otherwise aborted runs record `$0.00` and the daily cost cap never sees them. |
| `sweep/task_*.go` | A task that hits the token ceiling is non-converging, not under-provisioned — every aborted run consumed exactly its ceiling at 2M, 5M and 8M, while every productive one finished under 1M. Any task that verifies in a loop must cap both its work and its full-suite runs in the prompt, and be told to ship what is already green. |
| `notify/discord.go` | `allowed_mentions.parse` must be a **non-nil** empty slice so it marshals to `[]` and not `null`; `null` re-enables `@everyone`/`@here` parsing on untrusted ticket text. |
| `notify/telegram.go` | `EscapeMarkdown` covers Telegram's strict legacy-Markdown chars. Apply it to dynamic values only, leaving the template's own `*bold*` alone — an unescaped `snake_case` title returns 400 and the notification vanishes (PR #52). |
| `linear/types.go` | The self-comment filter still matches the pre-rename `**Nightshift` prefix (ENG-204 tickets carry it). Classify by the **first non-empty line** only: a leading `>` quote is a human quoting our notice, never a system comment. |
| `github/types.go` | `RepoURL` is the discovering clone's remote URL, not gh JSON, and preserves its scheme — synthesizing HTTPS from `owner/name` breaks auto-iterate on SSH-only private repos. |
| `github/client.go` | Truncation skips UTF-8 continuation bytes (top bits `10`) so a log slice never cuts mid-rune. |
| `github/client.go` | Every `Repo:` directive ref goes through `NormalizeRepoRef` before parsing: Linear rewrites a bare URL in project content into `[url](<url>)` on save, and the raw markdown reaches `ExtractOwnerRepo` as the ref. Unwrapped, a GitHub link errors as "no repo mapping" and a non-GitHub link silently resolves to `github.com/<owner>/<name>`. |
| `telegram/listener.go` | The HTTP client timeout must exceed `pollTimeout`, or every long-poll `getUpdates` aborts client-side. |
| `state/state.go` | `Update`'s callback runs under the store lock — never call back into the `Store` from it. `OpenMigrating` imports the legacy JSON only when the DB is newly created; an existing DB is never clobbered. |
| `agent/log.go` | `usageFooterRe` is anchored to end-of-string so it never eats a mid-summary mention of token usage. |
| `agent/copilot.go` | Bridge only tokens Copilot accepts (`gho_` / `github_pat_`). It rejects classic `ghp_` PATs and reads the env token before its own store, so injecting one breaks an otherwise-valid `copilot /login`. |
| `agent/antigravity.go` | `agy`'s `--print` is a **string** flag whose value *is* the prompt: the auto-approve flag must precede it and the prompt must be the final token, or `--print` swallows the next flag. |
| `dashboard/dashboard.go` | `/fonts/` stays unauthenticated: `@font-face` `url()` subrequests don't carry the page's `?token=`, so gating them silently breaks the brand fonts. |
| `selfupdate` | Dev/snapshot builds never advertise an update — they can't be compared to a release tag. |
| `config/config.go` | `MigrateLegacyPaths` renames `~/.nightshift*` → `~/.noctra*` only when the old exists and the new doesn't; best-effort, never clobbers. |
| `cmd/noctra` | `uninstall --help` must never trigger the destructive action, and an unrecognized flag errors rather than falling through. |
| `ghauthcmd/credential.go` | `git-credential` never fails git outright: when it cannot help (not linked, not installed, non-GitHub host) it prints the reason to stderr and returns no credentials, so public repositories still clone anonymously and private ones fail with git's own auth error. It reads `--dir` instead of resolving the config dir, because git runs helpers with the worktree as cwd and the cwd-checkout override would pick the wrong directory. |
| `ghauth/session.go` | Noctra's own `gh` calls reuse a cached `write` token until 10 minutes before expiry; the git helper and agent runs always mint fresh (`FreshToken`), so a long run never inherits a token that is about to lapse. An agent's `GH_TOKEN` is fixed at spawn and lives an hour, which is why `Activate` warns when `AGENT_TIMEOUT_MINUTES` exceeds 50. |
| `ghauth/sign.go` | `CanonicalString` is a wire format shared with `noctra-auth` (`src/signing.ts`); `TestCanonicalString_MatchesTokenServiceFormat` pins a vector checked against the TypeScript implementation. Change both sides together or every signed request is refused. |
| `agent/backend.go` | `RunOptions.Env` is merged over the backend's own env (or `os.Environ()`) by key, so the agent's `GH_TOKEN` and `GIT_CONFIG_*` replace the process-wide app-mode values rather than appearing twice. |

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
