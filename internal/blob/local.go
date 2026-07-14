package blob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// localStore writes blobs to a directory under baseDir/blobs. Keys may contain
// slashes (they become subdirectories); path traversal is rejected.
type localStore struct {
	root string
}

func newLocal(baseDir string) (Store, error) {
	root := filepath.Join(baseDir, "blobs")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	return &localStore{root: root}, nil
}

func (s *localStore) Backend() string { return "local" }

func (s *localStore) path(key string) (string, error) {
	if key == "" || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid blob key %q", key)
	}
	full := filepath.Join(s.root, filepath.Clean("/"+key))
	if !strings.HasPrefix(full, s.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid blob key %q", key)
	}
	return full, nil
}

func (s *localStore) Put(_ context.Context, key, _ string, data []byte) error {
	full, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return err
	}
	// Atomic write: temp file then rename.
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, full)
}

func (s *localStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (s *localStore) Delete(_ context.Context, key string) error {
	full, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
