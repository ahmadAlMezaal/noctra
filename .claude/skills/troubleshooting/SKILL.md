---
name: troubleshooting
description: Use when Noctra fails to pick up tickets, run an agent, create PRs, pass review, iterate on feedback, or when logs/worktrees/branches need diagnosis or cleanup.
---

# Troubleshooting Noctra

Use this runbook when Noctra behaves unexpectedly. Prefer `noctra doctor`, service logs, and per-ticket agent logs before changing code.

## First Checks

1. Run `noctra doctor` from the same host and config context as the service.
2. Check the startup banner in service logs; it prints the resolved backend, trigger mode, states or label, repo routing, review gate, auto-iterate, and notifications.
3. Inspect the per-ticket transcript under the configured log directory:

```bash
make tail TICKET=ENG-42
# or
tail -f logs/ENG-42.log
```

Config defaults to `~/.noctra/` for `.env` and `logs/`, but a checkout containing `.env`, `.env.example`, or `go.mod` uses the current directory.

## Tickets Are Not Picked Up

Check the trigger mode first:

```env
TRIGGER_MODE=state
TRIGGER_STATE=Next
IN_REVIEW_STATE=In Review
```

or:

```env
TRIGGER_MODE=label
TRIGGER_LABEL=noctra
IN_REVIEW_STATE=In Review
```

Then verify:

1. `LINEAR_TEAM_KEY` matches the ticket prefix, such as `ENG` for `ENG-42`.
2. State and label names match Linear exactly; names are case-sensitive.
3. The Linear API key has workspace access to read issues and write comments/state changes.
4. The ticket's Linear project has a `Repo:` directive in its description, or `REPO_PATH` is set as a fallback.

Query Linear states directly when names are unclear:

```bash
curl -s -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: $LINEAR_API_KEY" \
  --data '{"query":"{ team(key: \"ENG\") { states { nodes { name } } } }"}' \
  https://api.linear.app/graphql | jq '.data.team.states.nodes[].name'
```

## Wrong Repo Or No Changes

Noctra routes each ticket to the repo named in its Linear project's `Repo:` directive. Confirm the project description (or content body) contains a line like:

```
Repo: your-org/web-app
Branch: main
```

`owner/name` expands to a GitHub HTTPS URL; full `https://` / `git@` URLs work verbatim. `Branch:` is optional (defaults to the repo's default branch). If the directive is missing, Noctra falls back to `REPO_PATH` when present. If neither exists, it skips the ticket with a Linear comment.

If the agent changed irrelevant files, improve the ticket with the `writing-good-tickets` skill and requeue it with specific files, errors, and acceptance criteria.

## Agent Does Not Start

Check the selected backend:

```bash
grep '^AGENT_BACKEND=' .env
```

For Claude:

```bash
claude --version
claude
```

For Codex:

```bash
codex --version
codex login
```

Noctra inherits CLI auth from the host unless API keys are supplied. In containers, use API-key auth instead of interactive login:

```env
ANTHROPIC_API_KEY=...
OPENAI_API_KEY=...
```

## Agent Teams Is Not Active

Agent Teams is Claude-only. Verify:

```env
AGENT_BACKEND=claude
USE_AGENT_TEAMS=true
```

The value must be exactly lowercase `true`. Check the ticket log for the backend run header and use a recent `claude` CLI.

## Gemini Review Fails Or Seems Wrong

If every review errors or fails before useful comments appear:

1. Confirm `GEMINI_API_KEY` is set only when the review gate is desired.
2. Validate the key with a direct API call.
3. Check `logs/ENG-42-gemini.log` for the raw response.
4. If the diff is huge, split the ticket or reduce scope.

```bash
curl -s \
  "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent?key=$GEMINI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"parts":[{"text":"Say hello"}]}]}' | jq .
```

Gemini returning `PASS` on weak work usually means the review prompt or ticket acceptance criteria are too broad. Tighten the ticket and consider increasing `MAX_REVIEW_RETRIES`.

## PR Creation Fails

Check GitHub auth and repo write access:

```bash
gh auth status
git ls-remote origin
```

For HTTPS remotes in containers, provide `GH_TOKEN`; the entrypoint wires it into `gh` and git.

Common fixes:

1. Log in as the GitHub account with repo write access.
2. Delete a stale remote branch if a prior run left one behind:

```bash
git push origin --delete noctra/eng-42
```

3. Check the ticket log for the exact `gh pr create` error.

## Auto-Iterate Does Not React

Verify:

```env
AUTO_ITERATE_PRS=true
MAX_PR_ITERATIONS=3
PR_POLL_INTERVAL=120
TRUSTED_REVIEWERS=
```

Noctra only watches PRs it authored, identified by the `noctra/<id>` branch prefix. It ignores bot feedback unless the bot login is in `TRUSTED_REVIEWERS`. CI failures are keyed by head commit SHA and are acted on once per failing commit.

The restart-safe cursor is stored in `~/.noctra-state.json` unless `STATE_FILE` overrides it.

## Cleanup After A Crash Or Bad Run

Prefer the built-in cleanup command:

```bash
./noctra cleanup
./noctra cleanup --force
```

Manual cleanup, when needed:

```bash
cd /path/to/repo
git worktree remove --force ~/.noctra-worktrees/ENG-42
git worktree prune
git branch -D noctra/eng-42 2>/dev/null || true
git push origin --delete noctra/eng-42
```

Move the Linear ticket back to the trigger state, or requeue it from Telegram:

```text
/requeue ENG-42 More context for the next attempt.
```

## Useful Logs

```bash
make logs                    # systemd service log
make watch                   # most recent agent transcript
make tail TICKET=ENG-42      # one ticket transcript
ls -lt logs/*.log | head     # recent local checkout logs
```

Noctra has no inbound server, webhook, ngrok tunnel, or port requirement. It polls Linear and GitHub over outbound HTTPS.

