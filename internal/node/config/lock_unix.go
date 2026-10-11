//go:build unix

package config

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockDir takes an exclusive lock on the directory, blocking until it is
// free, and returns the unlock. Locking the directory leaves no lock file
// behind. flock locks belong to the open file, so two editors in one process
// exclude each other too.
func lockDir(dir string) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s to lock it: %w", dir, err)
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", dir, err)
	}
	return func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}
