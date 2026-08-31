package runtime

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"testing"

	"github.com/joeykchen/codexlink/internal/config"
)

func TestInstallSecretConcurrentSingleValue(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	const workers = 32
	results := make([][]byte, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := range workers {
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = InstallSecret()
		}(i)
	}
	wait.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if len(results[i]) != 32 {
			t.Fatalf("worker %d secret length = %d", i, len(results[i]))
		}
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("worker %d observed a different install secret", i)
		}
	}
	path := filepath.Join(config.StateDir(), "install-secret")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || !bytes.Equal(decoded, results[0]) {
		t.Fatalf("persisted secret mismatch: %v", err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestInstallSecretRepairsCorruptState(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.StateDir(), "install-secret")
	if err := os.WriteFile(path, []byte("not-base64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err := InstallSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length = %d", len(secret))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || !bytes.Equal(secret, decoded) {
		t.Fatalf("repaired secret mismatch: %v", err)
	}
}
