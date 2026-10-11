// Package activity keeps the most recent tool calls in memory for the admin
// UI. It is a stand-in for the persistent audit log.
package activity

import (
	"sync"
	"time"
)

// Recorder receives finished tool calls. [Log] keeps recent ones in memory;
// a persistent audit log will implement it too.
type Recorder interface {
	Add(Call)
}

// Call is one finished tool call.
type Call struct {
	Time     time.Time
	ClientID string // identity.Client.ID: the client key's ID
	Client   string // identity.Client.Name
	// Kind is KindTool (also when empty), KindPrompt or KindResource.
	Kind string
	// Tool is the exposed tool or prompt name, or the resource URI.
	Tool     string
	Node     string
	MCP      string
	Duration time.Duration
	// Outcome is "ok", "tool_error" (result IsError) or "error" (protocol or
	// transport failure).
	Outcome string
	Error   string
}

// What a Call recorded.
const (
	KindTool     = "tool"
	KindPrompt   = "prompt"
	KindResource = "resource"
)

// KindOrTool is c.Kind, with "" meaning a tool call.
func (c Call) KindOrTool() string {
	if c.Kind == "" {
		return KindTool
	}
	return c.Kind
}

// Log is a fixed-size ring of recent calls, safe for concurrent use.
type Log struct {
	mu    sync.Mutex
	calls []Call
	next  int
	full  bool
}

// New returns a log that keeps the last n calls.
func New(n int) *Log {
	return &Log{calls: make([]Call, n)}
}

// Add records a call.
func (l *Log) Add(c Call) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls[l.next] = c
	l.next = (l.next + 1) % len(l.calls)
	if l.next == 0 {
		l.full = true
	}
}

// Recent returns the recorded calls, newest first.
func (l *Log) Recent() []Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = len(l.calls)
	}
	out := make([]Call, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, l.calls[(l.next-i+len(l.calls))%len(l.calls)])
	}
	return out
}

// Multi returns a Recorder that passes each call to all of rs. Nil entries
// are skipped.
func Multi(rs ...Recorder) Recorder {
	var out multi
	for _, r := range rs {
		if r != nil {
			out = append(out, r)
		}
	}
	return out
}

type multi []Recorder

func (m multi) Add(c Call) {
	for _, r := range m {
		r.Add(c)
	}
}
