#!/bin/sh
# Entrypoint for the Nightshift container. Configures the bits Nightshift
# normally inherits from a developer's machine (git identity, GitHub auth) so a
# fresh container can commit and push without manual setup, then execs the CMD.
set -e

# Some orchestrators (Kubernetes runAsNonRoot, OpenShift) run with a HOME that
# isn't writable by the container user, which breaks git/gh config writes below.
# Fall back to a writable directory in that case.
if [ ! -w "${HOME:-/root}" ]; then
  export HOME=/tmp
fi

# Nightshift commits with the ambient git identity; a fresh container has none,
# so `git commit` would fail. Default to a bot identity, overridable via env.
git config --global user.name  "${GIT_USER_NAME:-Nightshift}"
git config --global user.email "${GIT_USER_EMAIL:-nightshift@users.noreply.github.com}"
# Worktrees/clones live on a mounted volume that may be owned by a different
# uid than the container user — avoid git's "dubious ownership" refusal.
# --replace-all keeps this idempotent so restarts don't pile up duplicate entries.
git config --global --replace-all safe.directory '*'

# GitHub auth: gh reads GH_TOKEN from the env for PR creation; wire the same
# token into git's credential helper so `git push` over HTTPS works too.
# Accept GITHUB_TOKEN as an alias (common in CI environments).
if [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
  export GH_TOKEN="${GH_TOKEN:-$GITHUB_TOKEN}"
  gh auth setup-git >/dev/null 2>&1 || true
fi

exec "$@"
