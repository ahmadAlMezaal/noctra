package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

// testStore opens a throwaway SQLite store in t.TempDir().
func testStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "test-state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	return s
}

func TestNeedsPlanConfirm_GlobalEnabled(t *testing.T) {
	p := &Pipeline{
		cfg:   &config.Config{PlanConfirm: true, PlanConfirmLabel: "plan-first"},
		store: testStore(t),
	}
	issue := linear.Issue{Identifier: "ENG-1", Labels: linear.LabelConnection{}}
	if !p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=true when PlanConfirm is globally enabled")
	}
}

func TestNeedsPlanConfirm_LabelActivation(t *testing.T) {
	p := &Pipeline{
		cfg:   &config.Config{PlanConfirm: false, PlanConfirmLabel: "plan-first"},
		store: testStore(t),
	}
	issue := linear.Issue{
		Identifier: "ENG-2",
		Labels:     linear.LabelConnection{Nodes: []linear.Label{{Name: "plan-first"}}},
	}
	if !p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=true when issue has plan-first label")
	}
}

func TestNeedsPlanConfirm_NoActivation(t *testing.T) {
	p := &Pipeline{
		cfg:   &config.Config{PlanConfirm: false, PlanConfirmLabel: "plan-first"},
		store: testStore(t),
	}
	issue := linear.Issue{
		Identifier: "ENG-3",
		Labels:     linear.LabelConnection{Nodes: []linear.Label{{Name: "bug"}}},
	}
	if p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=false when feature is off and label is absent")
	}
}

func TestNeedsPlanConfirm_NilStore(t *testing.T) {
	p := &Pipeline{
		cfg:   &config.Config{PlanConfirm: true, PlanConfirmLabel: "plan-first"},
		store: nil,
	}
	issue := linear.Issue{Identifier: "ENG-5"}
	if p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=false when store is nil, even with PlanConfirm enabled")
	}
}

func TestNeedsPlanConfirm_EmptyLabel(t *testing.T) {
	p := &Pipeline{
		cfg:   &config.Config{PlanConfirm: false, PlanConfirmLabel: ""},
		store: testStore(t),
	}
	issue := linear.Issue{
		Identifier: "ENG-4",
		Labels:     linear.LabelConnection{Nodes: []linear.Label{{Name: "plan-first"}}},
	}
	if p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=false when PlanConfirmLabel is empty")
	}
}

func TestHasPendingPlan_NilStore(t *testing.T) {
	p := &Pipeline{store: nil}
	if p.hasPendingPlan("ENG-1") {
		t.Error("expected hasPendingPlan=false when store is nil")
	}
}

func TestIsPlanComment(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{linear.PlanConfirmCommentPrefix + "\n\nSome plan here.", true},
		{linear.PlanConfirmCommentPrefix, true},
		{"Some random comment.", false},
		{"📋 **Not Noctra**", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.body[:min(len(c.body), 30)], func(t *testing.T) {
			if got := isPlanComment(c.body); got != c.want {
				t.Errorf("isPlanComment(%q) = %v, want %v", c.body[:min(len(c.body), 30)], got, c.want)
			}
		})
	}
}
