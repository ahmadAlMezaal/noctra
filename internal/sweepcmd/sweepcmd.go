package sweepcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/config"
)

func Run(scriptDir string, args []string) error {
	cfg, err := config.Load(scriptDir)
	if err != nil {
		return err
	}

	body, help, err := parseArgs(args)
	if err != nil {
		return err
	}
	if help {
		printUsage()
		return nil
	}

	if cfg.DashboardAddr == "" {
		return fmt.Errorf("`noctra sweep` talks to the running daemon over the dashboard API, which is off.\nSet DASHBOARD_ADDR (e.g. 127.0.0.1:8080) and DASHBOARD_ADMIN_TOKEN in your .env, then restart.\nAlternatively send /sweep from Telegram, which needs no extra config")
	}
	if cfg.DashboardAdminToken == "" {
		return fmt.Errorf("DASHBOARD_ADMIN_TOKEN is not set — it gates every control endpoint.\nAdd it to your .env and restart, or send /sweep from Telegram instead")
	}

	return post(cfg, body)
}

func parseArgs(args []string) (body request, help bool, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--now":
		case "--force", "-f":
			body.Force = true
		case "--task", "-t":
			if i+1 >= len(args) {
				return request{}, false, fmt.Errorf("--task needs a value (e.g. --task lint-cleanup)")
			}
			i++
			body.Tasks = append(body.Tasks, args[i])
		case "--repo", "-r":
			if i+1 >= len(args) {
				return request{}, false, fmt.Errorf("--repo needs a value (e.g. --repo trade-mate)")
			}
			i++
			body.Repos = append(body.Repos, args[i])
		case "--help", "-h":
			return request{}, true, nil
		default:
			return request{}, false, fmt.Errorf("unknown flag %q\n\nRun `noctra sweep --help` for usage", args[i])
		}
	}
	return body, false, nil
}

type request struct {
	Tasks []string `json:"tasks,omitempty"`
	Repos []string `json:"repos,omitempty"`
	Force bool     `json:"force,omitempty"`
}

func post(cfg *config.Config, body request) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := "http://" + strings.TrimPrefix(strings.TrimPrefix(cfg.DashboardAddr, "http://"), "https://") + "/api/sweep"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.DashboardAdminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Noctra at %s: %w\n\nIs the daemon running? Check `noctra status`", cfg.DashboardAddr, err)
	}
	defer func() { _ = resp.Body.Close() }()

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sweep refused (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	fmt.Println("🧹 Sweep queued.")
	fmt.Println("   " + describe(body))
	fmt.Println()
	fmt.Println("It starts within a few seconds. Follow it with `noctra tail`.")
	return nil
}

func describe(body request) string {
	var parts []string
	if len(body.Tasks) > 0 {
		parts = append(parts, "tasks: "+strings.Join(body.Tasks, ", "))
	}
	if len(body.Repos) > 0 {
		parts = append(parts, "repos: "+strings.Join(body.Repos, ", "))
	}
	if len(parts) == 0 {
		parts = append(parts, "all eligible tasks")
	}
	if body.Force {
		parts = append(parts, "cooldowns ignored")
	}
	return strings.Join(parts, "; ")
}

func printUsage() {
	fmt.Print(`noctra sweep — run a maintenance sweep now, without waiting for the schedule

Usage:
  noctra sweep [--task <name>] [--repo <slug>] [--force]

Flags:
  --task, -t <name>   Only this sweep task (repeatable, e.g. lint-cleanup)
  --repo, -r <slug>   Only repos matching this substring (repeatable)
  --force, -f         Dispatch even if the per-repo cooldown has not expired
  --now               Accepted for readability; sweeps are always immediate
  --help, -h          Show this help

Examples:
  noctra sweep                                  # everything currently eligible
  noctra sweep --task lint-cleanup              # one task, every repo
  noctra sweep -t dead-code -r trade-mate -f    # one task, one repo, ignore cooldown

Requires the daemon to be running with DASHBOARD_ADDR and DASHBOARD_ADMIN_TOKEN set.
The /sweep Telegram command does the same thing with no extra configuration.
`)
}
