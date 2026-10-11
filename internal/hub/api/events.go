package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sync"
	"time"
)

// subBuffer is how many undelivered events a subscriber may fall behind by.
// Snapshots are whole-state, so a subscriber that is this far behind is
// better off reconnecting (EventSource does so by itself and gets a fresh
// snapshot) than reading a long backlog of stale ones.
const subBuffer = 16

// writeTimeout bounds one write to a stream; a browser that stops reading
// (its TCP window full) would otherwise hold the handler forever.
const writeTimeout = 10 * time.Second

type event struct {
	typ  string
	data []byte
}

type sub struct {
	ch   chan event
	done chan struct{} // closed when the feed drops the subscriber or stops
	once sync.Once
}

func (s *sub) close() { s.once.Do(func() { close(s.done) }) }

// feed polls snapshots and fans changes out to subscribers. The poller is
// the only source of events: what it publishes (last) and who receives the
// next change (subs) change together under mu, so a stream that starts from
// last and then reads its channel sees every state in order, and its final
// state is the newest. A subscriber whose buffer is full is closed, never
// waited for, so one stuck browser can't delay the others.
type feed struct {
	snap  func(ctx context.Context, typ string) ([]byte, error)
	types []string

	mu      sync.Mutex
	last    map[string][]byte // what was last published, by type
	subs    map[*sub]struct{}
	stopped bool
	created time.Time
	lastAt  time.Time // when the last poll that read every type finished
}

// polled is when the feed last finished polling every type (zero before the
// first such poll) and when the feed was made.
func (f *feed) polled() (at, created time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAt, f.created
}

func newFeed(snap func(ctx context.Context, typ string) ([]byte, error), types []string) *feed {
	return &feed{snap: snap, types: types, last: map[string][]byte{}, subs: map[*sub]struct{}{}, created: time.Now()}
}

func (f *feed) run(ctx context.Context, every time.Duration) {
	defer f.stop()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		f.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (f *feed) poll(ctx context.Context) {
	complete := true
	defer func() {
		// Only a poll that read every type counts as fresh (health).
		if complete {
			f.mu.Lock()
			f.lastAt = time.Now()
			f.mu.Unlock()
		}
	}()
	for _, typ := range f.types {
		b, err := f.snap(ctx, typ)
		if err != nil {
			complete = false
			continue // keep the last good state; the next tick retries
		}
		f.mu.Lock()
		if !bytes.Equal(f.last[typ], b) {
			f.last[typ] = b
			f.broadcast(event{typ, b})
		}
		f.mu.Unlock()
	}
}

// broadcast requires f.mu.
func (f *feed) broadcast(ev event) {
	for s := range f.subs {
		select {
		case s.ch <- ev:
		default:
			s.close()
			delete(f.subs, s)
		}
	}
}

func (f *feed) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	for s := range f.subs {
		s.close()
		delete(f.subs, s)
	}
}

// subscribe registers a subscriber and returns what was last published, by
// type, as one step: every later change reaches the subscriber.
func (f *feed) subscribe() (*sub, map[string][]byte) {
	s := &sub{ch: make(chan event, subBuffer), done: make(chan struct{})}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		s.close()
		return s, nil
	}
	f.subs[s] = struct{}{}
	return s, maps.Clone(f.last)
}

func (f *feed) unsubscribe(s *sub) {
	f.mu.Lock()
	delete(f.subs, s)
	f.mu.Unlock()
}

func (s *Server) eventTypes() []string {
	types := []string{"nodes", "tools", "prompts", "resources", "calls", "stats", "approvals", "settings"}
	if s.opts.Sessions != nil && s.opts.Keys != nil {
		types = append(types, "keys")
	}
	if s.opts.Sessions != nil && s.opts.Groups != nil {
		types = append(types, "groups")
	}
	if s.opts.Sessions != nil && s.opts.Admins != nil {
		types = append(types, "admins")
	}
	if s.opts.Overrides != nil {
		types = append(types, "overrides")
	}
	return types
}

// snapshot returns the JSON the matching GET endpoint answers with.
func (s *Server) snapshot(ctx context.Context, typ string) ([]byte, error) {
	var v any
	var err error
	switch typ {
	case "nodes":
		v = s.nodeViews()
	case "tools":
		v = s.toolViews()
	case "calls":
		v = s.callViews()
	case "stats":
		v, err = s.statsView(ctx)
	case "prompts":
		v = s.promptViews()
	case "resources":
		v = s.resourceViews()
	case "approvals":
		v = s.approvalViews()
	case "settings":
		v = s.settingsView()
	case "overrides":
		v, err = s.opts.Overrides.List(ctx)
	case "groups":
		v, err = s.opts.Groups.List(ctx)
	case "keys":
		v, err = s.keyViews(ctx)
	case "admins":
		v, err = s.adminViews(ctx)
	default:
		err = fmt.Errorf("unknown snapshot %q", typ)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	ctx := r.Context()
	// The initial snapshots are what the feed last published, taken as the
	// stream subscribes, so the changes it sends next are all newer.
	sb, published := s.feed.subscribe()
	defer s.feed.unsubscribe(sb)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// write sends one chunk with a deadline. Not every ResponseWriter
	// supports deadlines (tests' recorders); those just write.
	write := func(chunk string) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := io.WriteString(w, chunk); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	sent := map[string][]byte{}
	send := func(ev event) bool {
		if bytes.Equal(sent[ev.typ], ev.data) {
			return true // the browser has it
		}
		sent[ev.typ] = ev.data
		return write(fmt.Sprintf("event: %s\ndata: %s\n\n", ev.typ, ev.data))
	}
	for _, typ := range s.feed.types {
		data, ok := published[typ]
		if !ok {
			// Not published yet (the first poll hasn't read it). Read it now;
			// should the first poll's older read reach the stream after this
			// one, the poll after it publishes the current state again.
			var err error
			if data, err = s.feed.snap(ctx, typ); err != nil {
				s.log.Error("api: events snapshot", "type", typ, "error", err)
				continue
			}
		}
		if !send(event{typ, data}) {
			return
		}
	}
	// The headers go out even if no snapshot could be read.
	if rc.Flush() != nil {
		return
	}

	ping := time.NewTicker(s.ping)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sb.done:
			return
		case ev := <-sb.ch:
			if !s.stillSignedIn(r) || !send(ev) {
				return
			}
		case <-ping.C:
			if !s.stillSignedIn(r) {
				return
			}
			if !write(": ping\n\n") {
				return
			}
		}
	}
}

// stillSignedIn re-checks the stream's session, so signing out, a password
// change or a deleted admin also ends a stream opened before.
func (s *Server) stillSignedIn(r *http.Request) bool {
	if s.opts.Sessions == nil {
		return true
	}
	_, ok := s.opts.Sessions.Current(r)
	return ok
}
