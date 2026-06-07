# 🌙 Nightshift

> Move tickets to Next. Go to sleep. Wake up to PRs.

Nightshift picks up your Linear tickets, implements them with your coding agent of choice — **Claude Code or OpenAI Codex** — and creates PRs, all while you sleep. Iterate on review feedback and CI failures, and drive the whole thing from Telegram.

---

## How it works

```
You: Move 3 tickets to "Next" → Run nightshift → Go to sleep

Nightshift:
  1. Polls Linear, finds tickets in "Next" (or carrying a trigger label)
  2. Creates an isolated git worktree per ticket
  3. Dispatches your agent backend (Claude Code or OpenAI Codex):
     → Reads the ticket, plans, implements, and self-reviews
     → (Claude + USE_AGENT_TEAMS) a lead agent delegates to teammates in parallel
  4. (If Gemini key provided) Multi-model review gate:
     → Sends diff + ticket to Gemini for independent review
     → If issues found → the agent gets a fix pass → Gemini re-reviews
  5. Pushes branch, creates PR via gh CLI
  6. Moves ticket to "In Review", comments PR link on Linear
  7. Picks up next ticket — and (optionally) keeps iterating on PR feedback + CI

You: Wake up → Review 3 PRs → Merge   (check on it from Telegram any time)
```

---

## Prerequisites

Nightshift is a single Go binary. Beyond Go for the build, it shells out to a few standard tools at runtime:

