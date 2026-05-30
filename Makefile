# Nightshift — common dev + ops commands
#
# Run `make` (no target) to see this list. Tab-indent all recipes — Make
# requires real tabs, not spaces.

BINARY := nightshift
PKG    := ./cmd/nightshift

.DEFAULT_GOAL := help
.PHONY: help build test vet run setup cleanup build-pi update start stop restart status logs

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

# ── Cross-compile (run on the Mac, scp the output to the Pi) ────────────────

build-pi: ## Cross-compile for Raspberry Pi (arm64)
	GOOS=linux GOARCH=arm64 go build -o $(BINARY)-pi $(PKG)

# ── systemd service management (run on the Pi) ──────────────────────────────

update: ## Pull, rebuild, and restart the systemd service
	git pull
	go build -o $(BINARY) $(PKG)
	systemctl --user restart nightshift

start: ## Start the systemd service
	systemctl --user start nightshift

stop: ## Stop the systemd service
	systemctl --user stop nightshift

restart: ## Restart the systemd service (does not rebuild)
	systemctl --user restart nightshift

status: ## Show service status
	systemctl --user status nightshift

logs: ## Tail live logs (Ctrl+C to stop)
	journalctl --user-unit=nightshift.service -f
