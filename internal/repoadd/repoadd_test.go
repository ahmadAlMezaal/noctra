package repoadd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/onelastcommit/noctra/internal/linear"
	"github.com/onelastcommit/noctra/internal/repo"
)

type fakeCloner struct {
	gotRef, gotBranch string
	resolved          repo.Resolved
	err               error
}

func (c *fakeCloner) ResolveDirect(_ context.Context, ref, branch string) (repo.Resolved, error) {
	c.gotRef, c.gotBranch = ref, branch
	if c.err != nil {
		return repo.Resolved{}, c.err
	}
	return c.resolved, nil
}

type fakeProjects struct {
	list       []linear.Project
	listErr    error
	gotID      string
	gotContent string
	updateErr  error
}

func (p *fakeProjects) ListProjects(context.Context) ([]linear.Project, error) {
	return p.list, p.listErr
}

func (p *fakeProjects) UpdateProjectContent(_ context.Context, id, content string) error {
	p.gotID, p.gotContent = id, content
	return p.updateErr
}

func TestNormalizeRef(t *testing.T) {
	tests := []struct {
		input         string
		wantRef       string
		wantOwnerRepo string
		wantErr       bool
	}{
		{input: "acme/app", wantRef: "acme/app", wantOwnerRepo: "acme/app"},
		{input: "  acme/app  ", wantRef: "acme/app", wantOwnerRepo: "acme/app"},
		{input: "https://github.com/acme/app", wantRef: "https://github.com/acme/app", wantOwnerRepo: "acme/app"},
		{input: "git@github.com:acme/app.git", wantRef: "git@github.com:acme/app.git", wantOwnerRepo: "acme/app"},
		{input: "", wantErr: true},
		{input: "not a repo", wantErr: true},
	}

	for _, tt := range tests {
		ref, ownerRepo, err := NormalizeRef(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizeRef(%q) succeeded, want an error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeRef(%q): %v", tt.input, err)
			continue
		}
		if ref != tt.wantRef || ownerRepo != tt.wantOwnerRepo {
			t.Errorf("NormalizeRef(%q) = (%q, %q), want (%q, %q)",
				tt.input, ref, ownerRepo, tt.wantRef, tt.wantOwnerRepo)
		}
	}
}

func TestAdd_CloneOnlyWithoutProject(t *testing.T) {
	cloner := &fakeCloner{resolved: repo.Resolved{Path: "/repos/acme-app", MainBranch: "main"}}
	projects := &fakeProjects{}

	res, err := Add(context.Background(), cloner, projects, Request{Ref: "acme/app"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.OwnerRepo != "acme/app" || res.Path != "/repos/acme-app" || res.Branch != "main" {
		t.Errorf("result = %+v", res)
	}
	if res.Project != "" {
		t.Errorf("project = %q, want none", res.Project)
	}
	if projects.gotID != "" {
		t.Error("no project was requested, yet the directive was written")
	}
}

func TestAdd_WritesDirectivePreservingContent(t *testing.T) {
	cloner := &fakeCloner{resolved: repo.Resolved{Path: "/repos/acme-app", MainBranch: "staging"}}
	projects := &fakeProjects{}
	project := &linear.Project{ID: "p1", Name: "Noctra", Content: "Project notes.\n"}

	res, err := Add(context.Background(), cloner, projects, Request{
		Ref: "acme/app", Branch: "staging", Project: project,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Project != "Noctra" {
		t.Errorf("project = %q, want Noctra", res.Project)
	}
	if projects.gotID != "p1" {
		t.Errorf("updated project %q, want p1", projects.gotID)
	}
	if !strings.Contains(projects.gotContent, "Repo: acme/app") ||
		!strings.Contains(projects.gotContent, "Branch: staging") ||
		!strings.Contains(projects.gotContent, "Project notes.") {
		t.Errorf("content = %q, want the directive prepended above the existing notes", projects.gotContent)
	}
	if cloner.gotBranch != "staging" {
		t.Errorf("cloned with branch %q, want staging", cloner.gotBranch)
	}
}

func TestAdd_SkipsDirectiveWhenCloneFails(t *testing.T) {
	cloner := &fakeCloner{err: errors.New("no such remote")}
	projects := &fakeProjects{}

	_, err := Add(context.Background(), cloner, projects, Request{
		Ref: "acme/app", Project: &linear.Project{ID: "p1", Name: "Noctra"},
	})
	if err == nil {
		t.Fatal("Add succeeded despite a failed clone")
	}
	if projects.gotID != "" {
		t.Error("directive written for a repo that could not be cloned")
	}
}

func TestAdd_ReportsDirectiveFailureAfterSuccessfulClone(t *testing.T) {
	cloner := &fakeCloner{resolved: repo.Resolved{Path: "/repos/acme-app", MainBranch: "main"}}
	projects := &fakeProjects{updateErr: errors.New("forbidden")}

	res, err := Add(context.Background(), cloner, projects, Request{
		Ref: "acme/app", Project: &linear.Project{ID: "p1", Name: "Noctra"},
	})
	if err == nil {
		t.Fatal("Add succeeded despite a failed project update")
	}
	if res.Path == "" {
		t.Error("result lost the clone path, so callers can't report the partial success")
	}
	if res.Project != "" {
		t.Errorf("project = %q, want it unset when the directive failed", res.Project)
	}
}

func TestResolveProject(t *testing.T) {
	projects := &fakeProjects{list: []linear.Project{
		{ID: "p1", Name: "Noctra"},
		{ID: "p2", Name: "Noctra Site"},
	}}

	got, ambiguous, err := ResolveProject(context.Background(), projects, "Noctra")
	if err != nil || got == nil || got.ID != "p1" {
		t.Fatalf("exact match = (%+v, %v, %v), want p1", got, ambiguous, err)
	}

	got, ambiguous, err = ResolveProject(context.Background(), projects, "noct")
	if err != nil {
		t.Fatalf("substring match: %v", err)
	}
	if got != nil || len(ambiguous) != 2 {
		t.Errorf("substring match = (%+v, %d candidates), want 2 candidates and no pick", got, len(ambiguous))
	}

	if _, _, err = ResolveProject(context.Background(), projects, "missing"); err == nil {
		t.Error("a project that doesn't exist should be an error")
	}
}
