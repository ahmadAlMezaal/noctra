package ghauth

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeApp   Mode = "app"
	ModeToken Mode = "token"
)

func ResolveMode(setting string, linked bool) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "", "auto":
		if linked {
			return ModeApp, nil
		}
		return ModeToken, nil
	case "app":
		if !linked {
			return "", fmt.Errorf("GITHUB_AUTH_MODE=app but %w", ErrNotLinked)
		}
		return ModeApp, nil
	case "token":
		return ModeToken, nil
	}
	return "", fmt.Errorf("GITHUB_AUTH_MODE must be auto, app or token (got %q)", setting)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func HelperCommand(exe, dir string, scope Scope) string {
	return "!" + shellQuote(exe) + " git-credential --dir " + shellQuote(dir) + " --scope " + string(scope)
}

func GitConfigEnv(helper string) []string {
	return []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=" + helper,
		"GIT_CONFIG_KEY_2=credential.useHttpPath",
		"GIT_CONFIG_VALUE_2=true",
	}
}

func IdentityEnv(inst Instance) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + inst.BotLogin,
		"GIT_AUTHOR_EMAIL=" + inst.BotEmail,
		"GIT_COMMITTER_NAME=" + inst.BotLogin,
		"GIT_COMMITTER_EMAIL=" + inst.BotEmail,
	}
}

func MergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	replaced := make(map[string]bool, len(overrides))
	for _, kv := range overrides {
		replaced[envKey(kv)] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if !replaced[envKey(kv)] {
			out = append(out, kv)
		}
	}
	return append(out, overrides...)
}

func envKey(kv string) string {
	k, _, _ := strings.Cut(kv, "=")
	return k
}
