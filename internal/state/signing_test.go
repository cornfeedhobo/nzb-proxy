package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSigningKeyPersists(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadSigningKey(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadSigningKey(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatal("signing key changed between loads")
	}
	info, err := os.Stat(filepath.Join(dir, ".signing-key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected temporary files: %v, %v", entries, err)
	}
	other, err := LoadSigningKey(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, other) {
		t.Fatal("independent caches received identical secrets")
	}
}

func TestSigningKeyRejectsCorruptionWithoutReplacing(t *testing.T) {
	for _, size := range []int{0, 1, 31, 33, 64} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".signing-key")
		data := bytes.Repeat([]byte{42}, size)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSigningKey(dir, ""); err == nil {
			t.Fatalf("accepted key with %d bytes", size)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, data) {
			t.Fatalf("corrupt key was replaced: %v", err)
		}
	}
}

func TestSigningKeyRejectsLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSigningKey(dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, ".signing-key"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(dir, ""); err == nil {
		t.Fatal("accepted readable-by-others signing key")
	}
}

func TestMountedSigningKey(t *testing.T) {
	secret := t.TempDir()
	key := bytes.Repeat([]byte{42}, 32)
	file := filepath.Join(secret, "payload")
	if err := os.WriteFile(file, key, 0440); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(secret, "signing-key")
	if err := os.Symlink(file, mount); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		cache := t.TempDir()
		got, err := LoadSigningKey(cache, mount)
		if err != nil || !bytes.Equal(got, key) {
			t.Fatalf("mounted key load failed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cache, ".signing-key")); !os.IsNotExist(err) {
			t.Fatal("unexpected signing key in cache")
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(t.TempDir(), mount); err == nil {
		t.Fatal("missing mounted key accepted")
	}
	if err := os.WriteFile(file, key[:31], 0440); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(t.TempDir(), mount); err == nil {
		t.Fatal("short mounted key accepted")
	}
}
