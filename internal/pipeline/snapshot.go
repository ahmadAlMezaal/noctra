package pipeline

import "github.com/ahmadAlMezaal/noctra/internal/budget"

// ActiveEntry is one in-flight run in the dashboard snapshot.
type ActiveEntry struct {
	Identifier string `json:"identifier"`
	Repo       string `json:"repo,omitempty"`
}

// QueuedEntry is one queued-for-retry run in the dashboard snapshot.
type QueuedEntry struct {
	Identifier string `json:"identifier"`
	Repo       string `json:"repo,omitempty"`
	Retries    int    `json:"retries"`
}

// DashboardSnapshot is a point-in-time view of the pipeline state, safe for
// JSON serialization. Collected under a single p.mu lock so the fields are
// consistent with each other.
type DashboardSnapshot struct {
	Active  []ActiveEntry  `json:"active"`
	Queued  []QueuedEntry  `json:"queued"`
	Skipped []string       `json:"skipped"`
	Paused  bool           `json:"paused"`
	Budget  budget.Stats   `json:"budget"`
}

// Snapshot takes p.mu once and returns a consistent snapshot of the
// pipeline's runtime state for the dashboard.
func (p *Pipeline) Snapshot() DashboardSnapshot {
	p.mu.Lock()
	active := make([]ActiveEntry, 0, len(p.active))
	for id := range p.active {
		active = append(active, ActiveEntry{
			Identifier: id,
			Repo:       p.activeRepos[id],
		})
	}
	queued := make([]QueuedEntry, 0, len(p.failedAttempts))
	for id, n := range p.failedAttempts {
		if _, running := p.active[id]; running {
			continue
		}
		if _, skip := p.skipped[id]; skip {
			continue
		}
		queued = append(queued, QueuedEntry{
			Identifier: id,
			Repo:       p.activeRepos[id],
			Retries:    n,
		})
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
