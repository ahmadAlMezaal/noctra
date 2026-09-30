package reposcmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/linear"
	"github.com/onelastcommit/noctra/internal/linearclient"
	"github.com/onelastcommit/noctra/internal/repo"
	"github.com/onelastcommit/noctra/internal/repoadd"
	"github.com/onelastcommit/noctra/internal/state"
)

const addTimeout = 15 * time.Minute

const maxProjectAttempts = 3

func Run(scriptDir string, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "add":
		return runAdd(scriptDir, args[1:])
	case "list":
		return runList(scriptDir)
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown repos subcommand %q", args[0])
	}
}

func printUsage() {
	fmt.Println("Usage: noctra repos <subcommand>")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  add [repo] [--project <name|url>] [--branch <name>]")
	fmt.Println("            Clone a repo and point a Linear project at it (prompts for anything omitted)")
	fmt.Println("  list      List the repos Noctra has cloned")
}

type addOptions struct {
	Ref     string
	Project string
	Branch  string
}

func parseAddArgs(args []string) (addOptions, error) {
	var opts addOptions

	value := func(i *int, flag string) (string, error) {
		*i++
		if *i >= len(args) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		return args[*i], nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		var err error
		switch {
		case arg == "--project" || arg == "-p":
			opts.Project, err = value(&i, arg)
		case strings.HasPrefix(arg, "--project="):
			opts.Project = strings.TrimPrefix(arg, "--project=")
		case arg == "--branch" || arg == "-b":
			opts.Branch, err = value(&i, arg)
		case strings.HasPrefix(arg, "--branch="):
			opts.Branch = strings.TrimPrefix(arg, "--branch=")
		case strings.HasPrefix(arg, "-"):
			err = fmt.Errorf("unknown flag %q", arg)
		case opts.Ref != "":
			err = fmt.Errorf("unexpected argument %q", arg)
		default:
			opts.Ref = arg
		}
		if err != nil {
			return addOptions{}, err
		}
	}
	return opts, nil
}

func runAdd(scriptDir string, args []string) error {
	opts, err := parseAddArgs(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load(scriptDir)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), addTimeout)
	defer cancel()

	in := bufio.NewScanner(os.Stdin)

	if opts.Ref == "" {
		if opts.Ref, err = prompt(in, "GitHub repo (owner/name or URL): "); err != nil {
			return err
		}
	}
	ref, ownerRepo, err := repoadd.NormalizeRef(opts.Ref)
	if err != nil {
		return err
	}

	store, err := state.OpenMigrating(cfg.StateDB, cfg.StateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open the state DB (%v); a rotated Linear token won't be persisted\n", err)
	} else {
		defer func() { _ = store.Close() }()
	}
	projects := linearclient.New(cfg, store)

	if opts.Project == "" {
		if opts.Project, err = prompt(in, "Linear project (URL or name, blank to skip routing): "); err != nil {
			return err
		}
	}

	var project *linear.Project
	if opts.Project != "" && !isSkip(opts.Project) {
		if project, err = resolveProject(ctx, projects, in, opts.Project); err != nil {
			return err
		}
	}

	if opts.Branch == "" {
		if opts.Branch, err = prompt(in, "Base branch (blank for the repo's default): "); err != nil {
			return err
		}
	}

	fmt.Printf("\nCloning %s…\n", ownerRepo)
	res, addErr := repoadd.Add(ctx, repo.FromConfig(cfg), projects, repoadd.Request{
		Ref:     ref,
		Branch:  opts.Branch,
		Project: project,
	})
	if addErr != nil && res.Path == "" {
		return addErr
	}

	fmt.Printf("\n✅ %s\n", res.OwnerRepo)
	fmt.Printf("   Path:        %s\n", res.Path)
	fmt.Printf("   Base branch: %s\n", res.Branch)
	if res.Project != "" {
		fmt.Printf("   Routing:     tickets in %q build here\n", res.Project)
	} else {
		fmt.Println("   Routing:     none — add a `Repo:` directive to a Linear project to send tickets here")
	}
	if cfg.SweepEnabled && len(cfg.SweepRepos) == 0 {
		fmt.Println("\n⚠️  Sweeps are on and cover every cloned repo, so maintenance PRs may now be opened against it.")
	}
	return addErr
}

func resolveProject(ctx context.Context, projects repoadd.Projects, in *bufio.Scanner, query string) (*linear.Project, error) {
	for attempt := 0; attempt < maxProjectAttempts; attempt++ {
		project, ambiguous, err := repoadd.ResolveProject(ctx, projects, query)
		if err != nil {
			return nil, err
		}
		if project != nil {
			return project, nil
		}

		fmt.Printf("%d projects match %q:\n", len(ambiguous), query)
		for _, p := range ambiguous {
			fmt.Printf("  • %s\n", p.Name)
		}
		if query, err = prompt(in, "Exact name or Linear URL: "); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not narrow %q to a single Linear project", query)
}

func runList(scriptDir string) error {
	cfg, err := config.Load(scriptDir)
	if err != nil {
		return err
	}

	ctx := context.Background()
	paths := repo.FromConfig(cfg).AllRepoPaths()
	if len(paths) == 0 {
		fmt.Println("No repos cloned yet. Add one with `noctra repos add`.")
		return nil
	}

	for _, p := range paths {
		if remote := repo.OriginRemoteOf(ctx, p); remote != "" {
			fmt.Printf("%s\n  %s\n", remote, p)
			continue
		}
		fmt.Println(p)
	}
	return nil
}

func isSkip(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "skip", "none", "no":
		return true
	}
	return false
}

func prompt(in *bufio.Scanner, label string) (string, error) {
	fmt.Print(label)
	if !in.Scan() {
		if err := in.Err(); err != nil {
			return "", err
		}
		return "", errors.New("no input read from stdin")
	}
	return strings.TrimSpace(in.Text()), nil
}
