package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestProbeRejectsInvalidPortsAndRedirects(t *testing.T) {
	if _, err := Probe(context.Background(), 0); err == nil {
		t.Fatal("zero port was accepted")
	}
	var followed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			followed.Store(true)
			response.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(response, request, "/target", http.StatusFound)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), port); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect probe error = %v", err)
	}
	if followed.Load() {
		t.Fatal("loopback probe followed a redirect")
	}
}
