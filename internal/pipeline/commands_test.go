package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
	"github.com/ahmadAlMezaal/nightshift/internal/linear"
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
			TriggerLabel:  "nightshift",
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
