# Noctra — common dev + ops commands
#
# Run `make` (no target) to see this list. Tab-indent all recipes — Make
# requires real tabs, not spaces.

BINARY := noctra
PKG    := ./cmd/noctra

# Where per-ticket agent transcripts live. Defaults to ./logs (the config dir
# when running from a repo checkout); override if you set LOG_DIR in .env.
LOG_DIR ?= logs

.DEFAULT_GOAL := help
.PHONY: help build test vet run setup cleanup build-pi update start stop restart status logs tail watch

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' \
		$(MAKEFILE_LIST)

# ── Build & test ────────────────────────────────────────────────────────────

build: ## Compile the local binary
	go build -o $(BINARY) $(PKG)

test: ## Run the test suite
	go test ./...

vet: ## Run go vet
	go vet ./...

# ── Run locally (foreground) ────────────────────────────────────────────────

run: build ## Build and start the poll loop in the foreground
	./$(BINARY)

setup: build ## Build and run the interactive setup wizard
	./$(BINARY) setup

cleanup: build ## Build and run cleanup
	./$(BINARY) cleanup

dashboard: build ## SSH-tunnel to a remote dashboard and open it (set DASHBOARD_SSH in .env)
	./$(BINARY) dashboard

# ── Cross-compile (run on the Mac, scp the output to the Pi) ────────────────

build-pi: ## Cross-compile for Raspberry Pi (arm64)
	GOOS=linux GOARCH=arm64 go build -o $(BINARY)-pi $(PKG)

# ── systemd service management (run on the Pi) ──────────────────────────────

update: ## Pull, rebuild, and restart the systemd service
	git pull
	# Build to a side file first so a failed build can't leave us with no
	# binary, and so we can swap the live one with an atomic rename — rename
	# is allowed while the old binary is executing (the kernel keeps the old
	# inode alive for the running process; the path gets the new inode).
	go build -o $(BINARY).new $(PKG)
	mv $(BINARY).new $(BINARY)
	systemctl --user restart noctra

start: ## Start the systemd service
	systemctl --user start noctra

stop: ## Stop the systemd service
	systemctl --user stop noctra

restart: ## Restart the systemd service (does not rebuild)
	systemctl --user restart noctra

status: ## Show service status
	systemctl --user status noctra

logs: ## Tail Noctra's own service logs (Ctrl+C to stop)
	journalctl --user-unit=noctra.service -f

# ── Agent transcripts (what Claude/Codex is actually doing) ─────────────────

tail: ## Tail one ticket's agent transcript (make tail TICKET=ENG-42)
	@test -n "$(TICKET)" || { echo "usage: make tail TICKET=ENG-42"; exit 1; }
	tail -f "$(LOG_DIR)/$(TICKET).log"

watch: ## Tail the most recently active agent transcript
	@f="$$(ls -t $(LOG_DIR)/*.log 2>/dev/null | head -1)"; \
	test -n "$$f" || { echo "no agent logs in $(LOG_DIR)/"; exit 1; }; \
	echo "tailing $$f"; tail -f "$$f"
