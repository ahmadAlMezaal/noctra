package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

type TokenSource func(ctx context.Context, ownerRepo string) (string, error)

var tokenSource atomic.Pointer[TokenSource]

func SetTokenSource(src TokenSource) {
	if src == nil {
		tokenSource.Store(nil)
		return
	}
	tokenSource.Store(&src)
}

func Command(ctx context.Context, ownerRepo string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	src := tokenSource.Load()
	if src == nil {
		return cmd, nil
	}
	if ownerRepo == "" {
		return nil, fmt.Errorf("gh %s: repository unknown, cannot pick a GitHub App token", strings.Join(args, " "))
	}
	token, err := (*src)(ctx, ownerRepo)
	if err != nil {
		return nil, fmt.Errorf("GitHub App token for %s: %w", ownerRepo, err)
	}
	cmd.Env = append(os.Environ(), "GH_TOKEN="+token, "GITHUB_TOKEN="+token)
	return cmd, nil
}

func CommandInDir(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	ownerRepo := ""
	if tokenSource.Load() != nil {
		var err error
		if ownerRepo, err = OwnerRepoOfDir(ctx, dir); err != nil {
			return nil, err
		}
	}
	cmd, err := Command(ctx, ownerRepo, args...)
	if err != nil {
		return nil, err
	}
	cmd.Dir = dir
	return cmd, nil
}

func OwnerRepoOfDir(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("read origin remote of %s: %w", dir, err)
	}
	return ExtractOwnerRepo(strings.TrimSpace(string(out)))
}

func OwnerRepoOfPR(prURL string) (string, error) {
	owner, repo, _, err := parsePRURL(prURL)
	if err != nil {
		return "", err
	}
	return owner + "/" + repo, nil
}
