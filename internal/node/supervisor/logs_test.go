package supervisor

import (
	"bytes"
	"sync"
	"testing"
)

func TestLogBuffer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes []string
		max    int
		want   string
	}{
		{"empty", nil, 10, ""},
		{"partial", []string{"abc"}, 10, "abc"},
		{"wrapped", []string{"abc", "defg"}, 10, "cdefg"},
		{"tail", []string{"abc", "defg"}, 3, "efg"},
		{"oversize", []string{"abcdefgh"}, 10, "defgh"},
		{"oversize then wrap", []string{"abcdefgh", "ijk"}, 10, "ghijk"},
		{"exact size", []string{"ab", "cdefg"}, 10, "cdefg"},
		{"zero", []string{"abc"}, 0, ""},
		{"negative", []string{"abc"}, -1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chunks []byte
			var b *logBuffer
			b = newLogBuffer(5, func(p []byte) {
				_ = b.tail(5) // callbacks run without the ring lock
				chunks = append(chunks, p...)
				clear(p)
			})
			var all []byte
			for _, write := range tc.writes {
				p := []byte(write)
				all = append(all, p...)
				n, err := b.Write(p)
				if err != nil || n != len(p) || string(p) != write {
					t.Fatalf("Write = %d, %v, %q", n, err, p)
				}
			}
			if got := string(b.tail(tc.max)); got != tc.want {
				t.Fatalf("tail = %q, want %q", got, tc.want)
			}
			if !bytes.Equal(chunks, all) {
				t.Fatalf("callbacks = %q, want %q", chunks, all)
			}
			clear(b.tail(5))
			if got := string(b.tail(tc.max)); got != tc.want {
				t.Fatal("tail aliases ring")
			}
		})
	}
}

func TestLogBufferConcurrent(t *testing.T) {
	b := newLogBuffer(32, nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				_, _ = b.Write([]byte("abcd"))
				_ = b.tail(10)
			}
		})
	}
	wg.Wait()
	if got := b.tail(100); len(got) != 32 {
		t.Fatalf("ring size = %d", len(got))
	}
}
