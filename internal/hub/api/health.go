package api

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Health is the hub's readiness and its counters. The server assembles the
// parts the API can't see (database, watchers, audit, registry, gateway).
type Health struct {
	// Ready is false when any problem was found.
	Ready bool `json:"ready"`
	// Problems says what is wrong, one line each; [] when ready.
	Problems []string `json:"problems"`
	// Metrics are point-in-time values and totals since the hub started,
	// by name.
	Metrics map[string]any `json:"metrics"`
}

// maxSnapshotAge is how old the events feed's last complete poll may be
// before the dashboard counts as stale; also how long after start the hub
// may go without one.
const maxSnapshotAge = 10 * time.Second

// Health checks the hub: Options.Health plus the events feed's freshness.
func (s *Server) Health(ctx context.Context) Health {
	h := Health{Metrics: map[string]any{}}
	if s.opts.Health != nil {
		h = s.opts.Health(ctx)
		if h.Metrics == nil {
			h.Metrics = map[string]any{}
		}
	}
	switch at, created := s.feed.polled(); {
	case !at.IsZero():
		age := time.Since(at)
		h.Metrics["events_snapshot_age_ms"] = age.Milliseconds()
		if age > maxSnapshotAge {
			h.Problems = append(h.Problems, fmt.Sprintf("dashboard events are stale: last snapshot %s ago", age.Round(time.Second)))
		}
	case time.Since(created) > maxSnapshotAge:
		// Never one complete poll: every stream reads on its own, and the
		// dashboard may show nothing at all.
		h.Problems = append(h.Problems, fmt.Sprintf("dashboard events have no complete snapshot since the hub started %s ago", time.Since(created).Round(time.Second)))
	}
	if h.Problems == nil {
		h.Problems = []string{}
	}
	h.Ready = len(h.Problems) == 0
	return h
}

func (s *Server) healthGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Health(r.Context()))
}

// Ready serves GET /readyz for probes: 200 "ready", or 503 with one problem
// per line. Unlike /api/v1/health it needs no session and has no metrics.
func (s *Server) Ready(w http.ResponseWriter, r *http.Request) {
	h := s.Health(r.Context())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if h.Ready {
		w.Write([]byte("ready\n"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	for _, p := range h.Problems {
		fmt.Fprintln(w, p)
	}
}
