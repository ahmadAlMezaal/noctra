# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Changed

- **Renamed the project from Nightshift to Noctra** (ENG-204). The product, the
  binary, the CLI command, the Go module (`github.com/ahmadAlMezaal/noctra`), the
  systemd unit (`noctra.service`), the PR branch prefix (`noctra/<id>`), the
  Docker image (`ghcr.io/ahmadalmezaal/noctra`), and the canonical domain
  (`getnoctra.dev`) all change accordingly. The night/moon "works while you
  sleep" theme is unchanged.

### Migration

- On startup Noctra now **migrates legacy state in place**: if a `~/.nightshift*`
  path exists and its `~/.noctra*` counterpart does not, it is renamed. This
  covers the config dir (`~/.nightshift` → `~/.noctra`), the clone cache
  (`~/.nightshift-repos`), worktrees (`~/.nightshift-worktrees`), and the PR
  cursor store (`~/.nightshift-state.json`), so an upgraded instance keeps its
  PR cursor, cloned repos, and worktrees without manual intervention. The
  migration is a no-op on fresh installs and never overwrites existing state.
- **Open PRs on the old `nightshift/<id>` branch prefix are no longer
  auto-iterated** — the watcher now recognizes only the `noctra/<id>` prefix.
  Any in-flight PRs on the old prefix should be merged or closed before
  upgrading, or re-driven manually.
