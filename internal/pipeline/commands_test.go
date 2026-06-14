package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
)

func TestNormalizeIdentifier(t *testing.T) {
	tests := []struct {
		input, teamKey, want string
	}{
		{"ENG-42", "ENG", "ENG-42"},
		{"eng-42", "ENG", "ENG-42"},
		{"42", "ENG", "ENG-42"},
		{"  ENG-42 ", "ENG", "ENG-42"},
		{"42", "PROJ", "PROJ-42"},
		{"proj-7", "PROJ", "PROJ-7"},
		{"", "ENG", ""},
		{"  ", "ENG", ""},
	}
	for _, tt := range tests {
		got := normalizeIdentifier(tt.input, tt.teamKey)
		if got != tt.want {
			t.Errorf("normalizeIdentifier(%q, %q) = %q, want %q",
				tt.input, tt.teamKey, got, tt.want)
		}
	}
}

func TestHandleStatus_Idle(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{
			MaxConcurrent: 3,
			MaxDispatches: 10,
		},
		active:       map[string]struct{}{},
		sessionStart: time.Now().Add(-5 * time.Minute),
	}
	reply := p.handleStatus(context.Background(), "")
	if !strings.Contains(reply, "0/3") {
		t.Errorf("expected 0/3 in reply, got %q", reply)
	}
	if !strings.Contains(reply, "idle") {
		t.Errorf("expected 'idle' in reply, got %q", reply)
	}
}

func TestHandleStatus_Active(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{
			MaxConcurrent: 3,
			MaxDispatches: 10,
		},
		active: map[string]struct{}{
			"ENG-42": {},
			"ENG-44": {},
		},
		successCount:    2,
		failCount:       1,
		totalDispatches: 5,
		sessionStart:    time.Now().Add(-1 * time.Hour),
	}
	reply := p.handleStatus(context.Background(), "")
	if !strings.Contains(reply, "2/3") {
		t.Errorf("expected 2/3 in reply, got %q", reply)
	}
	if !strings.Contains(reply, "ENG-42") {
		t.Errorf("expected ENG-42 in reply, got %q", reply)
	}
	if !strings.Contains(reply, "ENG-44") {
		t.Errorf("expected ENG-44 in reply, got %q", reply)
	}
	if !strings.Contains(reply, "2 PRs created") {
		t.Errorf("expected success count in reply, got %q", reply)
	}
	if !strings.Contains(reply, "1 failed") {
		t.Errorf("expected fail count in reply, got %q", reply)
	}
	if !strings.Contains(reply, "5/10") {
		t.Errorf("expected dispatch count in reply, got %q", reply)
	}
}

func TestHandleKill_NoArgs(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{LinearTeamKey: "ENG"},
	}
	reply := p.handleKill(context.Background(), "")
	if !strings.Contains(reply, "Usage") {
		t.Errorf("expected usage in reply, got %q", reply)
	}
}

func TestHandleKill_NotActive(t *testing.T) {
	p := &Pipeline{
		cfg:     &config.Config{LinearTeamKey: "ENG"},
		active:  map[string]struct{}{},
		cancels: map[string]context.CancelFunc{},
		killed:  map[string]struct{}{},
	}
	reply := p.handleKill(context.Background(), "ENG-42")
	if !strings.Contains(reply, "no active run") {
		t.Errorf("expected 'no active run' in reply, got %q", reply)
	}
}

func TestHandleKill_Active(t *testing.T) {
	ticketCtx, ticketCancel := context.WithCancel(context.Background())
	defer ticketCancel()

	p := &Pipeline{
		cfg:     &config.Config{LinearTeamKey: "ENG"},
		active:  map[string]struct{}{"ENG-42": {}},
		cancels: map[string]context.CancelFunc{"ENG-42": ticketCancel},
		killed:  map[string]struct{}{},
	}
	reply := p.handleKill(context.Background(), "ENG-42")
	if !strings.Contains(reply, "Killed") {
		t.Errorf("expected 'Killed' in reply, got %q", reply)
	}
	// Verify the context was cancelled.
	select {
	case <-ticketCtx.Done():
		// expected
	default:
		t.Error("expected ticket context to be cancelled after kill")
	}
}

func TestHandleKill_CaseInsensitive(t *testing.T) {
	_, ticketCancel := context.WithCancel(context.Background())
	defer ticketCancel()

	p := &Pipeline{
		cfg:     &config.Config{LinearTeamKey: "ENG"},
		active:  map[string]struct{}{"ENG-42": {}},
		cancels: map[string]context.CancelFunc{"ENG-42": ticketCancel},
		killed:  map[string]struct{}{},
	}
	reply := p.handleKill(context.Background(), "eng-42")
	if !strings.Contains(reply, "Killed") {
		t.Errorf("expected 'Killed' in reply for case-insensitive input, got %q", reply)
	}
}

