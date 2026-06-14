# Contributing to Noctra

Thanks for your interest! Noctra is a single Go binary that turns Linear tickets into PRs. Contributions are welcome — bug fixes, new deploy targets, and new **backends** (coding agent, project management, git host) especially.

By participating you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

## Dev setup

Requires **Go 1.23+**. The `Makefile` wraps the common commands (`make help` lists them):

```bash
git clone https://github.com/ahmadAlMezaal/noctra.git
cd noctra
make build      # go build -o noctra ./cmd/noctra
make test       # go test ./...
make vet        # go vet ./...
```

CI runs `go vet`, `go build`, `go test -race`, and `golangci-lint` on every PR — run them locally first to avoid round-trips.

For local development, `go run ./cmd/noctra ...` uses the repo's own `.env` (the cwd-checkout override), so you don't touch `~/.noctra/`.

## Project layout

The **Package map** in [CLAUDE.md](CLAUDE.md) documents every `internal/<pkg>`. The short version:

- `internal/pipeline` — the poll loop and the per-ticket lifecycle (dispatch → worktree → agent → review → PR → Linear).
- `internal/agent` — pluggable coding-agent backends behind the `Backend` interface (Claude / Codex).
- `internal/config`, `internal/linear`, `internal/github`, `internal/repo`, `internal/watch`, `internal/telegram` — config, the external integrations, and the PR-watch classifier.

## Pull requests

- Keep them **small and focused** — one logical change per PR.
- Add or update tests for behaviour changes; keep `go vet` and lint clean.
- Write a clear description: **what** changed and **why**. Reference the issue if there is one.
- Branch off the latest `main`.

## Adding a backend

The coding agent is pluggable behind `agent.Backend` (see `internal/agent`). A new agent CLI needs roughly two things — argv construction and rate-limit phrasing — plus its CLI registered in `internal/config`. The same spirit applies to future project-management (Linear → others) and git-host (GitHub → others) integrations: keep the surface behind an interface and the rest of the pipeline unchanged.

## Reporting bugs

Open a GitHub issue with repro steps. For runtime problems, include the relevant slice of `make logs` (the service log) and, if it's about an agent run, the per-ticket transcript (`make tail TICKET=ENG-42`). Redact secrets.
