//go:build !windows

package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joeykchen/codexlink/internal/logging"
)

func TestProcessProviderLifecycle(t *testing.T) {
	script := writeTunnelScript(t, "#!/bin/sh\necho 'INF https://blue-bird.trycloudflare.com ready'\nwhile :; do sleep 1; done\n")
	provider := &processProvider{
		name: "test", binary: script, logger: logging.Null(), timeout: time.Second,
		args: func(int) []string { return nil },
		ready: func(line string) (string, bool) {
			value := ParseQuickURL(line)
			return value, value != ""
		},
	}
	url, err := provider.Start(context.Background(), 1234)
	if err != nil || url != "https://blue-bird.trycloudflare.com" {
		t.Fatalf("Start = %q, %v", url, err)
	}
	if repeated, err := provider.Start(context.Background(), 1234); err != nil || repeated != url {
		t.Fatalf("idempotent Start = %q, %v", repeated, err)
	}
	status := provider.Status()
	if !status.Running || status.URL != url || provider.Name() != "test" {
		t.Fatalf("status = %+v", status)
	}
	report := provider.Doctor(context.Background())
	if !report.BinaryFound || !report.Running || len(report.Problems) != 0 {
		t.Fatalf("doctor = %+v", report)
	}
	if err := provider.Stop(); err != nil {
		t.Fatal(err)
	}
	if provider.Status().Running {
		t.Fatal("provider remained running after Stop")
	}
}

func TestProcessProviderReportsFailureAndTimeout(t *testing.T) {
	failing := &processProvider{
		name: "failure", binary: writeTunnelScript(t, "#!/bin/sh\necho 'fatal tunnel failed' >&2\nexit 7\n"),
		logger: logging.Null(), timeout: time.Second, args: func(int) []string { return nil },
		ready: func(string) (string, bool) { return "", false },
	}
	if _, err := failing.Start(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "fatal tunnel failed") {
		t.Fatalf("failure error = %v", err)
	}
	if !strings.Contains(failing.Status().Detail, "fatal tunnel failed") {
		t.Fatalf("failure detail = %+v", failing.Status())
	}

	timingOut := &processProvider{
		name: "timeout", binary: writeTunnelScript(t, "#!/bin/sh\nwhile :; do sleep 1; done\n"),
		logger: logging.Null(), timeout: 80 * time.Millisecond, args: func(int) []string { return nil },
		ready: func(string) (string, bool) { return "", false },
	}
	if _, err := timingOut.Start(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	if timingOut.Status().Running {
		t.Fatal("timed-out provider remained running")
	}
}

func writeTunnelScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcessProviderFailedStartDoesNotAdvanceGeneration(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(binary, []byte("not a program"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &processProvider{
		name: "failed-start", binary: binary, logger: logging.Null(), timeout: time.Second,
		args: func(int) []string { return nil }, ready: func(string) (string, bool) { return "", false },
	}
	if _, err := provider.Start(context.Background(), 1); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if provider.generation != 0 || provider.cmd != nil {
		t.Fatalf("failed start mutated lifecycle state: generation=%d cmd=%v", provider.generation, provider.cmd)
	}
}

func TestLineWriterBoundsUnterminatedOutput(t *testing.T) {
	var lines []string
	writer := newLineWriter(func(line string) { lines = append(lines, line) })
	payload := strings.Repeat("x", maxTunnelLineBytes*3)
	if n, err := writer.Write([]byte(payload)); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(writer.pending) > maxTunnelLineBytes {
		t.Fatalf("pending output grew to %d bytes", len(writer.pending))
	}
	if len(lines) != 1 || len(lines[0]) != maxTunnelLineBytes {
		t.Fatalf("bounded line output = %d lines, first length %d", len(lines), len(lines[0]))
	}
	if _, err := writer.Write([]byte("ignored\nready\n")); err != nil {
		t.Fatal(err)
	}
	if got := lines[len(lines)-1]; got != "ready" {
		t.Fatalf("writer did not recover after long line: %q", got)
	}
}

func TestProcessProviderRejectsInvalidPort(t *testing.T) {
	provider := &processProvider{
		name: "invalid-port", binary: writeTunnelScript(t, "#!/bin/sh\nexit 0\n"), logger: logging.Null(),
		args: func(int) []string { return nil }, ready: func(string) (string, bool) { return "", false },
	}
	for _, port := range []int{0, -1, 65536} {
		if _, err := provider.Start(context.Background(), port); err == nil {
			t.Fatalf("port %d was accepted", port)
		}
	}
}
