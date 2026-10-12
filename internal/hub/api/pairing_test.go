package api

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/pairing"
)

type fakePairing struct {
	mu         sync.Mutex
	reqs       []pairing.Request
	delivered  []pairing.Grant
	deliverErr error
}

func (f *fakePairing) Pending() []pairing.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakePairing) Get(id string) (pairing.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.ID == id {
			return r, true
		}
	}
	return pairing.Request{}, false
}

func (f *fakePairing) take(id string) (pairing.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.reqs {
		if r.ID == id {
			f.reqs = slices.Delete(f.reqs, i, i+1)
			return r, true
		}
	}
	return pairing.Request{}, false
}

func (f *fakePairing) Deliver(id string, g pairing.Grant) error {
	if _, ok := f.take(id); !ok {
		return pairing.ErrNotPending
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliverErr != nil {
		return f.deliverErr
	}
	f.delivered = append(f.delivered, g)
	return nil
}

func (f *fakePairing) Deny(id string) (pairing.Request, bool) { return f.take(id) }

type fakeAdminLog struct {
	mu      sync.Mutex
	entries []adminlog.Entry
}

func (f *fakeAdminLog) Record(_ context.Context, e adminlog.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
}

func (f *fakeAdminLog) Query(context.Context, int64, int) ([]adminlog.Entry, error) { return nil, nil }

func (f *fakeAdminLog) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.entries {
		out = append(out, e.Action+" "+e.Target+" "+e.Detail)
	}
	return out
}

func pairingEnv(t *testing.T) (*env, *fakePairing, *fakeAdminLog, *[]string) {
	t.Helper()
	e := newEnv(t, true)
	p := &fakePairing{reqs: []pairing.Request{
		{ID: "p_desk", Node: "desk", Hostname: "desk.lan", OS: "linux", IP: "10.0.0.9", Code: "ABC-DEF", Requested: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "p_laptop", Node: "laptop", IP: "10.0.0.7", Code: "GHJ-KMN", Requested: time.Date(2026, 4, 1, 0, 1, 0, 0, time.UTC)},
	}}
	alog := &fakeAdminLog{}
	var disconnected []string
	e.srv.opts.Pairing, e.srv.opts.AdminLog = p, alog
	e.srv.opts.DisconnectKey = func(id string) { disconnected = append(disconnected, id) }
	return e, p, alog, &disconnected
}

func TestPairingList(t *testing.T) {
	e, _, _, _ := pairingEnv(t)
	w := e.do("GET", "/api/v1/pairings", "")
	wantStatus(t, w, 200)
	var got []struct {
		ID        string `json:"id"`
		Node      string `json:"node"`
		Code      string `json:"code"`
		IP        string `json:"ip"`
		NameTaken bool   `json:"name_taken"`
	}
	decodeBody(t, w, &got)
	if len(got) != 2 || got[0].ID != "p_desk" || got[0].Code != "ABC-DEF" || got[0].IP != "10.0.0.9" || got[0].NameTaken {
		t.Fatalf("got %+v", got)
	}
	// laptop's key exists: approving under that name replaces it.
	if got[1].Node != "laptop" || !got[1].NameTaken {
		t.Fatalf("laptop = %+v, want name_taken", got[1])
	}
	var settings struct {
		Pairing bool `json:"pairing"`
	}
	decodeBody(t, e.do("GET", "/api/v1/settings", ""), &settings)
	if !settings.Pairing {
		t.Fatal("settings don't say pairing is on")
	}
}

