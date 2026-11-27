package state

import (
	"path/filepath"
	"testing"
)

func TestCacheDirectoryHasOneOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	unlock, err := LockCache(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if second, err := LockCache(path); err == nil {
		second()
		t.Fatal("a second process could own the cache")
	}
	unlock()
	third, err := LockCache(path)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	third()
}
