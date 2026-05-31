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

func TestPing_ReturnsViewerName(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "viewer",
	}, map[string]any{
		"data": map[string]any{
			"viewer": map[string]string{"id": "u_1", "name": "Ada Lovelace"},
		},
	})

	name, err := client.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if name != "Ada Lovelace" {
		t.Errorf("name: got %q, want %q", name, "Ada Lovelace")
	}
}

func TestPing_NoViewerIsAnError(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "bad",
		query:      "viewer",
	}, map[string]any{
		"data": map[string]any{"viewer": nil},
	})

	if _, err := client.Ping(context.Background()); err == nil {
		t.Fatal("expected error when viewer is empty, got nil")
	}
}

func TestFetchLabeledIssues_FiltersByLabel(t *testing.T) {
	resp := map[string]any{
		"data": map[string]any{
			"teams": map[string]any{
				"nodes": []map[string]any{{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{
								"id":          "lbl1",
								"identifier":  "ENG-10",
								"title":       "Labeled ticket",
								"description": "picked by label",
								"url":         "https://linear.app/x/issue/ENG-10",
								"project":     map[string]any{"name": "Web App"},
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
		query:      "labels: { name: { eq: $label } }",
		variables:  map[string]any{"label": "nightshift"},
	}, resp)

	issues, err := client.FetchLabeledIssues(context.Background(), "nightshift")
	if err != nil {
		t.Fatalf("FetchLabeledIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues: got %d, want 1", len(issues))
	}
	if issues[0].Identifier != "ENG-10" {
		t.Errorf("issue identifier: got %q, want %q", issues[0].Identifier, "ENG-10")
	}
	if issues[0].ProjectName() != "Web App" {
		t.Errorf("issue project: got %q, want %q", issues[0].ProjectName(), "Web App")
	}
}

func TestResolveLabelID_FindsLabel(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "issueLabels",
	}, map[string]any{
		"data": map[string]any{
			"issueLabels": map[string]any{
				"nodes": []map[string]any{
					{"id": "lbl_bug", "name": "bug"},
					{"id": "lbl_ns", "name": "nightshift"},
					{"id": "lbl_feat", "name": "feature"},
				},
			},
		},
	})

	id, err := client.ResolveLabelID(context.Background(), "nightshift")
	if err != nil {
		t.Fatalf("ResolveLabelID: %v", err)
	}
	if id != "lbl_ns" {
		t.Errorf("label ID: got %q, want %q", id, "lbl_ns")
	}
}

func TestResolveLabelID_NotFound(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "issueLabels",
	}, map[string]any{
		"data": map[string]any{
			"issueLabels": map[string]any{
				"nodes": []map[string]any{
					{"id": "lbl_bug", "name": "bug"},
				},
			},
		},
	})

	_, err := client.ResolveLabelID(context.Background(), "nightshift")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestRemoveLabel_RemovesTarget(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &got)

		callCount++
		if callCount == 1 {
			// First call: fetch current labels
			if !strings.Contains(got.Query, "labels { nodes { id } }") {
				t.Errorf("expected label fetch query, got: %s", got.Query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"labels": map[string]any{
							"nodes": []map[string]any{
								{"id": "lbl_keep"},
								{"id": "lbl_remove"},
								{"id": "lbl_also_keep"},
							},
						},
					},
				},
			})
		} else {
			// Second call: update with remaining labels
			if !strings.Contains(got.Query, "labelIds") {
				t.Errorf("expected labelIds mutation, got: %s", got.Query)
			}
			// Verify the removed label is absent
			labelIds, ok := got.Variables["labelIds"].([]any)
			if !ok {
				t.Fatalf("labelIds not an array: %T", got.Variables["labelIds"])
			}
			for _, lid := range labelIds {
				if lid == "lbl_remove" {
					t.Error("removed label should not be in labelIds")
				}
			}
			if len(labelIds) != 2 {
				t.Errorf("expected 2 remaining labels, got %d", len(labelIds))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
		}
	}))
	t.Cleanup(srv.Close)

	client := &Client{APIKey: "k", Endpoint: srv.URL, HTTP: srv.Client()}
	if err := client.RemoveLabel(context.Background(), "issue_1", "lbl_remove"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestResolveStateIDs_SkipsTriggerWhenEmpty(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "teams",
	}, map[string]any{
		"data": map[string]any{
			"teams": map[string]any{
				"nodes": []map[string]any{{
					"key": "ENG",
					"states": map[string]any{
						"nodes": []map[string]any{
							{"id": "s_next", "name": "Next"},
							{"id": "s_review", "name": "In Review"},
						},
					},
				}},
			},
		},
	})

	// With empty triggerName, only in-review is required.
	ids, err := client.ResolveStateIDs(context.Background(), "ENG", "", "In Review")
	if err != nil {
		t.Fatalf("ResolveStateIDs: %v", err)
	}
	if ids.Trigger != "" {
		t.Errorf("Trigger should be empty when triggerName is empty, got %q", ids.Trigger)
	}
	if ids.InReview != "s_review" {
		t.Errorf("InReview: got %q, want %q", ids.InReview, "s_review")
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
