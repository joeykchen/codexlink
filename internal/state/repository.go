package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Repository is the single persistence boundary for bridge-owned state. It
// provides atomic replacement, owner-only permissions, and predictable paths.
type Repository struct{ Root string }

func New(root string) Repository { return Repository{Root: filepath.Clean(root)} }

func (r Repository) Bucket(name string) (string, error) {
	if !safeComponent(name) {
		return "", fmt.Errorf("invalid state bucket %q", name)
	}
	return EnsurePrivateDir(filepath.Join(r.Root, name))
}

func (r Repository) Path(bucket, key, extension string) (string, error) {
	if !safeComponent(bucket) || !safeComponent(key) {
		return "", fmt.Errorf("invalid state path component")
	}
	if extension != "" && (!strings.HasPrefix(extension, ".") || strings.ContainsAny(extension, `/\\`)) {
		return "", fmt.Errorf("invalid state extension")
	}
	dir, err := r.Bucket(bucket)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, key+extension), nil
}

func (r Repository) ReadJSON(bucket, key string, target any) (bool, error) {
	path, err := r.Path(bucket, key, ".json")
	if err != nil {
		return false, err
	}
	return ReadJSONFile(path, target)
}

func (r Repository) WriteJSON(bucket, key string, value any) error {
	path, err := r.Path(bucket, key, ".json")
	if err != nil {
		return err
	}
	return WriteJSONFile(path, value)
}

func (r Repository) Remove(bucket, key, extension string) error {
	path, err := r.Path(bucket, key, extension)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r Repository) AppendJSONLine(bucket, key string, value any) error {
	path, err := r.Path(bucket, key, ".jsonl")
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_ = file.Chmod(0o600)
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (r Repository) ScanJSONLines(bucket, key string, visit func([]byte) error) error {
	path, err := r.Path(bucket, key, ".jsonl")
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		if err := visit(append([]byte(nil), scanner.Bytes()...)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func EnsurePrivateDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o700)
	return path, nil
}

func WriteJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

func ReadJSONFile(path string, target any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("decode %s: %w", path, err)
	}
	return true, nil
}

func WriteFileAtomic(path string, data []byte) error {
	if _, err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func safeComponent(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`) && !strings.ContainsRune(value, '\x00')
}
