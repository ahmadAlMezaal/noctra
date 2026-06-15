# syntax=docker/dockerfile:1

# ── Build stage ───────────────────────────────────────────────────────────────
# Compile the static Noctra binary. CGO is disabled so the result runs on
# any Linux without libc surprises — and so the compiler can cross-compile.
#
# Pin the build stage to BUILDPLATFORM (the native builder arch) and let Go
# cross-compile to TARGETARCH. For a multi-arch build this keeps the Go
# toolchain running natively instead of emulating the compiler under QEMU for
# the non-native arch — much faster, with an identical static result.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
WORKDIR /src

# Dependency layer first for caching. Noctra is stdlib-only today (no
# go.sum); the wildcard keeps this working if dependencies are added later.
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .
ARG VERSION=docker
# TARGETOS/TARGETARCH are injected by buildx for each target platform. They're
# empty under a plain `docker build` (legacy builder / BuildKit off), so fall
# back to the native arch via `go env` — keeping GOARCH= a literal assignment
# prefix (a VAR=val produced by expansion is NOT treated as an assignment).
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/noctra ./cmd/noctra

# ── Runtime stage ─────────────────────────────────────────────────────────────
# Noctra shells out to git (worktrees/clone), gh (PR creation), and a coding
# agent CLI (claude/codex/copilot, all via npm), so the runtime needs all of
# them. The node base supplies the agent runtime; git + gh on top.
# Node 22+ is required: @github/copilot lists Node.js 22 as its prerequisite but
# declares no `engines` field, so an older base installs cleanly yet fails at
# runtime — claude/codex are happy on 22 too.
FROM node:22-bookworm-slim AS runtime

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

# Bundle ALL coding-agent backends so AGENT_BACKEND=claude|codex|copilot works
# out of the box. The GitHub Copilot CLI is the standalone agentic CLI
# (@github/copilot, binary `copilot`) — NOT the `gh copilot` suggest/explain
# extension, which can't edit files or take `--allow-all-tools -p`. Auth is
# provided at runtime via ANTHROPIC_API_KEY / OPENAI_API_KEY / GH_TOKEN
# (interactive subscription login isn't viable in a detached container).
RUN npm install -g @anthropic-ai/claude-code @openai/codex @github/copilot \
 && npm cache clean --force

COPY --from=build /out/noctra /usr/local/bin/noctra
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# All mutable state lives under /data so a single mounted volume persists the
# repos cache, worktrees, logs, and the PR cursor across restarts. Each path is
# an env override Noctra already honours.
ENV REPOS_BASE=/data/repos \
    WORKTREE_BASE=/data/worktrees \
    LOG_DIR=/data/logs \
    STATE_FILE=/data/state.json \
    STATE_DB=/data/noctra.db
WORKDIR /data
VOLUME /data

# The container's only process is Noctra (PID 1 via exec); if it dies the
# container exits, so an orchestrator's restart policy is the health mechanism.
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["noctra"]
