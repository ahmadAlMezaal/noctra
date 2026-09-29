package lessons

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"regexp"
	"strings"

	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

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

		details, err := gh.GetPR(ctx, prURL)
		if err != nil {
			logger.Warn("lessons: failed to get PR details", "err", err)
			continue
		}

		if details.State == "OPEN" {
			continue
		}

		if details.State == "CLOSED" {
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

	prNumStr := prURL[strings.LastIndex(prURL, "/")+1:]
	fetchCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "fetch", "origin", fmt.Sprintf("pull/%s/head", prNumStr))
	if err := fetchCmd.Run(); err != nil {
		return fmt.Errorf("git fetch PR head (pull/%s/head): %w", prNumStr, err)
	}

	if cursor.LastPushedSHA == "" {
		return fmt.Errorf("no LastPushedSHA recorded for this PR; cannot compute human edits")
	}

	diffStr, err := humanEditsDiff(ctx, repoDir, cursor.LastPushedSHA, "FETCH_HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(diffStr) == "" {
		slog.Info("lessons: no human edits detected", "pr", prURL)
		return nil
	}

	if reviewGate == nil || !reviewGate.Enabled() {
		return errors.New("gemini review gate is not enabled/configured; cannot summarize human edits")
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

const (
	commitFieldSep  = "\x1f"
	commitRecordSep = "\x1e"
)

var noctraCommitRe = regexp.MustCompile(`(?m)^(Implemented by Noctra|Follow-up commit by Noctra|Autonomous maintenance by Noctra)\b`)

func humanEditsDiff(ctx context.Context, repoDir, base, head string) (string, error) {
	logCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "log", "--first-parent", "--no-merges", "--reverse",
		"--format=%H"+commitFieldSep+"%an"+commitFieldSep+"%B"+commitRecordSep, base+".."+head)
	var logOut bytes.Buffer
	logCmd.Stdout = &logOut
	if err := logCmd.Run(); err != nil {
		return "", fmt.Errorf("git log %s..%s: %w", base, head, err)
	}

	var diff strings.Builder
	for _, sha := range humanCommits(logOut.String()) {
		showCmd := exec.CommandContext(ctx, "git", "-C", repoDir, "show", "--format=", "--patch", sha)
		var showOut bytes.Buffer
		showCmd.Stdout = &showOut
		if err := showCmd.Run(); err != nil {
			return "", fmt.Errorf("git show %s: %w", sha, err)
		}
		diff.Write(showOut.Bytes())
	}
	return diff.String(), nil
}

func humanCommits(gitLog string) []string {
	var shas []string
	for _, record := range strings.Split(gitLog, commitRecordSep) {
		fields := strings.SplitN(strings.TrimLeft(record, "\n"), commitFieldSep, 3)
		if len(fields) != 3 || fields[0] == "" {
			continue
		}
		sha, author, body := fields[0], fields[1], fields[2]
		if strings.HasSuffix(author, "[bot]") || noctraCommitRe.MatchString(body) {
			continue
		}
		shas = append(shas, sha)
	}
	return shas
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
