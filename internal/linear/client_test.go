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
		query:      "project { name description content }",
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

func TestProjectIssueCounts_AggregatesAndOrders(t *testing.T) {
	// Issues arrive in arbitrary state order; the result must be grouped per
	// state and sorted by board position (Backlog=0 → In Review=3).
	state := func(name, typ string, pos float64) map[string]any {
		return map[string]any{"state": map[string]any{"name": name, "type": typ, "position": pos}}
	}
	resp := map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"nodes": []map[string]any{
					state("In Review", "started", 3),
					state("Backlog", "backlog", 0),
					state("Backlog", "backlog", 0),
					state("Next", "unstarted", 1),
					state("In Review", "started", 3),
					state("Next", "unstarted", 1),
					state("Next", "unstarted", 1),
				},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			},
		},
	}

	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "project: { name: { eq: $project } }",
		variables:  map[string]any{"project": "Noctra"},
	}, resp)

	counts, err := client.ProjectIssueCounts(context.Background(), "Noctra")
	if err != nil {
		t.Fatalf("ProjectIssueCounts: %v", err)
	}
	want := []StateCount{
		{State: "Backlog", Type: "backlog", Position: 0, Count: 2},
		{State: "Next", Type: "unstarted", Position: 1, Count: 3},
		{State: "In Review", Type: "started", Position: 3, Count: 2},
	}
	if len(counts) != len(want) {
		t.Fatalf("counts: got %d states, want %d (%+v)", len(counts), len(want), counts)
	}
	for i, w := range want {
		if counts[i].State != w.State || counts[i].Count != w.Count {
			t.Errorf("counts[%d]: got %+v, want %+v", i, counts[i], w)
		}
	}
}

func TestProjectIssueCounts_EmptyProject(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "issues(filter:",
		variables:  map[string]any{"project": "Ghost"},
	}, map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"nodes":    []map[string]any{},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			},
		},
	})

	counts, err := client.ProjectIssueCounts(context.Background(), "Ghost")
	if err != nil {
		t.Fatalf("ProjectIssueCounts: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("expected no states for empty project, got %+v", counts)
	}
}

func TestProjectIssueCounts_Paginates(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &got)
		page++
		if page == 1 {
			if _, ok := got.Variables["after"]; ok {
				t.Errorf("first page should not send an after cursor, got %v", got.Variables["after"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{"state": map[string]any{"name": "Next", "type": "unstarted", "position": 1.0}},
						},
						"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "CURSOR1"},
					},
				},
			})
			return
		}
		if got.Variables["after"] != "CURSOR1" {
			t.Errorf("second page after: got %v, want CURSOR1", got.Variables["after"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{
						{"state": map[string]any{"name": "Next", "type": "unstarted", "position": 1.0}},
					},
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := &Client{APIKey: "k", Endpoint: srv.URL, HTTP: srv.Client()}
	counts, err := client.ProjectIssueCounts(context.Background(), "Noctra")
	if err != nil {
		t.Fatalf("ProjectIssueCounts: %v", err)
	}
	if page != 2 {
		t.Errorf("expected 2 pages fetched, got %d", page)
	}
	if len(counts) != 1 || counts[0].Count != 2 {
		t.Errorf("expected Next=2 across both pages, got %+v", counts)
	}
}

func TestListProjectIssues_FiltersByState(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &got)
		gotQuery = got.Query
		if got.Variables["project"] != "Noctra" {
			t.Errorf("project var: got %v", got.Variables["project"])
		}
		if got.Variables["state"] != "Next" {
			t.Errorf("state var: got %v", got.Variables["state"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{
						{
							"id": "i1", "identifier": "ENG-7", "title": "Wire it up",
							"url":   "https://linear.app/x/issue/ENG-7",
							"state": map[string]any{"name": "Next", "type": "unstarted"},
						},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := &Client{APIKey: "k", Endpoint: srv.URL, HTTP: srv.Client()}
	issues, err := client.ListProjectIssues(context.Background(), "Noctra", "Next", 25)
	if err != nil {
		t.Fatalf("ListProjectIssues: %v", err)
	}
	if !strings.Contains(gotQuery, "state: { name: { eq: $state } }") {
		t.Errorf("query should filter by state when given, got: %s", gotQuery)
	}
	if len(issues) != 1 || issues[0].Identifier != "ENG-7" {
		t.Fatalf("issues: %+v", issues)
	}
	if issues[0].StateName() != "Next" {
		t.Errorf("state: got %q", issues[0].StateName())
	}
}

func TestListProjectIssues_NoStateOmitsFilter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &got)
		gotQuery = got.Query
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"issues": map[string]any{"nodes": []map[string]any{}}},
		})
	}))
	t.Cleanup(srv.Close)

	client := &Client{APIKey: "k", Endpoint: srv.URL, HTTP: srv.Client()}
	if _, err := client.ListProjectIssues(context.Background(), "Noctra", "", 25); err != nil {
		t.Fatalf("ListProjectIssues: %v", err)
	}
	if strings.Contains(gotQuery, "$state") {
		t.Errorf("query should not declare $state when no state given, got: %s", gotQuery)
	}
}

