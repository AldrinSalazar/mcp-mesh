package nodes

import "sync/atomic"

// counters are the registry's running totals since the hub started.
type counters struct {
	opens        atomic.Int64 // MCP sessions opened (shared MCPs and listings)
	openFailures atomic.Int64 // opens that never became ready
	failures     atomic.Int64 // ready sessions that ended with an error (crash, disconnect)
	busy         atomic.Int64 // calls refused because the MCP's queue was full
	nodesRefused atomic.Int64 // node connections turned away (bad hello, key, name taken)
}

// Stats is a point-in-time summary for health and metrics.
type Stats struct {
	NodesOnline     int   `json:"nodes_online"`
	MCPsReady       int   `json:"mcps_ready"`
	InFlight        int   `json:"calls_in_flight"`
	Queued          int   `json:"calls_queued"`
	Starting        int   `json:"calls_waiting_start"`
	ClientProcesses int   `json:"client_processes"`
	Opens           int64 `json:"mcp_opens"`
	OpenFailures    int64 `json:"mcp_open_failures"`
	Failures        int64 `json:"mcp_session_failures"`
	Busy            int64 `json:"calls_refused_busy"`
	NodesRefused    int64 `json:"nodes_refused"`
}

// Stats sums the current load and the totals since start.
func (r *Registry) Stats() Stats {
	s := Stats{
		Opens: r.counters.opens.Load(), OpenFailures: r.counters.openFailures.Load(),
		Failures: r.counters.failures.Load(), Busy: r.counters.busy.Load(), NodesRefused: r.counters.nodesRefused.Load(),
	}
	for _, n := range r.Nodes() {
		if n.Online {
			s.NodesOnline++
		}
		for _, m := range n.MCPs {
			if m.State == MCPReady {
				s.MCPsReady++
			}
			s.InFlight += m.InFlight
			s.Queued += m.Queued
			s.Starting += m.Starting
			s.ClientProcesses += m.Clients
		}
	}
	return s
}
