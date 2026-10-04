package state

import (
	"fmt"
	"os"
	"syscall"
)

// LockCache exclusively locks the cache lock file until the returned function is called.
func LockCache(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open cache lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cache directory is already locked or does not support locking: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
