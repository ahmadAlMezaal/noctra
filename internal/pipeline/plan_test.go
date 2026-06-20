package pipeline

import (
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
)

func TestNeedsPlanConfirm_GlobalEnabled(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{PlanConfirm: true, PlanConfirmLabel: "plan-first"},
	}
	issue := linear.Issue{Identifier: "ENG-1", Labels: linear.LabelConnection{}}
	if !p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=true when PlanConfirm is globally enabled")
	}
}

func TestNeedsPlanConfirm_LabelActivation(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{PlanConfirm: false, PlanConfirmLabel: "plan-first"},
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
		cfg: &config.Config{PlanConfirm: false, PlanConfirmLabel: "plan-first"},
	}
	issue := linear.Issue{
		Identifier: "ENG-3",
		Labels:     linear.LabelConnection{Nodes: []linear.Label{{Name: "bug"}}},
	}
	if p.needsPlanConfirm(issue) {
		t.Error("expected needsPlanConfirm=false when feature is off and label is absent")
	}
}

func TestNeedsPlanConfirm_EmptyLabel(t *testing.T) {
	p := &Pipeline{
		cfg: &config.Config{PlanConfirm: false, PlanConfirmLabel: ""},
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
