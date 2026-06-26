package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/config"
)

const dashboardUsage = `Usage: noctra dashboard [flags]

Open an SSH tunnel to a remote Noctra's dashboard and launch it in your browser.
Reads DASHBOARD_SSH (e.g. user@raspberrypi), DASHBOARD_ADDR (for the port), and
DASHBOARD_TOKEN from config; flags override. Ctrl+C closes the tunnel.

Flags:
  --host <user@host>   SSH target running Noctra (default: DASHBOARD_SSH)
  --port <port>        dashboard port (default: from DASHBOARD_ADDR, else 8080)
  --token <token>      dashboard token (default: DASHBOARD_TOKEN)
  --no-browser         tunnel only; don't open a browser
  --help, -h           show this help
`

func runDashboard(scriptDir string, args []string) error {
	var hostFlag, portFlag, tokenFlag string
	noBrowser := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			i, hostFlag = nextArg(args, i)
		case "--port":
			i, portFlag = nextArg(args, i)
		case "--token":
			i, tokenFlag = nextArg(args, i)
		case "--no-browser":
			noBrowser = true
		case "--help", "-h":
			fmt.Print(dashboardUsage)
			return nil
		default:
			return fmt.Errorf("unknown flag %q\n\n%s", args[i], dashboardUsage)
		}
	}

	cfg, _ := config.Load(scriptDir)

	host := firstNonEmpty(hostFlag, dashboardField(cfg, func(c *config.Config) string { return c.DashboardSSH }))
	if host == "" {
		return fmt.Errorf("no SSH host: set DASHBOARD_SSH (e.g. user@raspberrypi) or pass --host")
	}
	port := firstNonEmpty(portFlag, dashboardPort(dashboardField(cfg, func(c *config.Config) string { return c.DashboardAddr })), "8080")
	token := firstNonEmpty(tokenFlag, dashboardField(cfg, func(c *config.Config) string { return c.DashboardToken }))

	dashURL := fmt.Sprintf("http://localhost:%s/", port)
	if token != "" {
		dashURL += "?token=" + token
	}

	fmt.Printf("🌙 Tunnelling %s → localhost:%s\n", host, port)
	fmt.Printf("   Dashboard: %s\n", dashURL)
	fmt.Println("   Ctrl+C to close.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	forward := fmt.Sprintf("%s:localhost:%s", port, port)
	cmd := exec.CommandContext(ctx, "ssh", "-N", "-o", "ExitOnForwardFailure=yes", "-L", forward, host)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ssh tunnel: %w", err)
	}

	if !noBrowser {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			openBrowser(dashURL)
		}()
	}

	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func nextArg(args []string, i int) (int, string) {
	if i+1 < len(args) {
		return i + 1, args[i+1]
	}
	return i, ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func dashboardField(cfg *config.Config, get func(*config.Config) string) string {
	if cfg == nil {
		return ""
	}
	return get(cfg)
}

func dashboardPort(addr string) string {
	if addr == "" {
		return ""
	}
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return strings.TrimPrefix(addr, ":")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
