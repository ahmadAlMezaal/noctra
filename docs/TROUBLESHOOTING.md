# Troubleshooting Nightshift

---

## Tickets not being picked up

**Symptom:** Nightshift is running, polling, but no tickets are dispatched.

**Check 1: State name mismatch**

State names are case-sensitive and must match your Linear board exactly.

```bash
# Check what states are available by looking at the startup error output,
# or query directly:
curl -s -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: $LINEAR_API_KEY" \
  --data '{"query":"{ team(key: \"ENG\") { states { nodes { name } } } }"}' \
  https://api.linear.app/graphql | jq '.data.team.states.nodes[].name'
```

Common mismatches:
- `"next"` vs `"Next"` (capitalization)
- `"In review"` vs `"In Review"` (capitalization)
- Custom state names that differ from the defaults

Fix: update `TRIGGER_STATE` / `IN_PROGRESS_STATE` / `IN_REVIEW_STATE` in `.env` to match exactly.

**Check 2: Team key mismatch**

`LINEAR_TEAM_KEY` must match the prefix before your ticket numbers. If your tickets are `BACKEND-42`, use `BACKEND`, not `ENG`.

**Check 3: API key permissions**

Your Linear API key needs read/write access to issues and comments. Personal API keys have full access to the workspaces you're a member of by default.

---

## Claude not starting

**Symptom:** Nightshift dispatches a ticket but the log file is empty or missing.

**Check Claude CLI is installed:**

```bash
claude --version
```

