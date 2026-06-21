---
name: setup-and-config
description: Use when installing or configuring Noctra, running the setup wizard, editing .env, declaring repos via the Linear project Repo: directive, resolving config directory behavior, or preparing local, Docker, or cloud runtime config.
---

# Setup And Config Noctra

Use this playbook when installing Noctra, generating config, editing `.env`, or debugging where config is loaded from.

## Fast Local Setup

```bash
go install github.com/ahmadAlMezaal/noctra/cmd/noctra@latest
claude            # or: codex login
gh auth login
noctra setup
noctra doctor
noctra
```

The setup wizard prompts for the agent backend, Linear API key, trigger configuration, and optional Gemini and Telegram settings. It writes `.env` only — repos are declared per-project in Linear.

## Manual Setup

```bash
cp .env.example .env
```

Repos are routed per-ticket from the Linear project's description (`Repo: owner/name`, optional `Branch:`).

Then fill in the required values:

```env
LINEAR_API_KEY=...
LINEAR_TEAM_KEY=ENG
AGENT_BACKEND=claude
TRIGGER_MODE=state
TRIGGER_STATE=Next
IN_REVIEW_STATE=In Review
MAIN_BRANCH=main
```

Use `AGENT_BACKEND=codex` when running Codex instead of Claude.

## Repo Routing

Noctra chooses the target repository per-ticket from a `Repo:` directive in the ticket's Linear project description (or content body):

```
Repo: your-org/auth-service
Branch: main
```

Rules:

1. `owner/name` shorthand expands to a GitHub HTTPS URL; full `https://` / `git@` URLs are used verbatim (so SSH and non-GitHub hosts work).
2. `Branch:` is optional and falls back to the repo's default branch (then `MAIN_BRANCH`).
3. Repos are cloned on demand into `~/.noctra-repos/` unless `REPOS_BASE` overrides it.
4. Worktrees are created under `~/.noctra-worktrees/` unless `WORKTREE_BASE` overrides it.
5. If a project has no `Repo:` directive, `REPO_PATH` is used as a single-repo fallback when set.
6. If neither a directive nor `REPO_PATH` exists, Noctra skips the ticket and comments on Linear.

## Config Directory Resolution

Default per-user config lives at:

```text
~/.noctra/.env
~/.noctra/logs/
```

During development, a checkout-local config is used when the current directory contains any of:

```text
.env
.env.example
go.mod
```

This lets `go run` and local binaries use checkout files without touching `~/.noctra/`.

The relevant implementation is:

1. `resolveScriptDir()` in `cmd/noctra/main.go` for runtime config root selection.
2. `config.DefaultConfigDir()` in `internal/config` for the per-user default.

## Important Env Vars

Core:

```env
LINEAR_API_KEY=
LINEAR_TEAM_KEY=ENG
AGENT_BACKEND=claude
TRIGGER_MODE=state
TRIGGER_STATE=Next
TRIGGER_LABEL=
IN_REVIEW_STATE=In Review
REPO_PATH=
MAIN_BRANCH=main
MAX_CONCURRENT=3
POLL_INTERVAL=30
```

Safety guards:

```env
MAX_DISPATCHES=40
MAX_RETRIES=3
AGENT_TIMEOUT_MINUTES=45
```

`MAX_DISPATCHES` is a **per-UTC-day** cap: when hit, new dispatches pause and auto-resume at midnight (the process keeps running); `0` = unlimited. It's a coarse activity cap — actual token/$ spend is bounded by the budget caps (`MAX_DAILY_TOKENS` / `MAX_DAILY_USD`).

Quality controls:

```env
USE_AGENT_TEAMS=false
GEMINI_API_KEY=
GEMINI_MODEL=gemini-2.5-pro
MAX_REVIEW_RETRIES=1
```

Auto-iterate:

```env
AUTO_ITERATE_PRS=false
MAX_PR_ITERATIONS=3
PR_POLL_INTERVAL=120
TRUSTED_REVIEWERS=
```

Telegram:

```env
TELEGRAM_ENABLED=false
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHAT_ID=
VERBOSE_NOTIFICATIONS=false   # TELEGRAM_VERBOSE honored as deprecated alias
```

Container and cloud overrides:

```env
REPOS_BASE=/data/repos
WORKTREE_BASE=/data/worktrees
LOG_DIR=/data/logs
STATE_FILE=/data/state.json
GH_TOKEN=
GIT_USER_NAME=Noctra
GIT_USER_EMAIL=noctra@example.local
```

## Docker And Cloud Config

Containers should use API keys and tokens because interactive CLI login is not available:

```env
LINEAR_API_KEY=...
AGENT_BACKEND=claude
ANTHROPIC_API_KEY=...
GH_TOKEN=...
```

Declare each repo with an HTTPS URL (or `owner/name`) in its Linear project's `Repo:` directive so `GH_TOKEN` authenticates the clone. This works on every host, including PaaS that can't mount files — there's no registry file to supply.

## Preflight

After setup or config edits:

```bash
noctra doctor
```

Confirm:

1. The selected backend CLI is installed and authenticated.
2. `gh` is authenticated.
3. Linear API access works.
4. Repo URLs are reachable.
5. State or label names match Linear exactly.
