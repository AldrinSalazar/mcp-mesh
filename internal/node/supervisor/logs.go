package supervisor

import "sync"

// logBuffer keeps stderr independent of the lifecycle lock: upstream.Close
// may wait for the child's stderr writer to finish.
type logBuffer struct {
	mu         sync.Mutex
	buf        []byte
	next, size int
	onLog      func([]byte)
}

func newLogBuffer(size int, onLog func([]byte)) *logBuffer {
	return &logBuffer{buf: make([]byte, size), onLog: onLog}
}

func (b *logBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	if len(p) >= len(b.buf) {
		copy(b.buf, p[len(p)-len(b.buf):])
		b.next = 0
		b.size = len(b.buf)
	} else {
		first := copy(b.buf[b.next:], p)
		copy(b.buf, p[first:])
		b.next = (b.next + n) % len(b.buf)
		b.size = min(b.size+n, len(b.buf))
	}
	b.mu.Unlock()
	if b.onLog != nil {
		// A callback may retain or change its argument; it must not alias the
		// upstream's write buffer or the ring.
		b.onLog(append([]byte(nil), p...))
	}
	return n, nil
}

func (b *logBuffer) tail(n int) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	n = max(0, min(n, b.size))
	out := make([]byte, n)
	start := (b.next - n + len(b.buf)) % len(b.buf)
	first := copy(out, b.buf[start:])
	copy(out[first:], b.buf[:n-first])
	return out
}
