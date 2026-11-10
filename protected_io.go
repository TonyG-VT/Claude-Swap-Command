package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// fileFingerprint contains no file content or credential material. Modification
// time is part of the guard: even a later rewrite of identical bytes is newer work.
type fileFingerprint struct {
	Exists   bool   `json:"exists"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Modified int64  `json:"modified,omitempty"`
	Mode     uint32 `json:"mode,omitempty"`
}

func fingerprintBytes(data []byte, info os.FileInfo) fileFingerprint {
	sum := sha256.Sum256(data)
	return fileFingerprint{true, hex.EncodeToString(sum[:]), int64(len(data)), info.ModTime().UnixNano(), uint32(info.Mode().Perm())}
}

func readStableFile(path string) ([]byte, fileFingerprint, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fileFingerprint{}, err
	}
	if !before.Mode().IsRegular() {
		return nil, fileFingerprint{}, fmt.Errorf("refusing non-regular file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fileFingerprint{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, fileFingerprint{}, fmt.Errorf("file changed while opening: %s", path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fileFingerprint{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, fileFingerprint{}, err
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) ||
		after.Size() != opened.Size() || after.ModTime() != opened.ModTime() ||
		int64(len(data)) != after.Size() {
		return nil, fileFingerprint{}, fmt.Errorf("file changed while reading: %s", path)
	}
	return data, fingerprintBytes(data, after), nil
}

func fingerprintFile(path string) (fileFingerprint, error) {
	_, state, err := readStableFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileFingerprint{}, nil
	}
	return state, err
}

// Existing ancestor aliases such as macOS /var -> /private/var are permitted.
// Transaction scope validation separately rejects links below the pinned roots.
func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing non-directory or linked directory: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return ensureDirectory(path)
}

func ensurePrivateDirectory(path string) error {
	if err := ensureDirectory(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func syncDirectory(path string) error {
	// Windows does not support os.File.Sync on directory handles. This project
	// targets macOS; Windows still receives file fsync and atomic replacement.
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func rejectLinkedFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular destination: %s", path)
	}
	return nil
}

// stageProtectedFile makes an unpredictable, exclusive, private staging file.
// It is fsynced before the journal records its exact expected postwrite state.
func stageProtectedFile(path string, data []byte, preserve *fileFingerprint) (string, fileFingerprint, error) {
	if err := ensureDirectory(filepath.Dir(path)); err != nil {
		return "", fileFingerprint{}, err
	}
	if err := rejectLinkedFile(path); err != nil {
		return "", fileFingerprint{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".claude-swap-write-*")
	if err != nil {
		return "", fileFingerprint{}, err
	}
	tmp := f.Name()
	fail := func(err error) (string, fileFingerprint, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fileFingerprint{}, err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if preserve != nil && preserve.Exists {
		at := time.Unix(0, preserve.Modified)
		if err := os.Chtimes(tmp, at, at); err != nil {
			return fail(err)
		}
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fileFingerprint{}, err
	}
	return tmp, fingerprintBytes(data, info), nil
}

func writeProtectedFile(path string, data []byte, preserve *fileFingerprint) error {
	if err := validateActiveTransactionPath(path); err != nil {
		return err
	}
	tmp, after, err := stageProtectedFile(path, data, preserve)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return journalMutation(path, after, func() error {
		if err := rejectLinkedFile(path); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	})
}

func removeProtectedFile(path string) error {
	if err := validateActiveTransactionPath(path); err != nil {
		return err
	}
	return journalMutation(path, fileFingerprint{}, func() error { return removeFileRaw(path) })
}

func removeFileRaw(path string) error {
	if err := rejectLinkedFile(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

// writeFileRaw bypasses the journal for the journal itself, private snapshots,
// and a rollback that has already validated every affected path.
func writeFileRaw(path string, data []byte, preserve *fileFingerprint) error {
	tmp, _, err := stageProtectedFile(path, data, preserve)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if preserve != nil && preserve.Exists {
		if err := os.Chmod(tmp, os.FileMode(preserve.Mode)); err != nil {
			return err
		}
		f, err := os.Open(tmp)
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	if err := rejectLinkedFile(path); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
