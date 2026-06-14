# syntax=docker/dockerfile:1

# ── Build stage ───────────────────────────────────────────────────────────────
# Compile the static Nightshift binary. CGO is disabled so the result runs on
# any Linux without libc surprises.
FROM golang:1.23-bookworm AS build
WORKDIR /src

# Dependency layer first for caching. Nightshift is stdlib-only today (no
# go.sum); the wildcard keeps this working if dependencies are added later.
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/nightshift ./cmd/nightshift

# ── Runtime stage ─────────────────────────────────────────────────────────────
# Nightshift shells out to git (worktrees/clone), gh (PR creation), and a coding
# agent CLI (claude/codex — both are npm packages), so the runtime needs all of
# them. The node base supplies the agent runtime; git + gh are added on top.
FROM node:20-bookworm-slim AS runtime

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git gnupg \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
 && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

# Bundle BOTH coding-agent backends so AGENT_BACKEND=claude|codex works out of
# the box. Auth is provided at runtime via ANTHROPIC_API_KEY / OPENAI_API_KEY
# (interactive subscription login isn't viable in a detached container).
RUN npm install -g @anthropic-ai/claude-code @openai/codex \
 && npm cache clean --force

COPY --from=build /out/nightshift /usr/local/bin/nightshift
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# All mutable state lives under /data so a single mounted volume persists the
# repos cache, worktrees, logs, and the PR cursor across restarts. Each path is
# an env override Nightshift already honours.
ENV REPOS_BASE=/data/repos \
    WORKTREE_BASE=/data/worktrees \
    LOG_DIR=/data/logs \
    STATE_FILE=/data/state.json
WORKDIR /data
VOLUME /data

# The container's only process is Nightshift (PID 1 via exec); if it dies the
# container exits, so an orchestrator's restart policy is the health mechanism.
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["nightshift"]
