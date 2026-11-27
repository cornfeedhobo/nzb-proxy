package state

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LoadSigningKey must run while holding the cache directory's process lock.
// Its independent secret prevents API clients from forging release identities.
func LoadSigningKey(dir, keyFile string) ([]byte, error) {
	path := keyFile
	stat := os.Stat // Projected Secret volumes expose key files through symlinks.
	if keyFile == "" {
		path = filepath.Join(dir, ".signing-key")
		stat = os.Lstat
	}
	info, statErr := stat(path)
	if statErr == nil && !info.Mode().IsRegular() {
		return nil, errors.New("invalid signing key: expected a regular file")
	}
	if statErr != nil && (keyFile != "" || !errors.Is(statErr, os.ErrNotExist)) {
		return nil, fmt.Errorf("inspect signing key: %w", statErr)
	}
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect signing key: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() != 32 {
			return nil, errors.New("invalid signing key: expected a regular file containing 32 bytes")
		}
		if keyFile != "" && info.Mode().Perm() & ^os.FileMode(0640) != 0 {
			return nil, errors.New("invalid mounted signing key permissions: expected owner-only or group-readable")
		}
		if keyFile == "" && info.Mode().Perm() != 0600 {
			return nil, errors.New("invalid signing key permissions: expected 0600")
		}
		key := make([]byte, 32)
		if _, err := io.ReadFull(f, key); err != nil {
			return nil, fmt.Errorf("read signing key: %w", err)
		}
		return key, nil
	}
	if keyFile != "" || !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open signing key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".signing-key-*")
	if err != nil {
		return nil, fmt.Errorf("create signing key: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return nil, fmt.Errorf("set signing key permissions: %w", err)
	}
	if _, err := tmp.Write(key); err != nil {
		return nil, fmt.Errorf("write signing key: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("sync signing key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close signing key: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, fmt.Errorf("publish signing key: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open signing key directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return nil, fmt.Errorf("sync signing key directory: %w", err)
	}
	return key, nil
}
