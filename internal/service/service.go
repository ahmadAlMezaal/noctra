// Package service installs Nightshift as a systemd --user service so it can run
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
)

// unitFile renders the systemd --user unit content for Nightshift. It is a pure
// function (no I/O) so it can be unit-tested. exePath is the symlink-resolved
// path to the nightshift binary; pathEnv is the PATH the service should inherit
// (normally the install-time PATH that found git/gh/claude/codex).
func unitFile(exePath, pathEnv string) string {
	return fmt.Sprintf(`[Unit]
Description=Nightshift — autonomous Linear→PR agent
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
// ~/.config/systemd/user/nightshift.service.
func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", "nightshift.service"), nil
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

// Install writes the systemd --user unit for Nightshift and reloads the daemon.
// If the unit already exists it refuses unless force is set. When start is true
// it also enables + starts the service and enables lingering so it survives
// logout (both best-effort, warning on failure). On a non-systemd host it
// returns an error pointing the user at Docker or `nightshift run`.
func Install(force, start bool) error {
	sctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found — not a systemd host; use Docker or `nightshift run` directly")
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
		if out, err := exec.Command(sctl, "--user", "enable", "--now", "nightshift.service").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  could not enable/start the service (%v): %s\n", err, out)
		} else {
			fmt.Println("✓ Enabled and started nightshift.service")
		}
		// Lingering lets the user service keep running after logout / across reboots.
		// user.Current() is more reliable than $USER, which can be empty in
		// non-interactive shells / systemd / cron contexts.
		if loginctl, lerr := exec.LookPath("loginctl"); lerr == nil {
			if u, uerr := user.Current(); uerr == nil {
				if out, err := exec.Command(loginctl, "enable-linger", u.Username).CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "⚠️  could not enable lingering (%v): %s\n", err, out)
				} else {
					fmt.Println("✓ Enabled lingering (service survives logout)")
				}
			}
		}
	}

	printNextSteps(start)
	return nil
}

func printNextSteps(started bool) {
	fmt.Println()
	if started {
		fmt.Println("Nightshift is installed and running. Useful commands:")
		fmt.Println("  nightshift status    show service status + version")
		fmt.Println("  nightshift logs -f   follow the service logs")
		fmt.Println("  nightshift restart   restart after a config change")
		return
	}
	fmt.Println("Next steps:")
	fmt.Println("  nightshift start     start the service")
	fmt.Println("  nightshift status    show service status + version")
	fmt.Println("  nightshift logs -f   follow the service logs")
}
