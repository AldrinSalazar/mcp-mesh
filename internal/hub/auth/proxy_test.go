package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseProxies(t *testing.T) {
	got, err := ParseProxies(" 10.0.0.0/8, 192.168.1.5 ,::1,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.5/32", "::1/128"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("%d: %s, want %s", i, got[i], want[i])
		}
	}
	if _, err := ParseProxies("10.0.0.0/8,nonsense"); err == nil {
		t.Error("bad entry accepted")
	}
	if p, err := ParseProxies(""); err != nil || len(p) != 0 {
		t.Errorf("empty = %v, %v", p, err)
	}
}

func TestTrustProxies(t *testing.T) {
	trusted, _ := ParseProxies("127.0.0.1, 10.1.0.0/16")
	tests := []struct {
		name    string
		remote  string
		xff     []string
		proto   string
		wantIP  string
		wantTLS bool
	}{
		{"direct client, headers ignored", "203.0.113.9:5000", []string{"1.2.3.4"}, "https", "203.0.113.9", false},
		{"proxy, one hop", "127.0.0.1:4000", []string{"198.51.100.7"}, "https", "198.51.100.7", true},
		{"proxy chain", "127.0.0.1:4000", []string{"198.51.100.7, 10.1.2.3"}, "http", "198.51.100.7", false},
		{"spoofed leftmost", "127.0.0.1:4000", []string{"6.6.6.6, 198.51.100.7"}, "", "198.51.100.7", false},
		{"split headers", "127.0.0.1:4000", []string{"6.6.6.6", "198.51.100.7"}, "", "198.51.100.7", false},
		{"no header", "127.0.0.1:4000", nil, "", "127.0.0.1", false},
		{"garbage stops the walk", "127.0.0.1:4000", []string{"198.51.100.7, junk"}, "", "127.0.0.1", false},
		{"all hops trusted", "127.0.0.1:4000", []string{"10.1.0.1"}, "", "10.1.0.1", false},
		{"mapped v4 peer", "[::ffff:127.0.0.1]:4000", []string{"198.51.100.7"}, "", "198.51.100.7", false},
		{"last proto wins", "127.0.0.1:4000", []string{"198.51.100.7"}, "http, https", "198.51.100.7", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIP string
			var gotTLS bool
			h := TrustProxies(trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotIP, gotTLS = ClientIP(r), IsHTTPS(r)
			}))
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if gotIP != tt.wantIP || gotTLS != tt.wantTLS {
				t.Errorf("ip %q https %v, want %q %v", gotIP, gotTLS, tt.wantIP, tt.wantTLS)
			}
		})
	}
}

func TestLocalOrTLSBehindProxy(t *testing.T) {
	trusted, _ := ParseProxies("127.0.0.1")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := TrustProxies(trusted, LocalOrTLS(false, ok))
	for _, tt := range []struct {
		proto string
		want  int
	}{{"https", 200}, {"http", 403}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "127.0.0.1:4000"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Forwarded-Proto", tt.proto)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Errorf("public client over %s via proxy: %d, want %d", tt.proto, w.Code, tt.want)
		}
	}
}
