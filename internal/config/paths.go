package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/joeykchen/codexlink/internal/state"
)

const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 48765

	StateDirEnv       = "CODEXLINK_STATE_DIR"
	legacyStateDirEnv = "C2C_STATE_DIR"
)

func StateDir() string {
	for _, name := range []string{StateDirEnv, legacyStateDirEnv} {
		if override := strings.TrimSpace(os.Getenv(name)); override != "" {
			if absolute, err := filepath.Abs(override); err == nil {
				return absolute
			}
		}
	}
	return defaultStateDir("codexlink")
}

// LegacyStateDir returns the state directory used by the pre-CodexLink build.
// It is intentionally undocumented and only exists to support a safe, one-time
// migration for users upgrading in place.
func LegacyStateDir() string { return defaultStateDir("c2c-go") }

func defaultStateDir(appName string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", appName)
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, appName)
	default:
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(base, appName)
	}
}

func EnsureDir(path string) (string, error) { return state.EnsurePrivateDir(path) }

func StateSubdir(name string) (string, error) {
	return state.New(StateDir()).Bucket(name)
}

// WriteSecureJSON and related helpers remain as narrow compatibility wrappers
// for configuration files that intentionally live outside the state repository.
func WriteSecureJSON(path string, value any) error   { return state.WriteJSONFile(path, value) }
func WriteSecureFile(path string, data []byte) error { return state.WriteFileAtomic(path, data) }
func ReadJSON(path string, target any) (bool, error) { return state.ReadJSONFile(path, target) }