func TestHandleKill_NumberOnly(t *testing.T) {
	_, ticketCancel := context.WithCancel(context.Background())
	defer ticketCancel()

	p := &Pipeline{
		cfg:     &config.Config{LinearTeamKey: "ENG"},
		active:  map[string]struct{}{"ENG-42": {}},
		cancels: map[string]context.CancelFunc{"ENG-42": ticketCancel},
		killed:  map[string]struct{}{},
	}
	reply := p.handleKill(context.Background(), "42")
	if !strings.Contains(reply, "Killed") {
		t.Errorf("expected 'Killed' for number-only input, got %q", reply)
	}
}

func TestHandleRequeue_NoArgs(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{LinearTeamKey: "ENG"},
	}
	reply := p.handleRequeue(context.Background(), "")
	if !strings.Contains(reply, "Usage") {
		t.Errorf("expected usage in reply, got %q", reply)
	}
}

func TestHandleRequeue_TicketNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"issue": nil},
		})
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{
		cfg:    &config.Config{LinearTeamKey: "ENG"},
		linear: client,
	}
	reply := p.handleRequeue(context.Background(), "ENG-99")
	if !strings.Contains(reply, "Could not find") {
		t.Errorf("expected 'Could not find' in reply, got %q", reply)
	}
}

func TestHandleRequeue_StateMode(t *testing.T) {
	var commented, stateChanged bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "issue(id:"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":         "uuid-42",
						"identifier": "ENG-42",
						"title":      "Fix login",
						"url":        "https://linear.app/eng/issue/ENG-42",
					},
				},
			})
		case strings.Contains(req.Query, "commentCreate"):
			commented = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"commentCreate": map[string]any{"success": true},
				},
			})
		case strings.Contains(req.Query, "issueUpdate"):
			stateChanged = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
		}
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{
		cfg: &config.Config{
			LinearTeamKey: "ENG",
			TriggerMode:   "state",
		},
		linear: client,
		states: linear.StateIDs{Trigger: "state-trigger-id"},
	}

	reply := p.handleRequeue(context.Background(), "ENG-42 use Auth0 instead of Cognito")
	if !strings.Contains(reply, "requeued") {
		t.Errorf("expected 'requeued' in reply, got %q", reply)
	}
	if !commented {
		t.Error("expected a comment to be posted on Linear")
	}
	if !stateChanged {
		t.Error("expected state to be changed on Linear")
	}
	if !strings.Contains(reply, "Auth0") {
		t.Errorf("expected context snippet in reply, got %q", reply)
	}
}

func TestHandleRequeue_LabelMode(t *testing.T) {
	var addedLabel bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "issue(id:") && strings.Contains(req.Query, "labels"):
			// AddLabel: fetch current labels
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"labels": map[string]any{
							"nodes": []map[string]any{},
						},
					},
				},
			})
		case strings.Contains(req.Query, "issue(id:"):
			// GetIssueByIdentifier
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":         "uuid-42",
						"identifier": "ENG-42",
						"title":      "Fix login",
						"url":        "https://linear.app/eng/issue/ENG-42",
					},
				},
			})
		case strings.Contains(req.Query, "commentCreate"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"commentCreate": map[string]any{"success": true},
				},
			})
		case strings.Contains(req.Query, "issueUpdate"):
			addedLabel = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
		}
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{
		cfg: &config.Config{
			LinearTeamKey: "ENG",
			TriggerMode:   "label",
			TriggerLabel:  "noctra",
		},
		linear:  client,
		labelID: "label-id-123",
	}

	reply := p.handleRequeue(context.Background(), "42 fix auth")
	if !strings.Contains(reply, "requeued") {
		t.Errorf("expected 'requeued' in reply, got %q", reply)
	}
	if !addedLabel {
		t.Error("expected trigger label to be added in label mode")
	}
}

func TestHandleRequeue_NoContext(t *testing.T) {
	var commented bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "issue(id:"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":         "uuid-42",
						"identifier": "ENG-42",
						"title":      "Fix login",
						"url":        "https://linear.app/eng/issue/ENG-42",
					},
				},
			})
		case strings.Contains(req.Query, "commentCreate"):
			commented = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"commentCreate": map[string]any{"success": true},
				},
			})
		case strings.Contains(req.Query, "issueUpdate"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
		}
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{
		cfg: &config.Config{
			LinearTeamKey: "ENG",
			TriggerMode:   "state",
		},
		linear: client,
		states: linear.StateIDs{Trigger: "state-trigger-id"},
	}

	// Requeue without extra context — should skip comment.
	reply := p.handleRequeue(context.Background(), "ENG-42")
	if !strings.Contains(reply, "requeued") {
		t.Errorf("expected 'requeued' in reply, got %q", reply)
	}
	if commented {
		t.Error("expected no comment when no extra context is given")
	}
	if strings.Contains(reply, "Context added") {
		t.Errorf("reply should not mention context when none given, got %q", reply)
	}
}

