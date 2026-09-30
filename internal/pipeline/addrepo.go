package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/linear"
	"github.com/onelastcommit/noctra/internal/notify"
	"github.com/onelastcommit/noctra/internal/repoadd"
	"github.com/onelastcommit/noctra/internal/telegram"
)

const addRepoTimeout = 15 * time.Minute

const maxProjectChoices = 10

type addRepoStep int

const (
	stepRepo addRepoStep = iota
	stepProject
	stepBranch
)

type addRepoFlow struct {
	p    *Pipeline
	step addRepoStep

	ref       string
	ownerRepo string
	project   *linear.Project

	run func(ctx context.Context, req repoadd.Request)
}

func (p *Pipeline) startAddRepo(ctx context.Context, args string) (string, telegram.Conversation) {
	f := &addRepoFlow{p: p}
	f.run = f.runDetached

	if args = strings.TrimSpace(args); args != "" {
		reply, done := f.Answer(ctx, args)
		if done {
			return reply, nil
		}
		return reply, f
	}

	return "*Add a repository* — step 1 of 3\n\nSend the GitHub repo: `owner/name`, or a URL.\n\n_Reply /cancel to stop._", f
}

func (f *addRepoFlow) Answer(ctx context.Context, text string) (string, bool) {
	switch f.step {
	case stepRepo:
		return f.answerRepo(text)
	case stepProject:
		return f.answerProject(ctx, text)
	default:
		return f.answerBranch(ctx, text)
	}
}

func (f *addRepoFlow) answerRepo(text string) (string, bool) {
	ref, ownerRepo, err := repoadd.NormalizeRef(text)
	if err != nil {
		return fmt.Sprintf("That isn't a repository I can read: %s\n\nSend `owner/name`, or a URL like `https://github.com/owner/name`.",
			notify.EscapeMarkdown(err.Error())), false
	}

	f.ref, f.ownerRepo = ref, ownerRepo
	f.step = stepProject
	return fmt.Sprintf("✅ Repo: *%s*\n\n*Step 2 of 3* — send the Linear project whose tickets should land in this repo: its URL, or its name.\n\nReply `skip` to clone it without routing any tickets.",
		notify.EscapeMarkdown(ownerRepo)), false
}

func (f *addRepoFlow) answerProject(ctx context.Context, text string) (string, bool) {
	if isSkipAnswer(text) {
		f.step = stepBranch
		return branchPrompt(), false
	}

	if f.p.linear == nil {
		return "No Linear client is configured, so I can't look projects up. Reply `skip` to clone without routing.", false
	}

	project, ambiguous, err := repoadd.ResolveProject(ctx, f.p.linear, text)
	if err != nil {
		return fmt.Sprintf("%s\n\nSend its Linear URL or exact name, or reply `skip`.",
			notify.EscapeMarkdown(err.Error())), false
	}
	if project == nil {
		return ambiguousProjectReply(ambiguous), false
	}

	f.project = project
	f.step = stepBranch
	return fmt.Sprintf("✅ Project: *%s*\n\n%s", notify.EscapeMarkdown(project.Name), branchPrompt()), false
}

func (f *addRepoFlow) answerBranch(ctx context.Context, text string) (string, bool) {
	branch := strings.TrimSpace(text)
	if isDefaultAnswer(branch) {
		branch = ""
	}
	if strings.ContainsAny(branch, " \t") {
		return "A branch name can't contain spaces. Send one name, or reply `default`.", false
	}

	f.run(ctx, repoadd.Request{Ref: f.ref, Branch: branch, Project: f.project})

	return fmt.Sprintf("⏳ Cloning *%s*… I'll report back when it's in place.",
		notify.EscapeMarkdown(f.ownerRepo)), true
}

func (f *addRepoFlow) runDetached(ctx context.Context, req repoadd.Request) {
	p := f.p
	go func() {
		runCtx, cancel := context.WithTimeout(ctx, addRepoTimeout)
		defer cancel()
		p.notifier.Send(runCtx, p.finishAddRepo(runCtx, req))
	}()
}

func (p *Pipeline) finishAddRepo(ctx context.Context, req repoadd.Request) string {
	var projects repoadd.Projects
	if p.linear != nil {
		projects = p.linear
	}

	res, err := repoadd.Add(ctx, p.resolver, projects, req)
	if err != nil {
		slog.Warn("add repo failed", "ref", req.Ref, "err", err)
		if res.Path == "" {
			return fmt.Sprintf("❌ Could not add *%s*: %s",
				notify.EscapeMarkdown(req.Ref), notify.EscapeMarkdown(err.Error()))
		}
		return fmt.Sprintf("⚠️ Cloned *%s* to `%s`, but the Linear directive was not written: %s",
			notify.EscapeMarkdown(res.OwnerRepo), res.Path, notify.EscapeMarkdown(err.Error()))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "✅ Added *%s*\n\n", notify.EscapeMarkdown(res.OwnerRepo))
	fmt.Fprintf(&b, "Path: `%s`\n", res.Path)
	fmt.Fprintf(&b, "Base branch: `%s`\n", res.Branch)
	if res.Project != "" {
		fmt.Fprintf(&b, "Routing: tickets in *%s* now build here\n", notify.EscapeMarkdown(res.Project))
	} else {
		b.WriteString("Routing: none — add a `Repo:` directive to a Linear project to send tickets here\n")
	}
	if p.cfg != nil && p.cfg.SweepEnabled && len(p.cfg.SweepRepos) == 0 {
		b.WriteString("\n⚠️ Sweeps are on and cover every cloned repo, so maintenance PRs may now be opened against it.")
	}
	return b.String()
}

func branchPrompt() string {
	return "*Step 3 of 3* — which base branch should PRs target?\n\nReply `default` to use whatever the repo's default branch is."
}

func ambiguousProjectReply(matches []linear.Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d projects match.* Reply with the exact name, or paste the Linear URL:\n", len(matches))

	shown := matches
	if len(shown) > maxProjectChoices {
		shown = shown[:maxProjectChoices]
	}
	for _, m := range shown {
		fmt.Fprintf(&b, "• %s\n", notify.EscapeMarkdown(m.Name))
	}
	if len(matches) > len(shown) {
		fmt.Fprintf(&b, "…and %d more\n", len(matches)-len(shown))
	}
	return b.String()
}

func isSkipAnswer(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "skip", "none", "no":
		return true
	}
	return false
}

func isDefaultAnswer(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "default", "-":
		return true
	}
	return false
}
