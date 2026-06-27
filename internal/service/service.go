// Package service installs Noctra as a systemd --user service, writing a unit pointing at the
// installed binary and baking in the install-time PATH so the service finds the same git/gh/agent CLIs.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// unitFile renders the systemd --user unit (pure, for testing); exePath is the symlink-resolved binary, pathEnv the PATH to inherit.
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

// unitPath returns ~/.config/systemd/user/noctra.service.
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

// Install writes the systemd --user unit and reloads the daemon; refuses an existing unit unless force.
// When start is true it also enables/starts the service and lingering (best-effort); a non-systemd host errors.
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
		// Lingering keeps the user service running after logout/reboot; prefer user.Current() (reliable in systemd/cron) over $USER.
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

// PurgePaths returns the default ~/.noctra* locations `uninstall --purge` removes (config dir, clone cache, worktrees, PR-cursor store).
// These mirror internal/config's home-relative defaults; custom-path installs must be removed by hand.
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
	}, nil
}

// installedBinaryPath parses ExecStart from the unit so uninstall removes the service's binary, not the invoking one;
// returns "" when the unit is absent or unparseable, so the caller falls back to the running executable.
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

// Uninstall reverses Install: stop/disable/remove the unit, reload, and delete the binary; purge also removes state (PurgePaths).
// It is lenient — missing systemctl or an already-absent unit/binary warns, not fails — and does no prompting (caller confirms purge).
func Uninstall(purge bool) error {
	// Resolve the binary from ExecStart before removing the unit; fall back to the running exe if there's no unit.
	binPath := installedBinaryPath()

	// Service teardown — best-effort, skipped without systemd.
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
				continue // never created — don't claim we removed it
			}
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  could not remove %s: %v\n", p, err)
			} else {
				fmt.Printf("✓ Removed %s\n", p)
			}
		}
		fmt.Println("  (custom REPOS_BASE / WORKTREE_BASE / STATE_DB / STATE_FILE / LOG_DIR paths, if any, were not touched — remove them manually.)")
	}

	// Remove the binary last: once stopped, deleting the running exe is safe on Unix (unlinked, not truncated).
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
