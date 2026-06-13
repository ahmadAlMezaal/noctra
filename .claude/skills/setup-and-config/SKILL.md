---
name: setup-and-config
description: Use when installing or configuring Nightshift, running the setup wizard, editing .env or repos.json, resolving config directory behavior, or preparing local, Docker, or cloud runtime config.
---

# Setup And Config Nightshift

Use this playbook when installing Nightshift, generating config, editing `.env` or `repos.json`, or debugging where config is loaded from.

## Fast Local Setup

```bash
go install github.com/ahmadAlMezaal/nightshift/cmd/nightshift@latest
claude            # or: codex login
gh auth login
nightshift setup
nightshift doctor
nightshift
```

The setup wizard prompts for the agent backend, Linear API key, trigger configuration, optional Gemini and Telegram settings, and repo mappings. It writes `.env` and `repos.json`.

## Manual Setup

```bash
cp .env.example .env
```

Repos are routed per-ticket from the Linear project's description (`Repo: owner/name`, optional `Branch:`). A hand-written `repos.json` is an optional fallback.

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

## Repo Registry

Nightshift chooses the target repository from the ticket's Linear project name.

```json
{
  "repos": {
    "Auth Service": {
      "url": "git@github.com:your-org/auth-service.git",
      "main_branch": "main"
    },
    "Web App": {
      "url": "https://github.com/your-org/web-app.git"
    }
  }
}
```

Rules:

1. The JSON key must exactly match the Linear project name.
2. `main_branch` is optional and falls back to `MAIN_BRANCH`.
3. Repos are cloned on demand into `~/.nightshift-repos/` unless `REPOS_BASE` overrides it.
4. Worktrees are created under `~/.nightshift-worktrees/` unless `WORKTREE_BASE` overrides it.
5. If no project mapping exists, `REPO_PATH` is used as a single-repo fallback when set.
6. If neither a mapping nor `REPO_PATH` exists, Nightshift skips the ticket and comments on Linear.

## Config Directory Resolution

Default per-user config lives at:

```text
~/.nightshift/.env
~/.nightshift/repos.json
~/.nightshift/logs/
```

During development, a checkout-local config is used when the current directory contains any of:

```text
.env
repos.json
.env.example
go.mod
```

This lets `go run` and local binaries use checkout files without touching `~/.nightshift/`.

The relevant implementation is:

1. `resolveScriptDir()` in `cmd/nightshift/main.go` for runtime config root selection.
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
MAX_DISPATCHES=10
MAX_RETRIES=3
AGENT_TIMEOUT_MINUTES=45
```

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
TELEGRAM_VERBOSE=false
```

Container and cloud overrides:

```env
REPOS_JSON=
REPOS_FILE=/data/repos.json
REPOS_BASE=/data/repos
WORKTREE_BASE=/data/worktrees
LOG_DIR=/data/logs
STATE_FILE=/data/state.json
GH_TOKEN=
GIT_USER_NAME=Nightshift
GIT_USER_EMAIL=nightshift@example.local
```

## Docker And Cloud Config

Containers should use API keys and tokens because interactive CLI login is not available:

```env
LINEAR_API_KEY=...
AGENT_BACKEND=claude
ANTHROPIC_API_KEY=...
GH_TOKEN=...
```

Use HTTPS repo URLs with `GH_TOKEN` in `repos.json` or `REPOS_JSON`.

For PaaS hosts that cannot mount files, set `REPOS_JSON` inline:

```bash
REPOS_JSON='{"repos":{"My Project":{"url":"https://github.com/you/repo.git"}}}'
```

## Preflight

After setup or config edits:

```bash
nightshift doctor
```

Confirm:

1. The selected backend CLI is installed and authenticated.
2. `gh` is authenticated.
3. Linear API access works.
4. Repo URLs are reachable.
5. State or label names match Linear exactly.
