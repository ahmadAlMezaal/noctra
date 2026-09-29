package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func ghOpenPRForBranch(ctx context.Context, repoPath, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch, "--state", "open", "--json", "url", "--jq", ".[0].url // \"\"")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr list --head %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func remoteBranchSHA(ctx context.Context, dir, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git ls-remote origin %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func sweepPushArgs(branch, remoteSHA string) []string {
	args := []string{"push", "-u"}
	if remoteSHA != "" {
		args = append(args, "--force-with-lease=refs/heads/"+branch+":"+remoteSHA)
	}
	return append(args, "origin", branch)
}

func pushSweepBranch(ctx context.Context, workdir, branch string) error {
	sha, err := remoteBranchSHA(ctx, workdir, branch)
	if err != nil {
		return err
	}
	return runIn(ctx, workdir, "git", sweepPushArgs(branch, sha)...)
}
