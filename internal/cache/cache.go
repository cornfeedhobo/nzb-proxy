// Package cache stores complete NZB responses on disk for a fixed retention period.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry contains the response data needed to serve a cached download.
type Entry struct {
	Data        []byte
	ContentType string
	Filename    string
	// ExpiresAt carries the original retention deadline when reusing an entry
	// under another key. Zero means this is a new response.
	ExpiresAt time.Time `json:"-"`
}

type record struct {
	Version   int
	ExpiresAt time.Time
	Entry     Entry
}

// Store supports concurrent callers in a single process. Multiple processes must
// not share its directory. Retention starts when Put publishes a complete entry.
type Store struct {
	dir string
	ttl time.Duration
	mu  sync.Mutex
	now func() time.Time
}

func New(dir string, ttl time.Duration) (*Store, error) {
	if dir == "" || ttl <= 0 {
		return nil, errors.New("cache requires a directory and positive retention")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	return &Store{dir: dir, ttl: ttl, now: time.Now}, nil
}

func (s *Store) path(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(digest[:])+".json")
}

// Get returns a miss for absent, expired, or corrupt entries. Filesystem failures
// are returned to the caller so it can avoid silently redownloading an NZB.
func (s *Store) Get(key string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(s.path(key))
}

func (s *Store) read(path string) (Entry, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("read cache entry: %w", err)
	}
	var r record
	if json.Unmarshal(data, &r) != nil || r.Version != 1 || len(r.Entry.Data) == 0 || !s.now().Before(r.ExpiresAt) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Entry{}, false, fmt.Errorf("remove unusable cache entry: %w", err)
		}
		return Entry{}, false, nil
	}
	r.Entry.ExpiresAt = r.ExpiresAt
	return r.Entry, true, nil
}

// Put atomically publishes a complete record. Callers must validate NZB contents
// before writing. A successful return means both file and directory were synced.
func (s *Store) Put(key string, entry Entry) error {
	if len(entry.Data) == 0 {
		return errors.New("cannot cache empty response")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	expiresAt := now.Add(s.ttl)
	if !entry.ExpiresAt.IsZero() {
		if !now.Before(entry.ExpiresAt) {
			return errors.New("cannot cache expired response")
		}
		if entry.ExpiresAt.Before(expiresAt) {
			expiresAt = entry.ExpiresAt
		}
	}
	f, err := os.CreateTemp(s.dir, ".nzb-cache-*")
	if err != nil {
		return fmt.Errorf("create cache entry: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	r := record{Version: 1, ExpiresAt: expiresAt, Entry: entry}
	if err := json.NewEncoder(f).Encode(r); err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync cache entry: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close cache entry: %w", err)
	}
	if err := os.Rename(f.Name(), s.path(key)); err != nil {
		return fmt.Errorf("publish cache entry: %w", err)
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("open cache directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync cache directory: %w", err)
	}
	return nil
}

// Prune removes expired and corrupt cache entries, leaving unrelated files alone.
func (s *Store) Prune() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("list cache directory: %w", err)
	}
	var failures []error
	for _, file := range files {
		name := file.Name()
		if !file.Type().IsRegular() || len(name) != 69 || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err != nil {
			continue
		}
		if _, _, err := s.read(filepath.Join(s.dir, name)); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
