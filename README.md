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

Nightshift is a single Go binary. Beyond Go for the build, it shells out to a few standard tools at runtime:

| Tool | Install | Purpose |
|------|---------|---------|
| Go 1.23+ | [go.dev/dl](https://go.dev/dl), or `brew install go` / `apt install golang` | Build the binary |
| `claude` CLI | [Claude Code docs](https://docs.anthropic.com/en/docs/claude-code) | AI implementation engine |
| `gh` CLI | `brew install gh` | PR creation |
| `git` | Pre-installed | Worktrees + clone-on-demand |
| Linear API key | [Linear settings → API](https://linear.app/settings/api) | Ticket management |
| Gemini API key | [Google AI Studio](https://aistudio.google.com/apikey) | Optional review gate |

**Authentication:**

```bash
claude          # authenticate Claude Code
gh auth login   # authenticate gh
```

---

## Setup

```bash
# 1. Clone and build
git clone https://github.com/ahmadAlMezaal/nightshift.git
cd nightshift
go build -o nightshift ./cmd/nightshift

# 2. Run the interactive setup wizard
#    Prompts for Linear, optional Gemini/Telegram, and your repos —
#    then generates .env and repos.json for you.
./nightshift setup

# 3. Start the poll loop
./nightshift
```

That's it. Move tickets to your trigger state (default: "Next") and watch them become PRs.

Prefer editing config by hand? Copy `.env.example` → `.env` and `repos.example.json` → `repos.json` instead of running the wizard.

### Cross-compile for the Raspberry Pi

```bash
# Pi 4 / 5 (64-bit OS)
GOOS=linux GOARCH=arm64 go build -o nightshift ./cmd/nightshift

# Pi 3 or older / 32-bit OS
GOOS=linux GOARCH=arm GOARM=7 go build -o nightshift ./cmd/nightshift

scp nightshift pi@your-pi:/srv/nightshift/
```

Then update your cron to point at the binary instead of the old shell script.

---

## Repositories

Nightshift picks the target repo **per-ticket**, from the ticket's Linear **project** — so you never have to edit config to switch projects.

`repos.json` maps each Linear project name to a git repo:

```json
{
  "repos": {
    "Auth Service": { "url": "git@github.com:your-org/auth-service.git", "main_branch": "main" },
    "Web App":      { "url": "https://github.com/your-org/web-app.git" }
  }
}
```

When a ticket comes in, Nightshift reads its project, finds the matching repo, and **clones it on demand** into `~/.nightshift-repos/` (nothing needs to be cloned up front). `main_branch` is optional and falls back to `MAIN_BRANCH` in `.env`.

- **Register a repo once**, reference it forever — no per-session `.env` edits.
- `repos.json` is gitignored (machine-specific). The wizard generates it; `repos.example.json` is the template.
- **Auth:** the host running Nightshift needs git access to each repo — an SSH key, or `gh auth login` for HTTPS URLs. The setup wizard checks access (`git ls-remote`) before saving a repo, and Nightshift re-checks before cloning.
- **Fallback:** a ticket whose project has no `repos.json` entry uses `REPO_PATH` from `.env` if set; otherwise it's skipped with a Linear comment. Single-repo `.env`-only setups keep working unchanged.

---

## Configuration

Run `./nightshift setup` to generate config, or copy `.env.example` → `.env` and `repos.example.json` → `repos.json` by hand.

| Variable | Default | Description |
|----------|---------|-------------|
| `LINEAR_API_KEY` | *(required)* | Your Linear personal API key |
| `LINEAR_TEAM_KEY` | `ENG` | Your Linear team identifier (the prefix before ticket numbers, e.g., `ENG` for `ENG-42`) |
| `TRIGGER_STATE` | `Next` | Linear column to watch for new work |
| `IN_PROGRESS_STATE` | `In Progress` | State set when Nightshift picks up a ticket |
| `IN_REVIEW_STATE` | `In Review` | State set after PR is created |
| `REPO_PATH` | *(empty)* | Optional fallback repo for tickets whose Linear project is not in `repos.json` |
| `MAIN_BRANCH` | `main` | Default base branch (repos.json entries may override per-repo) |
| `MAX_CONCURRENT` | `3` | Maximum tickets processed simultaneously |
| `POLL_INTERVAL` | `30` | Seconds between Linear polls |
| `USE_AGENT_TEAMS` | `false` | Enable Claude Code Agent Teams (multi-agent parallelism) |
| `GEMINI_API_KEY` | *(empty)* | Gemini API key — leave empty to skip the review gate |
| `GEMINI_MODEL` | `gemini-2.5-pro` | Gemini model to use for code review |
| `MAX_REVIEW_RETRIES` | `1` | Fix passes Claude gets after Gemini flags issues |

State names are **case-sensitive** and must match your Linear board exactly.

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
  -v $HOME/.nightshift-repos:/root/.nightshift-repos \
  -v $HOME/.nightshift-worktrees:/root/.nightshift-worktrees \
  -v $(pwd):/srv/nightshift \
  -w /srv/nightshift \
  -e LINEAR_API_KEY=... \
  ubuntu:22.04 \
  /srv/nightshift/nightshift
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

Yes — that's built in. Register each repo in `repos.json` (mapped to its Linear project) and a single Nightshift instance routes every ticket to the right repo automatically. Tickets for different repos run concurrently up to `MAX_CONCURRENT`. See [Repositories](#repositories).

### What if Claude gets stuck?

Claude will output `BLOCKED: <reason>` and stop. Nightshift detects this, posts a comment on the Linear ticket explaining the blocker, and moves the ticket back to your trigger state. Reply to the ticket with more context and re-queue it.

### What are Agent Teams?

Claude Code Agent Teams is an experimental feature where a lead Claude agent spawns and coordinates teammate agents that work in parallel. The lead agent plans the approach, delegates subtasks (core implementation, tests, review), and synthesizes the results. It can significantly speed up complex tickets.

Enable with `USE_AGENT_TEAMS=true` in `.env` (disabled by default). Requires a recent version of the `claude` CLI.

### Why Gemini for review and not another Claude instance?

Different model = different blind spots. If Claude's implementation missed something, Claude reviewing itself is likely to miss the same thing — the same training and architecture produces the same failure modes. Gemini's different architecture catches different categories of issues, making the review genuinely additive rather than redundant.

### What if I don't have a Gemini key?

No problem — leave `GEMINI_API_KEY` empty and Nightshift skips the review gate entirely. Your tickets still get implemented and PRs get created. You can add the key later without any other changes.

### The agent crashed mid-task. How do I clean up?

```bash
./nightshift cleanup           # interactive — pick what to remove
./nightshift cleanup --force   # non-interactive — remove everything stale
```

Cleanup iterates every registered repo, deletes merged branches, force-deletes unmerged `nightshift/*` branches that don't have an open PR, prunes worktrees, and clears agent logs older than 7 days.

---

## Credits

Built with:
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) by Anthropic — the AI implementation engine
- [Claude Code Agent Teams](https://docs.anthropic.com/en/docs/claude-code/agent-teams) — multi-agent parallelism
- [Gemini](https://aistudio.google.com) by Google — independent code review
- Inspired by Damian Galarza's agent loop patterns and the broader Claude Code automation community

---

*MIT License — use freely, fork boldly, sleep soundly.*
