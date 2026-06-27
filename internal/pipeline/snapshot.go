package pipeline

import (
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/budget"
)

// activeRunMeta is the per-run metadata the dashboard shows for in-flight runs,
// captured at dispatch time and cleared in markDone.
type activeRunMeta struct {
	runType   string
	startedAt time.Time
}

type ActiveEntry struct {
	Identifier string `json:"identifier"`
	Repo       string `json:"repo,omitempty"`
	Agent      string `json:"agent,omitempty"`      // backend running it (e.g. "claude")
	RunType    string `json:"run_type,omitempty"`   // "ticket" | "iterate" | "sweep" | "plan"
	StartedAt  string `json:"started_at,omitempty"` // RFC3339, for live elapsed timers
}

type QueuedEntry struct {
	Identifier string `json:"identifier"`
	Repo       string `json:"repo,omitempty"`
	Retries    int    `json:"retries"`
}

// DashboardSnapshot is collected under a single p.mu lock for consistency.
type DashboardSnapshot struct {
	Active  []ActiveEntry `json:"active"`
	Queued  []QueuedEntry `json:"queued"`
	Skipped []string      `json:"skipped"`
	Paused  bool          `json:"paused"`
	Budget  budget.Stats  `json:"budget"`
	Version string        `json:"version,omitempty"`
}

func (p *Pipeline) Snapshot() DashboardSnapshot {
	p.mu.Lock()
	active := make([]ActiveEntry, 0, len(p.active))
	for id := range p.active {
		e := ActiveEntry{
			Identifier: id,
			Repo:       p.activeRepos[id],
			Agent:      p.cfg.AgentBackend,
		}
		if m, ok := p.activeMeta[id]; ok {
			e.RunType = m.runType
			if !m.startedAt.IsZero() {
				e.StartedAt = m.startedAt.UTC().Format(time.RFC3339)
			}
		}
		active = append(active, e)
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
		Version: Version,
	}
}