| Tool | Install | Purpose |
|------|---------|---------|
| Go 1.23+ | [go.dev/dl](https://go.dev/dl), or `brew install go` / `apt install golang` | Build the binary |
| `claude` **or** `codex` CLI | [Claude Code docs](https://docs.anthropic.com/en/docs/claude-code) (Claude) or `npm i -g @openai/codex` (Codex) | Implementation engine — pick one via `AGENT_BACKEND` (default `claude`) |
| `gh` CLI | `brew install gh` | PR creation |
| `git` | Pre-installed | Worktrees + clone-on-demand |
| Linear API key | [Linear settings → API](https://linear.app/settings/api) | Ticket management |
| Gemini API key | [Google AI Studio](https://aistudio.google.com/apikey) | Optional review gate |

You only need the CLI for the backend you select with `AGENT_BACKEND` — `claude` (default) or `codex`. `nightshift doctor` checks for the right one.

**Authentication:**

```bash
# Authenticate whichever agent backend you use (one-time, on the host):
claude              # Claude Code  (subscription login, or set ANTHROPIC_API_KEY)
codex login         # OpenAI Codex (subscription login, or set OPENAI_API_KEY)

gh auth login       # authenticate gh
```

Nightshift never stores or manages agent credentials — it inherits whatever the selected CLI is already logged into.

---

## Install

Nightshift is a single static binary — pick whichever you prefer:

```bash
# A. Go toolchain (installs the latest tagged release to $GOPATH/bin)
go install github.com/ahmadAlMezaal/nightshift/cmd/nightshift@latest

# B. Prebuilt binary — no Go required
#    Grab the archive for your OS/arch from the Releases page:
#    https://github.com/ahmadAlMezaal/nightshift/releases
#    (linux amd64/arm64/armv7, macOS amd64/arm64), then:
tar -xzf nightshift_*_linux_arm64.tar.gz && sudo mv nightshift /usr/local/bin/

# C. Build from source
git clone https://github.com/ahmadAlMezaal/nightshift.git
cd nightshift && go build -o nightshift ./cmd/nightshift
```

Run `nightshift version` to confirm the build.

## Setup

```bash
# 1. With nightshift on your PATH (or ./nightshift if built from source)

# 2. Run the interactive setup wizard
#    Prompts for the agent backend (Claude/Codex), Linear,
#    optional Gemini/Telegram, and your repos —
#    then generates .env and repos.json for you.
./nightshift setup

# 3. Start the poll loop
./nightshift
```

That's it. Move tickets to your trigger state (default: "Next") — or set `TRIGGER_MODE=label` to pick up any ticket carrying a label instead — and watch them become PRs.

Prefer editing config by hand? Copy `.env.example` → `.env` and `repos.example.json` → `repos.json` instead of running the wizard.

### Raspberry Pi

Easiest: download the prebuilt **`linux_arm64`** (Pi 4 / 5, 64-bit OS) or **`linux_armv7`** (Pi 3 / 32-bit OS) archive from the [Releases page](https://github.com/ahmadAlMezaal/nightshift/releases) — no Go toolchain on the Pi needed.

Prefer to cross-compile yourself:

```bash
GOOS=linux GOARCH=arm64 go build -o nightshift ./cmd/nightshift            # Pi 4 / 5
GOOS=linux GOARCH=arm GOARM=7 go build -o nightshift ./cmd/nightshift      # Pi 3 / 32-bit
scp nightshift pi@your-pi:/srv/nightshift/
```

### Cutting a release

Releases are automated by [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml` + `.github/workflows/release.yml`). Push a semver tag and a GitHub Release with cross-compiled archives + checksums is published automatically:

```bash
git tag v2.0.0 && git push origin v2.0.0
```

---

## Operating the service

Running Nightshift as a long-lived `systemd --user` service? The `Makefile` wraps the day-to-day operations so you don't have to remember the raw `systemctl` / build incantations (run `make help` to list everything):

| Command | What it does |
|---------|--------------|
| `make update`  | **Pull latest, rebuild, restart.** Builds to a side file and atomically swaps the binary, so a failed build never leaves you without one and the swap is safe while the old process is still running. This is how you upgrade. |
| `make restart` | Restart the service **without** rebuilding |
| `make start` / `make stop` | Start / stop the service |
| `make status`  | Show service status |
| `make logs`    | Tail live logs (`journalctl --user-unit=nightshift.service -f`) |
| `make build-pi`| Cross-compile an `arm64` binary for a Raspberry Pi |

**Upgrading is just `make update` on the host** — it pulls `main`, rebuilds, and restarts in one step.

The startup banner (visible in `make logs` right after a restart) prints the live configuration — active agent backend, review gate, auto-iterate, notifications — so you can confirm at a glance what a freshly-restarted instance is running.

---

## Control it from Telegram

Set `TELEGRAM_ENABLED=true` with a bot token + chat ID (the wizard walks you through it) and Nightshift both sends status updates **and** takes commands — a two-way control channel for when you're away from your desk:

| Command | What it does |
|---------|--------------|
| `/status` | Active runs + session stats |
| `/tickets [project] [state]` | Ticket counts by state, or list one state |
| `/ticket ENG-42` | Show a ticket's details |
| `/search-tickets <text>` *(alias `/find`)* | Search Linear tickets by text |
| `/requeue ENG-42 [context]` | Re-queue a blocked/failed ticket, optionally with extra context |
| `/kill ENG-42` | Stop a running ticket |
| `/help` | List all commands |

Only your configured chat can issue commands. `TELEGRAM_VERBOSE=true` also pings on every dispatch (otherwise: terminal events only).

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

## Auto-iterate on PR feedback (optional)

By default Nightshift's job ends when the PR is created. Set `AUTO_ITERATE_PRS=true` and it also polls the PRs it created — when new feedback lands **or CI fails**, it re-engages the agent **on the same branch** and pushes a follow-up commit. The PR just updates; no new PR, no lost context.

It picks up all three kinds of review feedback: top-level conversation comments, submitted reviews (`CHANGES_REQUESTED` / non-empty `COMMENTED`), and **inline review-thread comments** — the latter passed with their `file:line` location so the agent knows exactly where each note applies.

It also watches **CI**: once every check on the head commit has completed and at least one failed, Nightshift fetches the failed-step logs and asks the agent to reproduce and fix them. CI is keyed by commit SHA, so it acts at most once per commit. Review feedback and CI fixes share one per-PR iteration budget and, when both are pending, are handled in a single re-engagement.

```env
AUTO_ITERATE_PRS=true
MAX_PR_ITERATIONS=3              # safety cap per PR
PR_POLL_INTERVAL=120             # seconds between PR scans
TRUSTED_REVIEWERS=               # CSV of bots/logins; empty = humans only
```

**Safety guards built in:**
- **Iteration cap.** After `MAX_PR_ITERATIONS` re-engagements on the same PR, Nightshift stops and pings you (Telegram + Linear comment). Prevents flake-loops and stuck reviews from grinding through your API quota.
- **Trusted-reviewer allowlist.** Humans are always trusted. Bots are only acted on if their login is in `TRUSTED_REVIEWERS`. Default is empty = humans only — bot reviews are still seen and logged, but Nightshift won't act on them blindly. (The default exists because a bot reviewer once confidently misread a `golangci-lint` v2 config as v1; applying its "fix" would have broken CI.)
- **Cursor persistence.** State at `~/.nightshift-state.json` tracks how far the watcher has caught up (last comment/review timestamps + last CI commit SHA). Restarts don't re-react to historical comments or already-handled CI failures.
- **Telegram heads-up** on each re-engage (always — not gated by `TELEGRAM_VERBOSE`).

Disabled by default; set `AUTO_ITERATE_PRS=true` to opt in, or run the wizard.

---

## Configuration

Run `./nightshift setup` to generate config, or copy `.env.example` → `.env` and `repos.example.json` → `repos.json` by hand.

| Variable | Default | Description |
|----------|---------|-------------|
| `LINEAR_API_KEY` | *(required)* | Your Linear personal API key |
| `LINEAR_TEAM_KEY` | `ENG` | Team identifier — the prefix before ticket numbers (e.g. `ENG` for `ENG-42`) |
| `AGENT_BACKEND` | `claude` | Coding agent: `claude` or `codex` |
| `TRIGGER_MODE` | `state` | Pick up work by `state` (column) or `label` |
| `TRIGGER_STATE` | `Next` | Column to watch (state mode) |
| `TRIGGER_LABEL` | *(empty)* | Label to watch (label mode); removed after dispatch |
| `IN_REVIEW_STATE` | `In Review` | State set after the PR is created |
| `REPO_PATH` | *(empty)* | Fallback repo for tickets whose project isn't in `repos.json` |
| `MAIN_BRANCH` | `main` | Default base branch (per-repo override in `repos.json`) |
| `MAX_CONCURRENT` | `3` | Max tickets processed simultaneously |
| `POLL_INTERVAL` | `30` | Seconds between Linear polls |
| `USE_AGENT_TEAMS` | `false` | Claude-only: enable Agent Teams (multi-agent parallelism) |
| `GEMINI_API_KEY` | *(empty)* | Enables the review gate; empty = skip it |
| `MAX_REVIEW_RETRIES` | `1` | Fix passes after Gemini flags issues |

Telegram + auto-iterate vars are covered in their own sections below. State/label names are **case-sensitive** — match your Linear board exactly.

---

## Quality knobs

Two independent toggles shape each run; default is both off (single agent, no review gate) — the simplest, cheapest setup.

| Knob | Off (default) | On |
|------|---------------|-----|
| `USE_AGENT_TEAMS` *(Claude only)* | one agent session per ticket — fast, cheap, runs anywhere incl. Raspberry Pi | a lead Claude agent delegates implementation/tests/review to teammates in parallel — better on complex tickets |
| `GEMINI_API_KEY` | no external review | Gemini reviews the diff before the PR; the agent gets fix passes if it fails |

### The Gemini review gate

A second model has different blind spots than the one that wrote the code, so it catches bugs the implementer misses. When `GEMINI_API_KEY` is set, Nightshift sends the `git diff` + ticket to Gemini, which returns `VERDICT: PASS`/`FAIL` + comments. On FAIL the agent gets `MAX_REVIEW_RETRIES` fix passes (Gemini re-reviews each); if it still fails, the PR is created anyway with the unresolved comments in the body. Uses the Gemini API (~$0.01–$0.05/ticket with `gemini-2.5-pro`). Leave the key empty to skip it — addable later with no other changes.

---

## Linear State Flow

```
[Next] ──────────→ [In Progress] ──────────→ [In Review] ──────→ [Done]
  ↑                     │                          │
  │    BLOCKED or        │   PR created             │   You merge
  └────← no changes ←───┘                          └──────────────→
```

- **Next** → Nightshift picks up the ticket
- **In Progress** → the agent is working on it
- **In Review** → PR created, waiting for your review
- **Done** → You merge the PR (Nightshift doesn't touch this)

If the agent gets stuck or makes no changes, the ticket is moved back to **Next** with a comment explaining why.

---

## Writing Good Tickets

Nightshift is only as good as your tickets. See [docs/WRITING-GOOD-TICKETS.md](docs/WRITING-GOOD-TICKETS.md) for a full guide.

**The one-line rule:** the agent needs to know *what* to change, *where* to change it, and *how you'll know it's done*.

**Good ticket:**
> Login endpoint returns 500 when refresh token is expired. Should return 401 and clear the session cookie. See `auth.controller.ts` line 42. Tests in `auth.controller.spec.ts`. Acceptance: existing tests pass, new test covers the expired token case.

**Bad ticket:**
> Fix the auth bug

---

## Security

### Unattended, no-confirmation execution

Nightshift runs the agent CLI in full-autonomy mode — `claude --dangerously-skip-permissions`, or `codex exec --dangerously-bypass-approvals-and-sandbox` for the Codex backend. Either way, the agent can read, write, and execute commands in your repository without asking for confirmation on each action.

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

Nightshift creates PRs — it doesn't merge them. You review and merge manually. The risk is in what the agent writes during implementation, not in what Nightshift does with it. Use PR review as your safety gate. For extra isolation, run in a container.

### How much does it cost?

**Agent backend:** With `AGENT_BACKEND=claude` (default) it uses the `claude` CLI on your Claude Code subscription; with `codex`, the `codex` CLI on your ChatGPT subscription (or `OPENAI_API_KEY`). Either way it's the CLI's own auth/billing — Nightshift adds no implementation API costs of its own. (Agent Teams uses more tokens per ticket since multiple agents run at once.)

**Gemini:** Only if you enable the review gate — pay-per-token, ~$0.01–$0.05/ticket with `gemini-2.5-pro` ([pricing](https://aistudio.google.com/pricing)).

### Can I run multiple repos simultaneously?

Yes — that's built in. Register each repo in `repos.json` (mapped to its Linear project) and a single Nightshift instance routes every ticket to the right repo automatically. Tickets for different repos run concurrently up to `MAX_CONCURRENT`. See [Repositories](#repositories).

### What if the agent gets stuck?

It outputs `BLOCKED: <reason>` and stops. Nightshift posts the blocker as a Linear comment and moves the ticket back to your trigger state. Add context and re-queue it (from the ticket or via `/requeue` on Telegram).

### What are Agent Teams?

A Claude-only experimental mode (`USE_AGENT_TEAMS=true`) where a lead Claude agent delegates subtasks (implementation, tests, review) to teammates in parallel — faster on complex tickets, more tokens. Requires a recent `claude` CLI. Not applicable to the Codex backend.

### The agent crashed mid-task. How do I clean up?

```bash
./nightshift cleanup           # interactive — pick what to remove
./nightshift cleanup --force   # non-interactive — remove everything stale
```

Cleanup iterates every registered repo, deletes merged branches, force-deletes unmerged `nightshift/*` branches that don't have an open PR, prunes worktrees, and clears agent logs older than 7 days.

---

## Credits

Built with:
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) by Anthropic — implementation engine (default) + [Agent Teams](https://docs.anthropic.com/en/docs/claude-code/agent-teams)
- [OpenAI Codex](https://github.com/openai/codex) — alternative implementation engine (`AGENT_BACKEND=codex`)
- [Gemini](https://aistudio.google.com) by Google — independent code review
- Inspired by Damian Galarza's agent loop patterns and the broader agentic-coding community

---

*MIT License — use freely, fork boldly, sleep soundly.*
