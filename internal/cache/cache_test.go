package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPersistenceAndFixedExpiry(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	want := Entry{Data: []byte("nzb bytes"), ContentType: "application/x-nzb", Filename: "release.nzb"}
	if err := s.Put("secret-key", want); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.dir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return now.Add(59 * time.Minute) }
	got, hit, err := reopened.Get("secret-key")
	if err != nil || !hit || !bytes.Equal(got.Data, want.Data) || got.ContentType != want.ContentType || got.Filename != want.Filename {
		t.Fatalf("Get = %+v, %v, %v", got, hit, err)
	}
	reopened.now = func() time.Time { return now.Add(time.Hour) }
	if _, hit, err := reopened.Get("secret-key"); err != nil || hit {
		t.Fatalf("expired Get: hit=%v err=%v", hit, err)
	}
	if _, err := os.Stat(s.path("secret-key")); !os.IsNotExist(err) {
		t.Fatalf("expired entry remains: %v", err)
	}
}

func TestCorruptionAndPruning(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	if err := s.Put("expired", Entry{Data: []byte("expired")}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := s.Put("fresh", Entry{Data: []byte("fresh")}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"truncated": "{", "missing": "{}", "empty": `{"Version":1,"ExpiresAt":"9999-01-01T00:00:00Z","Entry":{}}`} {
		if err := os.WriteFile(s.path(key), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, hit, err := s.Get(key); err != nil || hit {
			t.Fatalf("corrupt %s: hit=%v err=%v", key, hit, err)
		}
	}
	unrelated := filepath.Join(s.dir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("corrupt"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(s.dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("after prune: %v, %v", files, err)
	}
	if _, hit, err := s.Get("fresh"); err != nil || !hit {
		t.Fatalf("fresh Get: %v %v", hit, err)
	}
}

func TestConcurrentPublication(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte(fmt.Sprintf("%02d", i)), 4096)
			if err := s.Put("shared", Entry{Data: payload}); err != nil {
				t.Error(err)
				return
			}
			entry, hit, err := s.Get("shared")
			if err != nil || !hit || len(entry.Data) != len(payload) {
				t.Errorf("Get after Put: %v %v len=%d", hit, err, len(entry.Data))
				return
			}
			if !bytes.Equal(entry.Data, bytes.Repeat(entry.Data[:2], 4096)) {
				t.Error("partially published entry")
			}
			if err := s.Prune(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	files, err := os.ReadDir(s.dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files remain: %v %v", files, err)
	}
	info, err := os.Stat(s.path("shared"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions: %v", info.Mode())
	}
}

func TestRejectInvalidConfigurationAndEmptyData(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := New(t.TempDir(), ttl); err == nil {
			t.Fatal("accepted invalid TTL")
		}
	}
	if _, err := New("", time.Hour); err == nil {
		t.Fatal("accepted empty directory")
	}
	s := newTestStore(t)
	if err := s.Put("empty", Entry{}); err == nil {
		t.Fatal("accepted empty data")
	}
	if _, hit, err := s.Get("../not-present"); err != nil || hit {
		t.Fatalf("missing Get = %v %v", hit, err)
	}
}

func TestFilesystemErrorsAreNotMisses(t *testing.T) {
	s := newTestStore(t)
	// A directory at the record path reliably produces a read error, even when
	// tests run as root (where chmod-based permission tests are ineffective).
	if err := os.Mkdir(s.path("unreadable"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := s.Get("unreadable"); err == nil || hit {
		t.Fatalf("read error must not become a miss: hit=%v err=%v", hit, err)
	}
	if err := s.Put("unreadable", Entry{Data: []byte("data")}); err == nil {
		t.Fatal("publication failure must be reported")
	}
	if err := os.RemoveAll(s.dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(); err == nil {
		t.Fatal("directory read failure must be reported")
	}
	if err := s.Put("absent-directory", Entry{Data: []byte("data")}); err == nil {
		t.Fatal("temporary file creation failure must be reported")
	}
}

func TestAliasesPreserveOriginalExpiry(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	deadline := now.Add(time.Hour)
	if err := s.Put("source", Entry{Data: []byte("NZB")}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Minute)
	entry, hit, err := s.Get("source")
	if err != nil || !hit || !entry.ExpiresAt.Equal(deadline) {
		t.Fatalf("source did not retain expiration: hit=%v err=%v deadline=%v", hit, err, entry.ExpiresAt)
	}
	if err := s.Put("alias", entry); err != nil {
		t.Fatal(err)
	}
	alias, hit, err := s.Get("alias")
	if err != nil || !hit || !alias.ExpiresAt.Equal(deadline) {
		t.Fatalf("alias extended original expiration: hit=%v err=%v deadline=%v", hit, err, alias.ExpiresAt)
	}
	now = deadline
	if _, hit, err := s.Get("alias"); err != nil || hit {
		t.Fatalf("alias survived source deadline: hit=%v err=%v", hit, err)
	}
	if err := s.Put("expired-alias", entry); err == nil {
		t.Fatal("expired inherited response accepted")
	}
	// A future inherited deadline cannot exceed this store's configured TTL.
	if err := s.Put("bounded", Entry{Data: []byte("NZB"), ExpiresAt: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	bounded, hit, err := s.Get("bounded")
	if err != nil || !hit || !bounded.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("inherited expiration exceeded TTL: hit=%v err=%v deadline=%v", hit, err, bounded.ExpiresAt)
	}
}
