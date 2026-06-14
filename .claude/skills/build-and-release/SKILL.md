---
name: build-and-release
description: Use when building Nightshift locally, cross-compiling for Raspberry Pi, validating GoReleaser, or cutting a tagged release with cross-platform archives.
---

# Build And Release Nightshift

Use this playbook for local builds, Raspberry Pi binaries, and official releases.

## Local Development Build

```bash
make build
# equivalent:
go build -o nightshift ./cmd/nightshift
```

Run the binary:

```bash
./nightshift version
./nightshift doctor
./nightshift
```

## Verification Before Shipping

Run the standard checks:

```bash
go test ./...
go vet ./...
```

If `golangci-lint` is available, run:

```bash
golangci-lint run
```

The repository uses `.golangci.yml` v2 with standard linters plus `gofmt`.

## Raspberry Pi Cross-Compile

For Pi 4/5 with a 64-bit OS:

```bash
GOOS=linux GOARCH=arm64 go build -o nightshift ./cmd/nightshift
scp nightshift pi@your-pi:/srv/nightshift/
```

For Pi 3 or 32-bit Raspberry Pi OS:

```bash
GOOS=linux GOARCH=arm GOARM=7 go build -o nightshift ./cmd/nightshift
scp nightshift pi@your-pi:/srv/nightshift/
```

The Makefile also has:

```bash
make build-pi
```

which produces an arm64 binary named `nightshift-pi`.

## Systemd Host Upgrade

On a host running Nightshift as a `systemd --user` service:

```bash
make update
```

This pulls the latest branch, builds to `nightshift.new`, atomically swaps it into place, and restarts `nightshift.service`. A failed build leaves the previous binary intact.

Useful service commands:

```bash
make status
make logs
make restart
make stop
make start
```

## Docker Image

The `Dockerfile` builds a static Go binary and packages it into a `node:20-bookworm-slim` runtime with `git`, `gh`, Claude Code, and Codex installed.

For local validation:

```bash
docker build -t nightshift:local .
```

Mutable state should live under `/data` in containers. Repos are declared per-project in Linear (a `Repo: owner/name` line in the project description) — nothing to mount or pass in.

## GoReleaser Validation

Releases are configured in `.goreleaser.yaml` and `.github/workflows/release.yml`.

Before changing release config, validate locally:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=publish
```

GoReleaser builds:

1. Linux `amd64`, `arm64`, and `armv7`.
2. macOS `amd64` and `arm64`.
3. Archives and checksums.

The `main.version` variable is stamped through `-ldflags "-X main.version=..."`.

## Cutting An Official Release

1. Ensure `main` is green and the changelog/release notes are ready.
2. Tag with a semver `v*` tag:

```bash
git tag v2.0.0
git push origin v2.0.0
```

3. The GitHub release workflow publishes the release artifacts automatically.
4. Verify the GitHub Release contains archives and checksums for all configured targets.

