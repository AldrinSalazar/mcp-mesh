package auth

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
)

func TestRequireKey(t *testing.T) {
	s, _ := openTemp(t)
	clientSecret, clientKey, _ := s.Create(KindClient, "laptop")
	nodeSecret, _, _ := s.Create(KindNode, "n1")
	var seen Key
	h := RequireKey(s, KindClient, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = KeyFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tt := range []struct {
		name, header string
		want         int
		bodyHas      string
	}{
		{"missing", "", 401, "missing key"},
		{"wrong scheme", "Basic abc", 401, "missing key"},
		{"empty bearer", "Bearer ", 401, "missing key"},
		{"unknown", "Bearer mm_client_" + strings.Repeat("0", 43), 401, "unknown or revoked"},
		{"wrong kind", "Bearer " + nodeSecret, 401, "needs a client key, not a node key"},
		{"ok", "Bearer " + clientSecret, 204, ""},
		{"ok lowercase scheme", "bearer " + clientSecret, 204, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seen = Key{}
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "192.168.1.5:4000"
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tt.bodyHas) || strings.Contains(body, "mm_") {
				t.Errorf("body = %q", body)
			}
			if tt.want == 401 {
				if rec.Header().Get("WWW-Authenticate") != `Bearer realm="mcp-mesh"` || seen.ID != "" {
					t.Errorf("header %q, handler ran: %v", rec.Header().Get("WWW-Authenticate"), seen.ID != "")
				}
				return
			}
			if seen.ID != clientKey.ID {
				t.Errorf("KeyFrom = %+v", seen)
			}
			got, _ := s.Get(clientKey.ID)
			if got.LastIP != "192.168.1.5" || got.LastUsed.IsZero() {
				t.Errorf("not touched: %+v", got)
			}
		})
	}
}

func TestIdentityAndAuthorizeNode(t *testing.T) {
	client := Key{ID: "k_1", Kind: KindClient, Name: "laptop"}
	node := Key{ID: "k_2", Kind: KindNode, Name: "n1"}
	bare := httptest.NewRequest("GET", "/", nil)
	withKey := func(k Key) *http.Request {
		return bare.WithContext(context.WithValue(bare.Context(), ctxKey{}, k))
	}
	if got := ClientIdentity(bare); got != identity.Anonymous {
		t.Errorf("no key: %+v", got)
	}
	if got := ClientIdentity(withKey(client)); got != (identity.Client{ID: "k_1", Name: "laptop"}) {
		t.Errorf("client: %+v", got)
	}
	for _, tt := range []struct {
		name string
		req  *http.Request
		node string
		ok   bool
	}{
		{"no key", bare, "n1", false},
		{"right node", withKey(node), "n1", true},
		{"other node", withKey(node), "n2", false},
		{"client key", withKey(client), "laptop", false},
	} {
		if err := AuthorizeNode(tt.req, tt.node); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestLocalOrTLS(t *testing.T) {
	for _, tt := range []struct {
		remote   string
		tls      bool
		insecure bool
		want     int
	}{
		{"127.0.0.1:1", false, false, 200},
		{"[::1]:1", false, false, 200},
		{"10.1.2.3:1", false, false, 200},
		{"172.16.0.1:1", false, false, 200},
		{"172.31.255.255:1", false, false, 200},
		{"172.32.0.1:1", false, false, 403},
		{"192.168.0.9:1", false, false, 200},
		{"169.254.1.1:1", false, false, 200},
		{"[fe80::1]:1", false, false, 200},
		{"[fd12:3456::1]:1", false, false, 200},
		{"[fc00::1]:1", false, false, 200},
		{"100.64.0.1:1", false, false, 200},
		{"100.127.255.255:1", false, false, 200},
		{"100.128.0.1:1", false, false, 403},
		{"[::ffff:192.168.1.1]:1", false, false, 200},
		{"8.8.8.8:1", false, false, 403},
		{"[2001:db8::1]:1", false, false, 403},
		{"garbage", false, false, 403},
		{"8.8.8.8:1", true, false, 200},
		{"8.8.8.8:1", false, true, 200},
	} {
		h := LocalOrTLS(tt.insecure, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tt.remote
		if tt.tls {
			req.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%+v: status %d", tt, rec.Code)
		}
		if tt.want == 403 && (!strings.Contains(rec.Body.String(), "TLS") || !strings.Contains(rec.Body.String(), "--allow-insecure")) {
			t.Errorf("message = %q", rec.Body.String())
		}
	}
}
