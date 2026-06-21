package lessons

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"

	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

// ProcessMergedPRs scans all tracked PRs in the store, checks if they have merged
// (or closed), computes the diff of human edits if merged, calls the Gemini review
// gate to update per-repo durable notes, and marks them as processed.
func ProcessMergedPRs(ctx context.Context, store *state.Store, gh *github.Client, resolver *repo.Resolver, reviewGate *review.Gate) {
	if store == nil || gh == nil || resolver == nil {
		return
	}

	prs := store.All()
	for prURL, cursor := range prs {
		if cursor.MergedProcessed {
			continue
		}

		logger := slog.With("pr", prURL, "ticket", cursor.TicketID)

		// 1. Fetch the latest PR details from GitHub to see its state
		details, err := gh.GetPR(ctx, prURL)
		if err != nil {
			logger.Warn("lessons: failed to get PR details", "err", err)
			continue
		}

		if details.State == "OPEN" {
			// Still open, check back later.
			continue
		}

		if details.State == "CLOSED" {
			// Closed without merge. Nothing to summarize, just mark processed.
			logger.Info("lessons: PR closed without merging; marking processed")
			if err := store.Update(prURL, func(r *state.PRState) {
				r.MergedProcessed = true
			}); err != nil {
				logger.Warn("lessons: failed to update PR state", "err", err)
			}
			continue
		}

		if details.State == "MERGED" {
			logger.Info("lessons: PR merged; processing human post-merge edits")
			if err := processMergedPR(ctx, store, resolver, reviewGate, prURL, cursor); err != nil {
				logger.Error("lessons: failed to process merged PR", "err", err)
			}

			// Regardless of success/failure, mark it as processed so we don't block
			// the polling loop or retry indefinitely on failing git diffs or API errors.
			if err := store.Update(prURL, func(r *state.PRState) {
				r.MergedProcessed = true
			}); err != nil {
				logger.Warn("lessons: failed to update PR state", "err", err)
			}
		}
	}
}

func processMergedPR(ctx context.Context, store *state.Store, resolver *repo.Resolver, reviewGate *review.Gate, prURL string, cursor state.PRState) error {
	ownerRepo, err := extractOwnerRepoFromPRURL(prURL)
	if err != nil {
		return fmt.Errorf("extract owner/repo: %w", err)
	}

	resolved, err := resolver.ResolveDirect(ctx, ownerRepo, "")
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}
	repoDir := resolved.Path

	// Fetch the PR head to FETCH_HEAD
	prNumStr := prURL[strings.LastIndex(prURL, "/")+1:]
	fetchCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "fetch", "origin", fmt.Sprintf("pull/%s/head", prNumStr))
	if err := fetchCmd.Run(); err != nil {
		return fmt.Errorf("git fetch PR head (pull/%s/head): %w", prNumStr, err)
	}

	if cursor.LastPushedSHA == "" {
		return fmt.Errorf("no LastPushedSHA recorded for this PR; cannot compute human edits")
	}

	// Diff between Noctra's last pushed commit and FETCH_HEAD (the final merged PR branch head)
	diffCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "diff", cursor.LastPushedSHA, "FETCH_HEAD")
	var diffOut bytes.Buffer
	diffCmd.Stdout = &diffOut
	if err := diffCmd.Run(); err != nil {
		return fmt.Errorf("git diff %s..FETCH_HEAD: %w", cursor.LastPushedSHA, err)
	}

	diffStr := diffOut.String()
	if strings.TrimSpace(diffStr) == "" {
		slog.Info("lessons: no human edits detected (empty diff)", "pr", prURL)
		return nil
	}

	if reviewGate == nil || !reviewGate.Enabled() {
		return errors.New("Gemini review gate is not enabled/configured; cannot summarize human edits")
	}

	repoSlug := repo.Slug(ownerRepo)
	existingLessons, err := store.GetLessons(repoSlug)
	if err != nil {
		return fmt.Errorf("get existing lessons: %w", err)
	}

	newLessons, err := reviewGate.SummarizeLessons(ctx, existingLessons, diffStr)
	if err != nil {
		return fmt.Errorf("summarize lessons: %w", err)
	}

	if err := store.SaveLessons(repoSlug, newLessons); err != nil {
		return fmt.Errorf("save lessons: %w", err)
	}

	slog.Info("lessons: successfully consolidated repo lessons", "repo", repoSlug, "lessons_len", len(newLessons))
	return nil
}

func extractOwnerRepoFromPRURL(prURL string) (string, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("PR URL path too short: %q", u.Path)
	}
	return parts[0] + "/" + parts[1], nil
}
