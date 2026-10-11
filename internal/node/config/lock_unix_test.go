//go:build unix

package config

import (
	"fmt"
	"sync"
	"testing"
)

// Concurrent edits (the CLI and the TUI) each read, change and write the
// whole file; without the lock one of them loses the other's change.
func TestConcurrentEditsKeepEveryChange(t *testing.T) {
	path := writeEditConfig(t, "hub: {url: ws://localhost/node}\n")
	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := AddMCP(path, Entry{Name: fmt.Sprintf("m%02d", i), Command: "echo"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, err := Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("%d entries after %d concurrent adds", len(entries), n)
	}
}
