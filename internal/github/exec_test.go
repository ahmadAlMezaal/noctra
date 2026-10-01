package github

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestCommand_TokenModeLeavesEnvironmentAlone(t *testing.T) {
	SetTokenSource(nil)
	cmd, err := Command(context.Background(), "", "pr", "list")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Env != nil {
		t.Fatalf("token mode must inherit the environment, got %v", cmd.Env)
	}
}

func TestCommand_AppModeInjectsRepoToken(t *testing.T) {
	var asked string
	SetTokenSource(func(_ context.Context, ownerRepo string) (string, error) {
		asked = ownerRepo
		return "ghs_for_" + ownerRepo, nil
	})
	t.Cleanup(func() { SetTokenSource(nil) })

	cmd, err := Command(context.Background(), "o/r", "pr", "view", "1")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "o/r" || !slices.Contains(cmd.Env, "GH_TOKEN=ghs_for_o/r") {
		t.Fatalf("asked=%q env has token: %v", asked, slices.Contains(cmd.Env, "GH_TOKEN=ghs_for_o/r"))
	}
}

func TestCommand_AppModeFailsClosed(t *testing.T) {
	SetTokenSource(func(context.Context, string) (string, error) { return "", errors.New("not installed") })
	t.Cleanup(func() { SetTokenSource(nil) })

	if _, err := Command(context.Background(), "o/r", "pr", "list"); err == nil {
		t.Fatal("a failed mint must not fall back to ambient gh auth")
	}
	if _, err := Command(context.Background(), "", "pr", "list"); err == nil {
		t.Fatal("an unknown repository must be an error in app mode")
	}
}

func TestOwnerRepoOfPR(t *testing.T) {
	got, err := OwnerRepoOfPR("https://github.com/onelastcommit/noctra/pull/284")
	if err != nil || got != "onelastcommit/noctra" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := OwnerRepoOfPR("https://github.com/onelastcommit/noctra"); err == nil {
		t.Fatal("non-PR URL should error")
	}
}
