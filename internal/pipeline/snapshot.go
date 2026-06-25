package pipeline

import "github.com/ahmadAlMezaal/noctra/internal/budget"

// DashboardSnapshot is a point-in-time view of the pipeline state, safe for
// JSON serialization. Collected under a single p.mu lock so the fields are
// consistent with each other.
type DashboardSnapshot struct {
	Active  []string         `json:"active"`
	Queued  map[string]int   `json:"queued"`
	Skipped []string         `json:"skipped"`
	Paused  bool             `json:"paused"`
	Budget  budget.Stats     `json:"budget"`
}

// Snapshot takes p.mu once and returns a consistent snapshot of the
// pipeline's runtime state for the dashboard.
func (p *Pipeline) Snapshot() DashboardSnapshot {
	p.mu.Lock()
	active := make([]string, 0, len(p.active))
	for id := range p.active {
		active = append(active, id)
	}
	queued := make(map[string]int, len(p.failedAttempts))
	for id, n := range p.failedAttempts {
		if _, running := p.active[id]; running {
			continue
		}
		if _, skip := p.skipped[id]; skip {
			continue
		}
		queued[id] = n
	}
	skipped := make([]string, 0, len(p.skipped))
	for id := range p.skipped {
		skipped = append(skipped, id)
	}
	paused := p.paused
	p.mu.Unlock()

	return DashboardSnapshot{
		Active:  active,
		Queued:  queued,
		Skipped: skipped,
		Paused:  paused,
		Budget:  p.budget.Stats(),
	}
}