// ticketCountServer returns a Linear server that answers the ProjectIssueCounts
// query with two states (Backlog x2, Next x1) and records the requested project.
func ticketCountServer(t *testing.T, gotProject *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if gotProject != nil {
			if p, ok := req.Variables["project"].(string); ok {
				*gotProject = p
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{
						{"state": map[string]any{"name": "Backlog", "type": "backlog", "position": 0.0}},
						{"state": map[string]any{"name": "Backlog", "type": "backlog", "position": 0.0}},
						{"state": map[string]any{"name": "Next", "type": "unstarted", "position": 1.0}},
					},
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				},
			},
		})
	}))
	return srv
}

func TestHandleTickets_NamedProject(t *testing.T) {
	var gotProject string
	srv := ticketCountServer(t, &gotProject)
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{cfg: &config.Config{}, linear: client}
	reply := p.handleTickets(context.Background(), "Noctra")

	if gotProject != "Noctra" {
		t.Errorf("queried project: got %q, want %q", gotProject, "Noctra")
	}
	if !strings.Contains(reply, "Noctra") {
		t.Errorf("expected project name in reply, got %q", reply)
	}
	if !strings.Contains(reply, "3 total") {
		t.Errorf("expected '3 total' in reply, got %q", reply)
	}
	if !strings.Contains(reply, "Backlog: 2") {
		t.Errorf("expected 'Backlog: 2' in reply, got %q", reply)
	}
	if !strings.Contains(reply, "Next: 1") {
		t.Errorf("expected 'Next: 1' in reply, got %q", reply)
	}
}

func TestHandleTickets_NoArgs(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{}}
	reply := p.handleTickets(context.Background(), "")
	if !strings.Contains(reply, "Usage") {
		t.Errorf("expected usage in reply, got %q", reply)
	}
}

func TestHandleTicket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"id": "u42", "identifier": "ENG-42", "title": "Fix login",
					"description": "Some details about the login bug.",
					"url":         "https://linear.app/eng/issue/ENG-42",
					"project":     map[string]any{"name": "Noctra"},
					"state":       map[string]any{"name": "In Review", "type": "started"},
					"assignee":    map[string]any{"name": "Ada Lovelace"},
				},
			},
		})
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{cfg: &config.Config{LinearTeamKey: "ENG"}, linear: client}
	reply := p.handleTicket(context.Background(), "42") // number-only → ENG-42
	for _, want := range []string{"ENG-42", "Fix login", "In Review", "Noctra", "Ada Lovelace"} {
		if !strings.Contains(reply, want) {
			t.Errorf("expected %q in reply, got %q", want, reply)
		}
	}
}

func TestHandleTicket_NoArgs(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{LinearTeamKey: "ENG"}}
	if reply := p.handleTicket(context.Background(), ""); !strings.Contains(reply, "Usage") {
		t.Errorf("expected usage, got %q", reply)
	}
}

func TestHandleSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{
						{"id": "s1", "identifier": "ENG-9", "title": "Auth flow",
							"state": map[string]any{"name": "Backlog", "type": "backlog"}},
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{cfg: &config.Config{}, linear: client}
	reply := p.handleSearch(context.Background(), "auth")
	if !strings.Contains(reply, "ENG-9") || !strings.Contains(reply, "Backlog") {
		t.Errorf("expected search result in reply, got %q", reply)
	}
}

func TestHandleSearch_NoArgs(t *testing.T) {
	p := &Pipeline{cfg: &config.Config{}}
	if reply := p.handleSearch(context.Background(), ""); !strings.Contains(reply, "Usage") {
		t.Errorf("expected usage, got %q", reply)
	}
}

func TestSnippet(t *testing.T) {
	if got := snippet("  short  ", 280); got != "short" {
		t.Errorf("snippet trim: got %q", got)
	}
	long := strings.Repeat("a", 300)
	got := snippet(long, 280)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis on truncation, got %q", got)
	}
	if len([]rune(got)) != 281 { // 280 runes + ellipsis
		t.Errorf("expected 280 runes + ellipsis, got %d runes", len([]rune(got)))
	}
}

func TestHandleStatus_UptimeFormat(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{
			MaxConcurrent: 5,
			MaxDispatches: 20,
		},
		active:       map[string]struct{}{},
		sessionStart: time.Now().Add(-2*time.Hour - 30*time.Minute),
	}
	reply := p.handleStatus(context.Background(), "")
	if !strings.Contains(reply, "Uptime") {
		t.Errorf("expected uptime in reply, got %q", reply)
	}
}
