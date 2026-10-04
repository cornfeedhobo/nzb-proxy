//go:build !linux

package state

import "fmt"

// LockCache exclusively locks the cache lock file until the returned function is called.
func LockCache(path string) (func(), error) {
	return nil, fmt.Errorf("this release supports Linux only; run it in a Linux container")
}
