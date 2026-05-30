package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer spins up an httptest server that decodes the GraphQL request and
// returns the given response body.
func fakeServer(t *testing.T, want struct {
	authHeader string
	query      string
	variables  map[string]any
}, response any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want.authHeader {
			t.Errorf("Authorization: got %q, want %q", r.Header.Get("Authorization"), want.authHeader)
		}
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if !strings.Contains(got.Query, want.query) {
			t.Errorf("query missing substring %q\nfull: %s", want.query, got.Query)
		}
		for k, v := range want.variables {
			if got.Variables[k] != v {
				t.Errorf("variable %s: got %v, want %v", k, got.Variables[k], v)
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(srv.Close)

	return &Client{APIKey: want.authHeader, Endpoint: srv.URL, HTTP: srv.Client()}
}

func TestFetchTriggerIssues_ParsesProject(t *testing.T) {
	resp := map[string]any{
		"data": map[string]any{
			"teams": map[string]any{
				"nodes": []map[string]any{{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{
								"id":          "abc",
								"identifier":  "ENG-1",
								"title":       "Fix it",
								"description": "details",
								"url":         "https://linear.app/x/issue/ENG-1",
								"project":     map[string]any{"name": "Auth Service"},
							},
							{
								"id":          "def",
								"identifier":  "ENG-2",
								"title":       "No project ticket",
								"description": "",
								"url":         "https://linear.app/x/issue/ENG-2",
								"project":     nil,
							},
						},
					},
				}},
			},
		},
	}

	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "project { name }",
		variables:  map[string]any{"state": "Next"},
	}, resp)

	issues, err := client.FetchTriggerIssues(context.Background(), "Next")
	if err != nil {
		t.Fatalf("FetchTriggerIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues: got %d, want 2", len(issues))
	}
	if issues[0].ProjectName() != "Auth Service" {
		t.Errorf("issue[0] project: %q", issues[0].ProjectName())
	}
	if issues[1].ProjectName() != "" {
		t.Errorf("issue[1] project should be empty, got %q", issues[1].ProjectName())
	}
}

func TestDo_SurfacesGraphQLErrors(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "k",
		query:      "{",
	}, map[string]any{
		"errors": []map[string]string{
			{"message": "Argument 'foo' missing"},
			{"message": "Invalid state"},
		},
	})

	err := client.Do(context.Background(), "{ noop }", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Invalid state") {
		t.Fatalf("expected graphql error surfaced, got %v", err)
	}
}