func TestPairingApprove(t *testing.T) {
	e, p, alog, _ := pairingEnv(t)
	w := e.do("POST", "/api/v1/pairings/p_desk/approve", "")
	wantStatus(t, w, 200)
	var got struct {
		Key struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"key"`
	}
	decodeBody(t, w, &got)
	if got.Key.Kind != "node" || got.Key.Name != "desk" {
		t.Fatalf("key = %+v", got.Key)
	}
	if len(p.delivered) != 1 || p.delivered[0] != (pairing.Grant{Node: "desk", Key: "mm_node_secret"}) {
		t.Fatalf("delivered %+v", p.delivered)
	}
	if a := alog.actions(); len(a) != 1 || a[0] != "pairing.approve "+got.Key.ID+" node desk from 10.0.0.9" {
		t.Fatalf("admin log = %q", a)
	}
	wantError(t, e.do("POST", "/api/v1/pairings/p_desk/approve", ""), 404, pairing.ErrNotPending.Error())
}

func TestPairingApproveRenamed(t *testing.T) {
	e, p, _, _ := pairingEnv(t)
	wantError(t, e.do("POST", "/api/v1/pairings/p_desk/approve", `{"name":"my desk"}`), 400, "")
	wantStatus(t, e.do("POST", "/api/v1/pairings/p_desk/approve", `{"name":" studio "}`), 200)
	if len(p.delivered) != 1 || p.delivered[0].Node != "studio" {
		t.Fatalf("delivered %+v, want the name the admin chose", p.delivered)
	}
}

// A name a node key is bound to needs replace, which revokes that key and
// disconnects its node before the new one can connect.
func TestPairingApproveReplace(t *testing.T) {
	e, p, alog, disconnected := pairingEnv(t)
	wantError(t, e.do("POST", "/api/v1/pairings/p_laptop/approve", ""), 409, "")
	if len(p.delivered) != 0 || len(e.keys.revoked) != 0 {
		t.Fatal("a conflicting approval changed something")
	}
	wantStatus(t, e.do("POST", "/api/v1/pairings/p_laptop/approve", `{"replace":true}`), 200)
	if !slices.Equal(e.keys.revoked, []string{"k_1"}) || !slices.Equal(*disconnected, []string{"k_1"}) {
		t.Fatalf("revoked %q, disconnected %q; want the old key k_1", e.keys.revoked, *disconnected)
	}
	if len(p.delivered) != 1 || p.delivered[0].Node != "laptop" {
		t.Fatalf("delivered %+v", p.delivered)
	}
	if a := alog.actions(); len(a) != 2 || a[0] != "key.revoke k_1 node laptop" {
		t.Fatalf("admin log = %q", a)
	}
}

// A node that leaves before it gets its key leaves no usable key behind.
func TestPairingApproveUndelivered(t *testing.T) {
	e, p, alog, _ := pairingEnv(t)
	p.deliverErr = pairing.ErrNotDelivered
	wantError(t, e.do("POST", "/api/v1/pairings/p_desk/approve", ""), 410, "")
	keys, _ := e.keys.List()
	for _, k := range keys {
		if k.Name == "desk" {
			t.Fatalf("undelivered key %s still exists", k.ID)
		}
	}
	if a := alog.actions(); len(a) != 0 {
		t.Fatalf("admin log = %q, want nothing approved", a)
	}
}

func TestPairingDeny(t *testing.T) {
	e, p, alog, _ := pairingEnv(t)
	wantStatus(t, e.do("DELETE", "/api/v1/pairings/p_desk", ""), 204)
	if len(p.Pending()) != 1 {
		t.Fatal("declined request still pending")
	}
	if a := alog.actions(); len(a) != 1 || a[0] != "pairing.deny desk 10.0.0.9" {
		t.Fatalf("admin log = %q", a)
	}
	wantError(t, e.do("DELETE", "/api/v1/pairings/p_desk", ""), 404, "")
	wantError(t, e.do("DELETE", "/api/v1/pairings/p_laptop", "", noCSRF), 403, "")
}

func TestPairingOff(t *testing.T) {
	e := newEnv(t, true)
	w := e.do("GET", "/api/v1/pairings", "")
	wantStatus(t, w, 200)
	if body := w.Body.String(); body != "[]\n" {
		t.Fatalf("body %q, want an empty list", body)
	}
	wantError(t, e.do("POST", "/api/v1/pairings/p_1/approve", ""), 404, "pairing is turned off on this hub")
	var settings struct {
		Pairing bool `json:"pairing"`
	}
	decodeBody(t, e.do("GET", "/api/v1/settings", ""), &settings)
	if settings.Pairing {
		t.Fatal("settings say pairing is on")
	}
	wantError(t, newEnv(t, false).do("GET", "/api/v1/pairings", ""), 404, "auth is off")
}
