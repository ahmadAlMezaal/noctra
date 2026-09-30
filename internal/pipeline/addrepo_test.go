package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/linear"
	"github.com/onelastcommit/noctra/internal/repoadd"
)

func projectsServer(t *testing.T, projects []linear.Project) (*linear.Client, *[]string) {
	t.Helper()

	var mutations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(body.Query, "projectUpdate") {
			mutations = append(mutations, body.Query)
			_, _ = w.Write([]byte(`{"data":{"projectUpdate":{"success":true}}}`))
			return
		}
		resp := map[string]any{"data": map[string]any{"projects": map[string]any{
			"nodes":    projects,
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	client := linear.New("test-key")
	client.Endpoint = srv.URL
	return client, &mutations
}

func newAddRepoFlow(p *Pipeline) (*addRepoFlow, *[]repoadd.Request) {
	var got []repoadd.Request
	f := &addRepoFlow{p: p}
	f.run = func(_ context.Context, req repoadd.Request) { got = append(got, req) }
	return f, &got
}

func TestAddRepoFlow_HappyPath(t *testing.T) {
	client, _ := projectsServer(t, []linear.Project{
		{ID: "p1", Name: "Noctra", SlugID: "abc123", URL: "https://linear.app/acme/project/noctra-abc123"},
	})
	f, requests := newAddRepoFlow(&Pipeline{cfg: &config.Config{}, linear: client})
	ctx := context.Background()

	reply, done := f.Answer(ctx, "acme/app")
	if done || !strings.Contains(reply, "acme/app") {
		t.Fatalf("repo step: reply=%q done=%v", reply, done)
	}

	reply, done = f.Answer(ctx, "https://linear.app/acme/project/noctra-abc123")
	if done || !strings.Contains(reply, "Noctra") {
		t.Fatalf("project step: reply=%q done=%v", reply, done)
	}

	reply, done = f.Answer(ctx, "staging")
	if !done {
		t.Fatalf("branch step should finish the flow, reply=%q", reply)
	}

	if len(*requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(*requests))
	}
	req := (*requests)[0]
	if req.Ref != "acme/app" || req.Branch != "staging" {
		t.Errorf("request = %+v, want ref acme/app on staging", req)
	}
	if req.Project == nil || req.Project.ID != "p1" {
		t.Errorf("request project = %+v, want the resolved project p1", req.Project)
	}
}

func TestAddRepoFlow_RejectsBadRepoAndStays(t *testing.T) {
	f, requests := newAddRepoFlow(&Pipeline{cfg: &config.Config{}})

	reply, done := f.Answer(context.Background(), "not a repo at all")
	if done {
		t.Fatal("a bad repo should not finish the flow")
	}
	if !strings.Contains(reply, "owner/name") {
		t.Errorf("reply = %q, want it to show the expected format", reply)
	}
	if f.step != stepRepo {
		t.Errorf("step = %v, want to stay on the repo step", f.step)
	}
	if len(*requests) != 0 {
		t.Errorf("a rejected repo still ran the add")
	}
}

func TestAddRepoFlow_SkipLeavesProjectUnset(t *testing.T) {
	f, requests := newAddRepoFlow(&Pipeline{cfg: &config.Config{}})
	ctx := context.Background()

	f.Answer(ctx, "acme/app")
	if _, done := f.Answer(ctx, "skip"); done {
		t.Fatal("skip should advance to the branch step, not finish")
	}
	f.Answer(ctx, "default")

	if len(*requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(*requests))
	}
	req := (*requests)[0]
	if req.Project != nil {
		t.Errorf("project = %+v, want none after skip", req.Project)
	}
	if req.Branch != "" {
		t.Errorf("branch = %q, want empty so the repo default is used", req.Branch)
	}
}

func TestAddRepoFlow_AmbiguousProjectListsCandidates(t *testing.T) {
	client, _ := projectsServer(t, []linear.Project{
		{ID: "p1", Name: "Noctra"},
		{ID: "p2", Name: "Noctra Site"},
	})
	f, _ := newAddRepoFlow(&Pipeline{cfg: &config.Config{}, linear: client})
	ctx := context.Background()

	f.Answer(ctx, "acme/app")
	reply, done := f.Answer(ctx, "noct")
	if done {
		t.Fatal("an ambiguous project should not finish the flow")
	}
	for _, want := range []string{"Noctra", "Noctra Site"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply = %q, want it to list %q", reply, want)
		}
	}
	if f.step != stepProject {
		t.Errorf("step = %v, want to stay on the project step", f.step)
	}
}

func TestAddRepoFlow_UnknownProjectStays(t *testing.T) {
	client, _ := projectsServer(t, []linear.Project{{ID: "p1", Name: "Noctra"}})
	f, _ := newAddRepoFlow(&Pipeline{cfg: &config.Config{}, linear: client})
	ctx := context.Background()

	f.Answer(ctx, "acme/app")
	reply, done := f.Answer(ctx, "does-not-exist")
	if done {
		t.Fatal("an unknown project should not finish the flow")
	}
	if !strings.Contains(reply, "skip") {
		t.Errorf("reply = %q, want it to offer skip as a way out", reply)
	}
}

func TestAddRepoFlow_RejectsBranchWithSpaces(t *testing.T) {
	f, requests := newAddRepoFlow(&Pipeline{cfg: &config.Config{}})
	ctx := context.Background()

	f.Answer(ctx, "acme/app")
	f.Answer(ctx, "skip")
	if _, done := f.Answer(ctx, "my branch"); done {
		t.Fatal("an invalid branch should not finish the flow")
	}
	if len(*requests) != 0 {
		t.Errorf("an invalid branch still ran the add")
	}
}

func TestStartAddRepo_ArgsSkipTheFirstPrompt(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{}}
	reply, conv := p.startAddRepo(context.Background(), "acme/app")
	if conv == nil {
		t.Fatal("a valid repo argument should still open the flow")
	}
	if !strings.Contains(reply, "Step 2") {
		t.Errorf("reply = %q, want it to land on step 2", reply)
	}
}

func TestFinishAddRepo_ReportsFailure(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{}}

	msg := p.finishAddRepo(context.Background(), repoadd.Request{Ref: "not a repo"})
	if !strings.Contains(msg, "Could not add") {
		t.Errorf("message = %q, want a failure report", msg)
	}
}
