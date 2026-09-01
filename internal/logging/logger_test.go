package logging

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/joeykchen/codexlink/internal/config"
)

func TestRedactRemovesCredentialsAndPairingCodes(t *testing.T) {
	message := `Authorization: Bearer secret access_token=access refresh_token=refresh client_secret=client code=grant "access_token":"json-access" abcd-efgh cl_at_abcdefghijklmnopqrstuvwxyz`
	redacted := redact(message)
	for _, secret := range []string{"Bearer secret", "=access", "=refresh", "=client", "=grant", "json-access", "abcd-efgh", "cl_at_abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(strings.ToLower(redacted), strings.ToLower(secret)) {
			t.Fatalf("redacted output still contains %q: %s", secret, redacted)
		}
	}
	if count := strings.Count(redacted, "[REDACTED]"); count < 6 {
		t.Fatalf("redacted markers = %d: %s", count, redacted)
	}
}

func TestLoggerWritesPrivateSingleLineOutputConcurrently(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	logger, err := New("test", false)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			logger.Info("line one\rline two\nline three Authorization: Bearer hidden")
		}()
	}
	wait.Wait()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.StateDir(), "logs", "bridge.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "hidden") || strings.Count(text, "\n") != 16 || !strings.Contains(text, "line one line two line three") {
		t.Fatalf("unexpected log output: %q", text)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %o", info.Mode().Perm())
	}
}
