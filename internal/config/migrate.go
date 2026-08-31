package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// MigrationResult describes a best-effort migration from the former state
// directory. Runtime and lock files are deliberately excluded because they
// describe processes started by a different executable.
type MigrationResult struct {
	Migrated bool
	From     string
	To       string
}

func MigrateLegacyState() (MigrationResult, error) {
	result := MigrationResult{From: LegacyStateDir(), To: StateDir()}
	if result.From == result.To || os.Getenv(StateDirEnv) != "" || os.Getenv(legacyStateDirEnv) != "" {
		return result, nil
	}
	if _, err := os.Stat(result.To); err == nil {
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if info, err := os.Stat(result.From); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, err
	} else if !info.IsDir() {
		return result, nil
	}
	if _, err := EnsureDir(result.To); err != nil {
		return result, err
	}
	for _, name := range []string{"auth", "endpoints", "executions", "sessions", "tunnels"} {
		if err := copyPrivateTree(filepath.Join(result.From, name), filepath.Join(result.To, name)); err != nil {
			return result, fmt.Errorf("migrate %s: %w", name, err)
		}
	}
	if err := copyPrivateFile(filepath.Join(result.From, "install-secret"), filepath.Join(result.To, "install-secret")); err != nil {
		return result, fmt.Errorf("migrate install secret: %w", err)
	}
	result.Migrated = true
	return result, nil
}

func copyPrivateTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if _, err := EnsureDir(destination); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			_, err := EnsureDir(target)
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyPrivateFile(path, target)
	})
}

func copyPrivateFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer input.Close()
	if _, err := EnsureDir(filepath.Dir(destination)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".codexlink-migrate-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return err
	}
	return os.Chmod(destination, 0o600)
}
