// Package service installs Noctra as a systemd --user service so it can run
// in the background without a source checkout. It writes a unit file pointing at
// the currently-installed binary (the one produced by scripts/install.sh or a
// release archive) and wires the install-time PATH into the unit so the service
// inherits the same git/gh/claude/codex that were on PATH at install time.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// unitFile renders the systemd --user unit content for Noctra. It is a pure
// function (no I/O) so it can be unit-tested. exePath is the symlink-resolved
// path to the noctra binary; pathEnv is the PATH the service should inherit
// (normally the install-time PATH that found git/gh/claude/codex).
func unitFile(exePath, pathEnv string) string {
	return fmt.Sprintf(`[Unit]
Description=Noctra — autonomous Linear→PR agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run
Environment=PATH=%s
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, exePath, pathEnv)
}

// unitPath returns the destination for the user unit file:
// ~/.config/systemd/user/noctra.service.
func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", "noctra.service"), nil
}

// resolveExe returns the symlink-resolved path to the running executable.
func resolveExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// Install writes the systemd --user unit for Noctra and reloads the daemon.
// If the unit already exists it refuses unless force is set. When start is true
// it also enables + starts the service and enables lingering so it survives
// logout (both best-effort, warning on failure). On a non-systemd host it
// returns an error pointing the user at Docker or `noctra run`.
func Install(force, start bool) error {
	sctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found — not a systemd host; use Docker or `noctra run` directly")
	}

	exe, err := resolveExe()
	if err != nil {
		return err
	}
	pathEnv := os.Getenv("PATH")

	dest, err := unitPath()
	if err != nil {
		return err
	}

	if _, err := os.Stat(dest); err == nil && !force {
		return fmt.Errorf("unit file already exists at %s — re-run with --force to overwrite", dest)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}

	content := unitFile(exe, pathEnv)
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	fmt.Printf("✓ Wrote %s\n", dest)

	if out, err := exec.Command(sctl, "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, out)
	}

	if start {
		if out, err := exec.Command(sctl, "--user", "enable", "--now", "noctra.service").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  could not enable/start the service (%v): %s\n", err, out)
		} else {
			fmt.Println("✓ Enabled and started noctra.service")
		}
		// Lingering lets the user service keep running after logout / across reboots.
		// Prefer user.Current() (reliable in systemd/cron), fall back to $USER, and
		// warn if neither yields a username (e.g. cgo-disabled minimal containers).
		if loginctl, lerr := exec.LookPath("loginctl"); lerr == nil {
			username := os.Getenv("USER")
			if u, uerr := user.Current(); uerr == nil {
				username = u.Username
			}
			if username == "" {
				fmt.Fprintln(os.Stderr, "⚠️  could not enable lingering: username is empty")
			} else if out, err := exec.Command(loginctl, "enable-linger", username).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  could not enable lingering (%v): %s\n", err, out)
			} else {
				fmt.Println("✓ Enabled lingering (service survives logout)")
			}
		}
	}

	printNextSteps(start)
	return nil
}

// PurgePaths returns the default ~/.noctra* locations that `uninstall --purge`
// removes: the config dir (.env + logs/), the clone cache, the worktrees, and
// the PR-cursor store. Pure but for reading $HOME, so it's unit-testable.
// These mirror the home-relative defaults in internal/config (REPOS_BASE etc.);
// a custom-path install is reported so the operator can remove those by hand.
func PurgePaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return []string{
		filepath.Join(home, ".noctra"),
		filepath.Join(home, ".noctra-repos"),
		filepath.Join(home, ".noctra-worktrees"),
		filepath.Join(home, ".noctra-state.json"),
		filepath.Join(home, ".noctra-state.json.migrated"),
	}, nil
}

// installedBinaryPath parses the ExecStart path out of the installed unit file
// so uninstall removes the *service's* binary, not whichever binary the command
// happened to be invoked with (e.g. a dev `./noctra` run from a checkout).
// Returns "" when the unit is absent or has no parseable ExecStart, leaving the
// caller to fall back to the running executable.
func installedBinaryPath() string {
	dest, err := unitPath()
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(dest)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		if v := strings.TrimPrefix(strings.TrimSpace(line), "ExecStart="); v != strings.TrimSpace(line) {
			// ExecStart is "<binary> run" — take the first field.
			if fields := strings.Fields(v); len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

// Uninstall is the inverse of Install: it stops + disables the systemd --user
// unit, removes it, reloads the daemon, and deletes the installed binary. When
// purge is true it also removes Noctra's state (see PurgePaths). It is lenient —
// a missing systemctl (non-systemd host) or an already-absent unit/binary is a
// warning, not a failure — so a partial install can always be cleaned up.
// The caller is responsible for confirming a purge (this function performs no
// prompting, so it stays free of stdin and unit-testable around the edges).
func Uninstall(purge bool) error {
	// Resolve the binary to delete from the unit's ExecStart BEFORE we remove
	// the unit — falling back to the running executable when there's no unit.
	binPath := installedBinaryPath()

	// Service teardown — best-effort, and skipped entirely without systemd.
	if sctl, err := exec.LookPath("systemctl"); err != nil {
		fmt.Println("systemctl not found — no systemd service to remove (skipping).")
	} else {
		// stop/disable are best-effort: the unit may already be stopped/absent.
		_ = exec.Command(sctl, "--user", "stop", "noctra.service").Run()
		_ = exec.Command(sctl, "--user", "disable", "noctra.service").Run()
		if dest, err := unitPath(); err == nil {
			if err := os.Remove(dest); err == nil {
				fmt.Printf("✓ Removed %s\n", dest)
			} else if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "⚠️  could not remove %s: %v\n", dest, err)
			}
		}
		if out, err := exec.Command(sctl, "--user", "daemon-reload").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  systemctl --user daemon-reload: %v: %s\n", err, out)
		}
	}

	if purge {
		paths, err := PurgePaths()
		if err != nil {
			return err
		}
		for _, p := range paths {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				continue // never created (e.g. custom paths) — don't claim we removed it
			}
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  could not remove %s: %v\n", p, err)
			} else {
				fmt.Printf("✓ Removed %s\n", p)
			}
		}
		fmt.Println("  (custom REPOS_BASE / WORKTREE_BASE / STATE_DB / STATE_FILE / LOG_DIR paths, if any, were not touched — remove them manually.)")
	}

	// Remove the binary last: once the service is stopped, deleting the running
	// executable is safe on Unix (the open file is unlinked, not truncated).
	if binPath == "" {
		if exe, err := resolveExe(); err == nil {
			binPath = exe
		}
	}
	if binPath != "" {
		if err := os.Remove(binPath); err == nil {
			fmt.Printf("✓ Removed %s\n", binPath)
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "⚠️  could not remove the binary at %s: %v\n", binPath, err)
		}
	}

	fmt.Println()
	fmt.Println("Noctra uninstalled.")
	if !purge {
		fmt.Println("  State was kept (~/.noctra*). Re-run with --purge to remove it too.")
	}
	return nil
}

func printNextSteps(started bool) {
	fmt.Println()
	if started {
		fmt.Println("Noctra is installed and running. Useful commands:")
		fmt.Println("  noctra status    show service status + version")
		fmt.Println("  noctra logs -f   follow the service logs")
		fmt.Println("  noctra restart   restart after a config change")
		return
	}
	fmt.Println("Next steps:")
	fmt.Println("  noctra start     start the service")
	fmt.Println("  noctra status    show service status + version")
	fmt.Println("  noctra logs -f   follow the service logs")
}