func TestSearchIssues_MatchesTitleOrDescription(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "containsIgnoreCase: $q",
		variables:  map[string]any{"q": "auth"},
	}, map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"nodes": []map[string]any{
					{
						"id": "s1", "identifier": "ENG-9", "title": "Auth flow",
						"url":   "https://linear.app/x/issue/ENG-9",
						"state": map[string]any{"name": "Backlog", "type": "backlog"},
					},
				},
			},
		},
	})

	issues, err := client.SearchIssues(context.Background(), "auth", 15)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Identifier != "ENG-9" {
		t.Fatalf("issues: %+v", issues)
	}
}

func TestGetIssueByIdentifier_LoadsStateAndAssignee(t *testing.T) {
	client := fakeServer(t, struct {
		authHeader string
		query      string
		variables  map[string]any
	}{
		authHeader: "test-key",
		query:      "assignee { name }",
		variables:  map[string]any{"id": "ENG-42"},
	}, map[string]any{
		"data": map[string]any{
			"issue": map[string]any{
				"id": "u42", "identifier": "ENG-42", "title": "Fix login",
				"url":      "https://linear.app/x/issue/ENG-42",
				"project":  map[string]any{"name": "Noctra"},
				"state":    map[string]any{"name": "In Review", "type": "started"},
				"assignee": map[string]any{"name": "Ada Lovelace"},
			},
		},
	})

	issue, err := client.GetIssueByIdentifier(context.Background(), "ENG-42")
	if err != nil {
		t.Fatalf("GetIssueByIdentifier: %v", err)
	}
	if issue.StateName() != "In Review" {
		t.Errorf("state: got %q", issue.StateName())
	}
	if issue.AssigneeName() != "Ada Lovelace" {
		t.Errorf("assignee: got %q", issue.AssigneeName())
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
		variables:  map[string]any{"label": "noctra"},
	}, resp)

	issues, err := client.FetchLabeledIssues(context.Background(), "noctra")
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
					{"id": "lbl_ns", "name": "noctra"},
					{"id": "lbl_feat", "name": "feature"},
				},
			},
		},
	})

	id, err := client.ResolveLabelID(context.Background(), "noctra")
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

	_, err := client.ResolveLabelID(context.Background(), "noctra")
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

func TestAuthHeader_KeyVsOAuthBearer(t *testing.T) {
	cases := []struct {
		name   string
		client *Client
		want   string
	}{
		{"personal API key sent verbatim", New("lin_api_xyz"), "lin_api_xyz"},
		{"OAuth token prefixed with Bearer", NewOAuth("lin_oauth_abc"), "Bearer lin_oauth_abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Authorization")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
			}))
			t.Cleanup(srv.Close)
			tc.client.Endpoint = srv.URL
			tc.client.HTTP = srv.Client()

			if err := tc.client.Do(context.Background(), "{ viewer { id } }", nil, nil); err != nil {
				t.Fatalf("Do: %v", err)
			}
			if got != tc.want {
				t.Errorf("Authorization header: got %q, want %q", got, tc.want)
			}
		})
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
