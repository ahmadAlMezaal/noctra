// Package configcmd implements `noctra config` — read, edit, and surgically
// update individual .env settings without a full wizard re-run.
//
// Subcommands:
//
//	noctra config path          print the resolved .env path
//	noctra config edit          open .env in $EDITOR
//	noctra config get KEY       print one value (exit 1 if unset)
//	noctra config set KEY=VALUE upsert one key (atomic, comment-preserving)
package configcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ahmadAlMezaal/noctra/internal/config"
)

// Run dispatches the config subcommand. scriptDir is resolved the same way
// as everywhere else (resolveScriptDir / config.DefaultConfigDir).
func Run(scriptDir string, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	envFile := filepath.Join(scriptDir, ".env")

	switch args[0] {
	case "path":
		return runPath(envFile)
	case "edit":
		return runEdit(envFile)
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: noctra config get KEY")
		}
		return runGet(envFile, args[1])
	case "set":
		return runSet(envFile, args[1:])
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func printUsage() {
	fmt.Println("Usage: noctra config <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  path            Print the resolved .env file path")
	fmt.Println("  edit            Open .env in $EDITOR (falls back to vi/nano)")
	fmt.Println("  get KEY         Print the value of KEY from .env")
	fmt.Println("  set KEY=VALUE   Set KEY to VALUE (atomic, preserves comments)")
	fmt.Println("  set KEY VALUE   Same as KEY=VALUE")
}

// runPath prints the resolved .env path for scripting.
func runPath(envFile string) error {
	fmt.Println(envFile)
	return nil
}

// runEdit opens the resolved .env in the user's $EDITOR. Falls back to vi,
// then nano. Mirrors how `noctra logs` execs journalctl — stdin/stdout/stderr
// wired through.
func runEdit(envFile string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		// Try vi first, then nano.
		if _, err := exec.LookPath("vi"); err == nil {
			editor = "vi"
		} else if _, err := exec.LookPath("nano"); err == nil {
			editor = "nano"
		} else {
			return fmt.Errorf("$EDITOR is not set and neither vi nor nano is on PATH")
		}
	}

	// Ensure the config directory exists so the editor doesn't fail on a
	// brand-new install where ~/.noctra/ hasn't been created yet.
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	bin, err := exec.LookPath(editor)
	if err != nil {
		return fmt.Errorf("editor %q not found: %w", editor, err)
	}

	// exec replaces the current process — cleaner than cmd.Run for an
	// interactive editor (no double-process, signal handling is simpler).
	return syscall.Exec(bin, []string{editor, envFile}, os.Environ())
}

// runGet prints the current value of a single key from the .env file.
// Exits non-zero (returns error) when the key is not set.
func runGet(envFile, key string) error {
	env, err := config.LoadEnvFile(envFile)
	if err != nil {
		return err
	}
	val, ok := env[key]
	if !ok {
		return fmt.Errorf("key %q is not set in %s", key, envFile)
	}
	fmt.Println(val)
	return nil
}

// runSet upserts a single key-value pair. Accepts "KEY=VALUE" or "KEY VALUE".
func runSet(envFile string, args []string) error {
	key, val, err := parseKeyValue(args)
	if err != nil {
		return err
	}
	return config.PatchEnvFile(envFile, map[string]string{key: val})
}

// parseKeyValue extracts a key-value pair from the arguments. It supports
// two forms: ["KEY=VALUE"] and ["KEY", "VALUE"].
func parseKeyValue(args []string) (key, value string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("usage: noctra config set KEY=VALUE (or KEY VALUE)")
	}

	// Form 1: KEY=VALUE in a single arg.
	if eq := strings.IndexByte(args[0], '='); eq >= 0 {
		key = args[0][:eq]
		value = args[0][eq+1:]
		if key == "" {
			return "", "", fmt.Errorf("empty key in %q", args[0])
		}
		return key, value, nil
	}

	// Form 2: KEY VALUE as two args.
	if len(args) < 2 {
		return "", "", fmt.Errorf("usage: noctra config set KEY=VALUE (or KEY VALUE)")
	}
	return args[0], args[1], nil
}
