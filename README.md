# 🌙 Nightshift

> Move tickets to Next. Go to sleep. Wake up to PRs.

Nightshift picks up your Linear tickets, implements them with Claude Code Agent Teams, and creates PRs — all while you sleep.

---

## How it works

```
You: Move 3 tickets to "Next" → Run nightshift → Go to sleep

Nightshift:
  1. Polls Linear, finds tickets in "Next"
  2. Creates isolated git worktree per ticket
  3. Spins up Claude Code with Agent Teams:
     → Lead agent reads the ticket and plans the approach
     → Teammate agents implement different parts in parallel
     → Code is reviewed before committing
  4. (If Gemini key provided) Multi-model review gate:
     → Sends diff + ticket to Gemini for independent review
     → If issues found → Claude gets a fix pass → Gemini re-reviews
  5. Pushes branch, creates PR via gh CLI
  6. Moves ticket to "In Review", comments PR link on Linear
  7. Picks up next ticket

You: Wake up → Review 3 PRs → Merge
```

---

## Prerequisites

Before running Nightshift, make sure you have:

| Tool | Install | Purpose |
|------|---------|---------|
| `claude` CLI | [Claude Code docs](https://docs.anthropic.com/en/docs/claude-code) | AI implementation engine |
| `gh` CLI | `brew install gh` | PR creation |
| `curl` | Pre-installed on macOS/Linux | API calls |
| `jq` | `brew install jq` | JSON parsing |
| `git` | Pre-installed | Worktree management |
| `bash 4+` | `brew install bash` | Script runtime (macOS ships with bash 3.2) |
| Linear API key | [Linear settings → API](https://linear.app/settings/api) | Ticket management |
| Gemini API key | [Google AI Studio](https://aistudio.google.com/apikey) | Optional review gate |

**Authentication:**

```bash
# Authenticate Claude Code
claude

# Authenticate gh CLI
gh auth login
```

---

## Setup

```bash
# 1. Clone the repo
git clone https://github.com/your-org/nightshift.git
cd nightshift

# 2. Copy the config template
cp .env.example .env

# 3. Edit .env with your values
open .env   # or: nano .env, vim .env

# 4. Make the script executable (already done if you cloned)
chmod +x nightshift.sh

# 5. Run
./nightshift.sh
```

That's it. Move tickets to your trigger state (default: "Next") and watch them become PRs.

---

## Configuration

All config lives in `.env`. Copy `.env.example` to get started.

| Variable | Default | Description |
|----------|---------|-------------|
| `LINEAR_API_KEY` | *(required)* | Your Linear personal API key |
| `LINEAR_TEAM_KEY` | `ENG` | Your Linear team identifier (the prefix before ticket numbers, e.g., `ENG` for `ENG-42`) |
| `TRIGGER_STATE` | `Next` | Linear column to watch for new work |
| `IN_PROGRESS_STATE` | `In Progress` | State set when Nightshift picks up a ticket |
| `IN_REVIEW_STATE` | `In Review` | State set after PR is created |
| `REPO_PATH` | *(required\*)* | Absolute path to the default git repository |
| `MAIN_BRANCH` | `main` | Base branch for new PRs |
| `REPOS_FILE` | `repos.json` | Multi-repo map — route tickets to repos with `repo:<key>` labels |
| `MAX_CONCURRENT` | `3` | Maximum tickets processed simultaneously |
| `POLL_INTERVAL` | `30` | Seconds between Linear polls |
| `USE_AGENT_TEAMS` | `false` | Enable Claude Code Agent Teams (multi-agent parallelism) |
| `GEMINI_API_KEY` | *(empty)* | Gemini API key — leave empty to skip the review gate |
| `GEMINI_MODEL` | `gemini-2.5-pro` | Gemini model to use for code review |
| `MAX_REVIEW_RETRIES` | `1` | Fix passes Claude gets after Gemini flags issues |

State names are **case-sensitive** and must match your Linear board exactly.

\* `REPO_PATH` is required unless `repos.json` defines at least one repo — see below.

---

## Working on multiple repos

By default Nightshift works on the single repo at `REPO_PATH`. To let one
Nightshift instance serve **any** repo — without editing `.env` when you switch
projects — use a repos file and Linear labels.

1. Copy `repos.example.json` to `repos.json` and map a short key to each repo:

   ```json
   {
     "my-app":       { "url": "git@github.com:you/my-app.git",       "branch": "main" },
     "side-project": { "url": "https://github.com/you/side-project.git", "branch": "master" }
   }
   ```

2. In Linear, create a label named `repo:my-app` (matching a key above) and add
   it to a ticket. When you move that ticket to your trigger state, Nightshift
   reads the label, clones the repo on demand into `~/.nightshift-repos/`, and
   works there.

Tickets **without** a `repo:` label fall back to `REPO_PATH`, so existing
single-repo setups keep working unchanged. The polling/cron trigger is
unchanged — only the repo each ticket targets is now decided per ticket.

Notes:
- The `branch` field is optional and defaults to `MAIN_BRANCH`.
- Repo keys are case-sensitive and must match the label exactly after `repo:`.
- A repo is cloned once and reused; later tickets just fetch the latest base branch.

---

## Modes

### Single agent *(default)*

One Claude session per ticket, no team coordination. Fastest, cheapest, and works on any platform including Linux/Raspberry Pi.

**Best for:** simple bug fixes, small isolated changes, getting started.

```env
USE_AGENT_TEAMS=false
GEMINI_API_KEY=   # leave empty
```

---

### Agent Teams + Gemini review

Claude implements the ticket with a coordinated team of agents. Gemini independently reviews the diff before the PR is created. Claude gets a fix pass if issues are found.

**Best for:** production repos, tickets with clear acceptance criteria, when you want the highest quality output.

```env
USE_AGENT_TEAMS=true
GEMINI_API_KEY=your_key_here
```

### Agent Teams only

Claude implements with a team but skips the external review step.

**Best for:** when you trust Claude's output, want faster runs, or don't have a Gemini key.

```env
USE_AGENT_TEAMS=true
GEMINI_API_KEY=   # leave empty
```

---

## The Gemini Review Gate

### Why a second model?

The implementing model has blind spots reviewing its own work — it tends to miss the same things it missed during implementation. A different model architecture catches different categories of bugs, security issues, and requirement gaps.

### What it does

After Claude finishes implementation, Nightshift takes the `git diff` and the original ticket description and sends them to Gemini with a structured review prompt. Gemini responds with a `VERDICT: PASS` or `VERDICT: FAIL` followed by review comments.

### What happens on FAIL

The review feedback is given back to Claude with instructions to fix the specific issues. Claude gets `MAX_REVIEW_RETRIES` fix passes. After each pass, Gemini re-reviews the updated diff.

If Gemini still hasn't passed after all retries, the PR is created anyway — but the body includes the unresolved review comments so you know exactly what to check before merging.

### Cost

The Gemini review gate uses the Gemini API (not a subscription). A typical diff review uses roughly 10,000–50,000 tokens depending on diff size.

With `gemini-2.5-pro` pricing, a review costs approximately **$0.01–$0.05**. Very cheap for what you get.

### Disabling the review gate

Leave `GEMINI_API_KEY` empty and Nightshift skips the review gate entirely. You can add it later — no other config changes required.

---

## Linear State Flow

```
[Next] ──────────→ [In Progress] ──────────→ [In Review] ──────→ [Done]
  ↑                     │                          │
  │    BLOCKED or        │   PR created             │   You merge
  └────← no changes ←───┘                          └──────────────→
```

- **Next** → Nightshift picks up the ticket
- **In Progress** → Claude is working on it
- **In Review** → PR created, waiting for your review
- **Done** → You merge the PR (Nightshift doesn't touch this)

If Claude gets stuck or makes no changes, the ticket is moved back to **Next** with a comment explaining why.

---

## Writing Good Tickets

Nightshift is only as good as your tickets. See [docs/WRITING-GOOD-TICKETS.md](docs/WRITING-GOOD-TICKETS.md) for a full guide.

**The one-line rule:** Claude needs to know *what* to change, *where* to change it, and *how you'll know it's done*.

**Good ticket:**
> Login endpoint returns 500 when refresh token is expired. Should return 401 and clear the session cookie. See `auth.controller.ts` line 42. Tests in `auth.controller.spec.ts`. Acceptance: existing tests pass, new test covers the expired token case.

**Bad ticket:**
> Fix the auth bug

---

## Security

### The `--dangerously-skip-permissions` flag

Nightshift runs `claude` with `--dangerously-skip-permissions`. This allows Claude to read, write, and execute commands in your repository without asking for confirmation on each action.

**Only use Nightshift on repositories where you accept this risk:**
- ✅ Personal projects and side projects
- ✅ Dedicated feature branches on team repos (with PR review before merge)
- ❌ Repos with secrets or credentials checked in
- ❌ Repos connected to production infrastructure with write access

### Running in a container

For extra safety, run Nightshift in a Docker container with your repo mounted:

```bash
docker run -it \
  -v /path/to/your/repo:/repo \
  -v /path/to/nightshift:/nightshift \
  -e LINEAR_API_KEY=... \
  ubuntu:22.04 \
  bash /nightshift/nightshift.sh
```

### Gemini API note

If `GEMINI_API_KEY` is configured, your git diffs and ticket descriptions are sent to Google's Gemini API. Do not use the review gate on repositories containing secrets, proprietary algorithms, or other sensitive intellectual property.

---

## FAQ

### Is this safe to run on my production repo?

Nightshift creates PRs — it doesn't merge them. You review and merge manually. The risk is in what Claude writes during implementation, not in what Nightshift does with it. Use PR review as your safety gate. For extra isolation, run in a container.

### How much does it cost?

**Claude:** Nightshift uses the `claude` CLI, which runs on your Claude Code subscription (not the API). Agent Teams mode uses more tokens per ticket since multiple agents are active simultaneously.

**Gemini:** Uses the Gemini API with pay-per-token pricing. Reviews are cheap — approximately $0.01–$0.05 per ticket with `gemini-2.5-pro`. You can check current pricing at [Google AI Studio](https://aistudio.google.com/pricing).

### Can I run multiple repos simultaneously?

Yes — a single Nightshift instance can serve any number of repos. Create a
`repos.json` and add a `repo:<key>` label to each ticket. See
[Working on multiple repos](#working-on-multiple-repos). Tickets for different
repos are processed concurrently, each in its own worktree, up to
`MAX_CONCURRENT`.

### What if Claude gets stuck?

Claude will output `BLOCKED: <reason>` and stop. Nightshift detects this, posts a comment on the Linear ticket explaining the blocker, and moves the ticket back to your trigger state. Reply to the ticket with more context and re-queue it.

### What are Agent Teams?

Claude Code Agent Teams is an experimental feature where a lead Claude agent spawns and coordinates teammate agents that work in parallel. The lead agent plans the approach, delegates subtasks (core implementation, tests, review), and synthesizes the results. It can significantly speed up complex tickets.

Enable with `USE_AGENT_TEAMS=true` in `.env` (disabled by default). Requires a recent version of the `claude` CLI.

### Why Gemini for review and not another Claude instance?

Different model = different blind spots. If Claude's implementation missed something, Claude reviewing itself is likely to miss the same thing — the same training and architecture produces the same failure modes. Gemini's different architecture catches different categories of issues, making the review genuinely additive rather than redundant.

### What if I don't have a Gemini key?

No problem — leave `GEMINI_API_KEY` empty and Nightshift skips the review gate entirely. Your tickets still get implemented and PRs get created. You can add the key later without any other changes.

### The script crashed mid-task. How do I clean up?

```bash
# List all worktrees
cd /path/to/your/repo && git worktree list

# Remove a stale worktree
git worktree remove .nightshift-worktrees/ENG-42 --force

# Or clean up all nightshift worktrees
git worktree list | grep nightshift | awk '{print $1}' | xargs -I{} git worktree remove {} --force
```

---

## Credits

Built with:
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) by Anthropic — the AI implementation engine
- [Claude Code Agent Teams](https://docs.anthropic.com/en/docs/claude-code/agent-teams) — multi-agent parallelism
- [Gemini](https://aistudio.google.com) by Google — independent code review
- Inspired by Damian Galarza's agent loop patterns and the broader Claude Code automation community

---

*MIT License — use freely, fork boldly, sleep soundly.*