If command not found, install Claude Code: [docs.anthropic.com/en/docs/claude-code](https://docs.anthropic.com/en/docs/claude-code)

**Check Claude authentication:**

```bash
claude
```

This opens an interactive session. If it prompts for authentication, complete it. Nightshift uses the same credentials.

**Check the log file:**

```bash
cat .agent-logs/ENG-42.log
```

Any startup errors from `claude` will appear there.

---

## Agent Teams not working

**Symptom:** `USE_AGENT_TEAMS=true` is set but it seems like only one agent is running.

**Check you're on a recent Claude Code version:**

```bash
claude --version
# Make sure it's a version that supports Agent Teams
```

Update if needed:

```bash
# macOS / Linux
curl -fsSL https://claude.ai/install.sh | sh
```

**Check the environment variable is being passed:**

Agent Teams is enabled via the `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` environment variable. Nightshift sets this automatically when `USE_AGENT_TEAMS=true`. Verify in `.env`:

```env
USE_AGENT_TEAMS=true
```

Note the value must be exactly `true` (lowercase string), not `1` or `TRUE`.

**Check the log for confirmation:**

The log line `Running Claude (agent-teams=true)` confirms the env var is being passed.

---

## Gemini review always fails

**Symptom:** Every PR comes through with "did not pass" and Claude's fix passes aren't helping.

**Check your API key:**

```bash
curl -s \
  "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent?key=$GEMINI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"parts":[{"text":"Say hello"}]}]}' | jq .
```

If you see an error about `API_KEY_INVALID`, regenerate your key at [aistudio.google.com/apikey](https://aistudio.google.com/apikey).

**Check the Gemini log:**

```bash
cat .agent-logs/ENG-42-gemini.log
```

This shows the raw Gemini response. If it says `ERROR: Could not parse Gemini response`, the API call is failing entirely. If it shows a FAIL verdict with comments, the review is working but the code has real issues.

**The diff might be too large:**

If the diff exceeds Gemini's context window, you'll get a parsing error. Try keeping tickets smaller in scope, or switch to `gemini-2.5-pro` (highest context).

---

## Gemini review always passes

**Symptom:** Gemini gives PASS on everything, even obviously wrong implementations.

The prompt uses `temperature: 0.1` to make Gemini deterministic and strict. If it's still too lenient, you can tighten the review prompt in `nightshift.sh` by adding more specific criteria to the `review_prompt` variable in the `gemini_review()` function.

Alternatively, increase `MAX_REVIEW_RETRIES` so Claude has more rounds of critique even on a PASS.

---

## PR creation fails

**Symptom:** Ticket reaches the commit stage, branch is pushed, but PR creation fails.

**Check gh authentication:**

```bash
gh auth status
```

If not logged in:

```bash
gh auth login
```

**Check push access:**

```bash
cd /path/to/your/repo
git push origin test-nightshift-access 2>&1
git push origin --delete test-nightshift-access 2>&1
```

**Check the log:**

```bash
cat .agent-logs/ENG-42.log
```

The PR creation error is logged there. Common causes:
- Branch already exists on remote from a previous run
- No write access to the repository
- `gh` is authenticated but not to the correct GitHub account

**Branch already exists:**

```bash
git push origin --delete nightshift/eng-42
```

---

## Changes don't make sense / wrong files modified

**Symptom:** The PR touches files unrelated to the ticket, or the implementation is completely off.

**Root cause:** The ticket was too vague.

Claude made its best guess about what was needed. Without specific file paths, error messages, or acceptance criteria, it may have implemented the wrong thing entirely.

**Fix:** Improve the ticket. See [WRITING-GOOD-TICKETS.md](WRITING-GOOD-TICKETS.md).

Close the PR, delete the branch, and re-queue the ticket with more context:

```bash
# Delete the branch
git push origin --delete nightshift/eng-42
cd /path/to/repo && git branch -D nightshift/eng-42 2>/dev/null || true
```

---

## Script crashes mid-task

**Symptom:** Nightshift exits unexpectedly. A ticket is stuck in "In Progress" with no PR.

**Clean up stale worktrees:**

```bash
# List all worktrees
cd /path/to/your/repo && git worktree list

# Remove a specific stale worktree
git worktree remove ~/.nightshift-worktrees/ENG-42 --force

# Remove all nightshift worktrees at once
git worktree list \
  | grep nightshift \
  | awk '{print $1}' \
  | xargs -I{} git worktree remove {} --force 2>/dev/null || true
```

**Reset the ticket state:**

Move the ticket back to your trigger state manually in Linear, or via the API:

```bash
# Get state ID for "Next"
curl -s -X POST \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"query":"{ team(key:\"ENG\") { states { nodes { id name } } } }"}' \
  https://api.linear.app/graphql | jq '.data.team.states.nodes[] | select(.name=="Next")'
```

---

## How to check what happened

**View the full session log:**

```bash
cat .agent-logs/ENG-42.log
```

**View the Gemini review log:**

```bash
cat .agent-logs/ENG-42-gemini.log
```

**View all logs sorted by most recent:**

```bash
ls -lt .agent-logs/*.log | head -10
```

**Follow logs in real-time while Nightshift runs:**

```bash
# In a second terminal
tail -f .agent-logs/ENG-42.log
```

---

## Port/network issues

Nightshift does not run a server, does not need a port, and does not require ngrok or any webhook setup. It works entirely by polling Linear's GraphQL API over HTTPS. No inbound connections are ever needed.

---

## "bash: declare: -A: invalid option"

You're running bash 3.2 (macOS default). Nightshift requires bash 4+.

```bash
# Install bash 4+ via Homebrew
brew install bash

# Run with the new bash explicitly
/opt/homebrew/bin/bash nightshift.sh

# Or update your PATH and use the shebang
which bash  # should show /opt/homebrew/bin/bash
```

---

## Still stuck?

1. Check `.agent-logs/<TICKET>.log` first — 90% of issues are explained there
2. Run `claude` interactively to confirm authentication
3. Run `gh auth status` to confirm GitHub authentication
4. Check Linear API key permissions at [linear.app/settings/api](https://linear.app/settings/api)
5. Open an issue at [github.com/your-org/nightshift/issues](https://github.com/your-org/nightshift/issues) with the relevant log excerpt
