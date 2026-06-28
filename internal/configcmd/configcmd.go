// Package configcmd implements `noctra config` (path/list/edit/get/set) — read and surgically update .env settings without a full wizard re-run.
package configcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/ahmadAlMezaal/noctra/internal/config"
)

// Run dispatches the config subcommand.
func Run(scriptDir string, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	envFile := filepath.Join(scriptDir, ".env")

	switch args[0] {
	case "path":
		return runPath(envFile)
	case "list", "ls":
		reveal := false
		for _, a := range args[1:] {
			if a == "--reveal" || a == "--show-secrets" {
				reveal = true
			}
		}
		return runList(envFile, reveal)
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
	fmt.Println("  list            Print all KEY=VALUE pairs (secrets masked; --reveal to show)")
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

// runList prints every KEY=VALUE from .env sorted by key, masking secret-looking values unless reveal is set; an empty/missing .env is reported.
func runList(envFile string, reveal bool) error {
	env, err := config.LoadEnvFile(envFile)
	if err != nil {
		return err
	}
	if len(env) == 0 {
		fmt.Printf("No settings found in %s\n", envFile)
		return nil
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	masked := false
	for _, k := range keys {
		val := env[k]
		if !reveal && isSecretKey(k) && val != "" {
			val = maskSecret(val)
			masked = true
		}
		fmt.Printf("%s=%s\n", k, val)
	}
	if masked {
		fmt.Fprintln(os.Stderr, "\n(secrets masked — pass --reveal to show full values)")
	}
	return nil
}

// secretKeyParts flags keys whose values are sensitive and should be masked.
var secretKeyParts = []string{"TOKEN", "SECRET", "KEY", "PASSWORD", "PASS", "WEBHOOK"}

// isSecretKey reports whether a setting's value should be masked by default.
func isSecretKey(key string) bool {
	up := strings.ToUpper(key)
	for _, part := range secretKeyParts {
		if strings.Contains(up, part) {
			return true
		}
	}
	return false
}

// maskSecret hides a value, keeping the last 4 chars as a hint when it's long enough to spare them.
func maskSecret(val string) string {
	if len(val) <= 8 {
		return "••••••"
	}
	return "••••••" + val[len(val)-4:]
}

// runEdit opens .env in $EDITOR (falling back to vi then nano); $EDITOR may carry args, so only the first field is resolved as the binary.
func runEdit(envFile string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		if _, err := exec.LookPath("vi"); err == nil {
			editor = "vi"
		} else if _, err := exec.LookPath("nano"); err == nil {
			editor = "nano"
		} else {
			return fmt.Errorf("$EDITOR is not set and neither vi nor nano is on PATH")
		}
	}

	// Create the config dir so the editor doesn't fail on a brand-new install.
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	parts := strings.Fields(editor)

	bin, err := exec.LookPath(parts[0])
	if err != nil {
		return fmt.Errorf("editor %q not found: %w", parts[0], err)
	}

	argv := make([]string, 0, len(parts)+1)
	argv = append(argv, parts...)
	argv = append(argv, envFile)

	// exec replaces this process — cleaner than cmd.Run for an interactive editor (no double-process, simpler signals).
	return syscall.Exec(bin, argv, os.Environ())
}

// runGet prints one key's value from .env, erroring (exit non-zero) when unset.
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

// parseKeyValue extracts a pair from ["KEY=VALUE"] or ["KEY","VALUE"], rejecting extra args to avoid silently losing unquoted multi-word values.
func parseKeyValue(args []string) (key, value string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("usage: noctra config set KEY=VALUE (or KEY VALUE)")
	}

	if eq := strings.IndexByte(args[0], '='); eq >= 0 {
		if len(args) > 1 {
			return "", "", fmt.Errorf("too many arguments; did you forget to quote the value?")
		}
		key = args[0][:eq]
		value = args[0][eq+1:]
		if key == "" {
			return "", "", fmt.Errorf("empty key in %q", args[0])
		}
		return key, value, nil
	}

	if len(args) < 2 {
		return "", "", fmt.Errorf("usage: noctra config set KEY=VALUE (or KEY VALUE)")
	}
	if len(args) > 2 {
		return "", "", fmt.Errorf("too many arguments; did you forget to quote the value?")
	}
	return args[0], args[1], nil
}
