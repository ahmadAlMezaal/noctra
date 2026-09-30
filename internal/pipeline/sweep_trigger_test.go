package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onelastcommit/noctra/internal/sweep"
)

func newTriggerPipeline() *Pipeline {
	return &Pipeline{
		sweeper:  &sweep.Scheduler{},
		sweepNow: make(chan sweep.PlanOptions, 1),
	}
}

func TestTriggerSweep_QueuesOnce(t *testing.T) {
	p := newTriggerPipeline()

	if err := p.TriggerSweep(sweep.PlanOptions{Tasks: []string{"lint-cleanup"}}); err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if err := p.TriggerSweep(sweep.PlanOptions{}); err == nil {
		t.Fatal("second trigger should have been refused while one is queued")
	}

	got := <-p.sweepNow
	if len(got.Tasks) != 1 || got.Tasks[0] != "lint-cleanup" {
		t.Errorf("queued options: got %+v", got)
	}

	if err := p.TriggerSweep(sweep.PlanOptions{}); err != nil {
		t.Fatalf("trigger after drain: %v", err)
	}
}

func TestTriggerSweep_DisabledSweeps(t *testing.T) {
	p := &Pipeline{sweepNow: make(chan sweep.PlanOptions, 1)}
	err := p.TriggerSweep(sweep.PlanOptions{})
	if err == nil {
		t.Fatal("expected an error when sweeps are disabled")
	}
	if !strings.Contains(err.Error(), "SWEEP_ENABLED") {
		t.Errorf("error should name the setting to change, got %q", err)
	}
}

func triggerPipelineWithTasks(names ...string) *Pipeline {
	tasks := make([]sweep.Task, 0, len(names))
	for _, n := range names {
		tasks = append(tasks, sweep.Task{Name: n, Cooldown: time.Hour})
	}
	return &Pipeline{
		sweeper:  sweep.NewScheduler(nil, nil, tasks, time.Hour, 5, nil, nil),
		sweepNow: make(chan sweep.PlanOptions, 1),
	}
}

func TestHandleSweep_ClassifiesBareTokens(t *testing.T) {
	p := triggerPipelineWithTasks("lint-cleanup", "dead-code")

	if reply := p.handleSweep(context.Background(), "lint-cleanup trade-mate"); !strings.Contains(reply, "queued") {
		t.Fatalf("unexpected reply: %q", reply)
	}

	got := <-p.sweepNow
	if len(got.Tasks) != 1 || got.Tasks[0] != "lint-cleanup" {
		t.Errorf("tasks: got %v, want [lint-cleanup]", got.Tasks)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "trade-mate" {
		t.Errorf("repos: got %v, want [trade-mate]", got.Repos)
	}
	if got.IgnoreCooldown {
		t.Error("force should be off unless asked for")
	}
}

func TestHandleSweep_Force(t *testing.T) {
	p := triggerPipelineWithTasks("dead-code")

	reply := p.handleSweep(context.Background(), "--force dead-code")
	if !strings.Contains(reply, "Cooldowns ignored") {
		t.Errorf("reply should confirm the cooldown bypass, got %q", reply)
	}
	if got := <-p.sweepNow; !got.IgnoreCooldown {
		t.Error("--force did not reach the request")
	}
}

func TestHandleSweep_DisabledSweeps(t *testing.T) {
	p := &Pipeline{sweepNow: make(chan sweep.PlanOptions, 1)}
	if reply := p.handleSweep(context.Background(), ""); !strings.Contains(reply, "SWEEP_ENABLED") {
		t.Errorf("got %q", reply)
	}
}
