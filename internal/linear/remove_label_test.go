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

func TestRemoveLabelSendsEmptyArrayWhenItWasTheOnlyLabel(t *testing.T) {
	var mutationVars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &got)

		if strings.Contains(got.Query, "issueUpdate") {
			mutationVars = got.Variables
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"issueUpdate": map[string]any{"success": true}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"labels": map[string]any{
						"nodes": []map[string]any{{"id": "trigger-label"}},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	c := &Client{APIKey: "k", Endpoint: srv.URL, HTTP: srv.Client()}
	if err := c.RemoveLabel(context.Background(), "issue-1", "trigger-label"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}

	got, ok := mutationVars["labelIds"]
	if !ok {
		t.Fatal("mutation did not send labelIds")
	}
	if got == nil {
		t.Fatal("labelIds was null; Linear rejects null for [String!]! and the trigger label is never removed")
	}
	if ids, isSlice := got.([]any); !isSlice || len(ids) != 0 {
		t.Fatalf("labelIds = %#v, want an empty array", got)
	}
}
