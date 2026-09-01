//go:build !windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnsureCleansUpTimedOutProcess(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("CODEXLINK_TEST_PID_FILE", pidFile)
	script := filepath.Join(t.TempDir(), "slow-bridge.sh")
	body := "#!/bin/sh\necho $$ > \"$CODEXLINK_TEST_PID_FILE\"\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := Ensure(context.Background(), t.TempDir(), "slow-workspace", EnsureOptions{
		Executable: script, StartupTimeout: 10 * time.Second, ProbeInterval: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
		t.Fatalf("Ensure error = %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed-out bridge process %d is still alive", pid)
}

func TestStopProcessByPIDTerminatesDetachedProcess(t *testing.T) {
	command, done := startDetachedSleeper(t)
	if err := stopProcessByPID(command.Process.Pid, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("detached process was not reaped")
	}
}

func TestStopFallsBackWhenAdminShutdownFails(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	command, done := startDetachedSleeper(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(response, `{"service":"codexlink-bridge","version":"test","workspaceRef":"ref","pid":%d,"status":"ok"}`, command.Process.Pid)
	})
	mux.HandleFunc("/admin/shutdown", func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "admin unavailable", http.StatusInternalServerError)
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := Write(State{
		Service: "codexlink-bridge", WorkspaceID: "fallback", PublicRef: "ref", PID: command.Process.Pid,
		Port: port, AdminToken: "invalid", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	stopped, err := Stop(context.Background(), "fallback")
	if err != nil || !stopped {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback process was not reaped")
	}
	if state, err := Read("fallback"); err != nil || state != nil {
		t.Fatalf("runtime state was not cleared: %+v, %v", state, err)
	}
}

func startDetachedSleeper(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "sleeper.sh")
	body := "#!/bin/sh\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(script)
	configureDetached(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = forceProcessStop(command.Process.Pid)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
	return command, done
}

func TestStopRefusesFallbackWhenHealthPIDDoesNotMatch(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	command, done := startDetachedSleeper(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(response, `{"service":"codexlink-bridge","version":"test","workspaceRef":"ref","pid":999999,"status":"ok"}`)
	})
	mux.HandleFunc("/admin/shutdown", func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "admin unavailable", http.StatusInternalServerError)
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := Write(State{
		Service: "codexlink-bridge", WorkspaceID: "stale-pid", PublicRef: "ref", PID: command.Process.Pid,
		Port: port, AdminToken: "invalid", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	stopped, err := Stop(context.Background(), "stale-pid")
	if err == nil || stopped || !strings.Contains(err.Error(), "refusing PID fallback") {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	select {
	case <-done:
		t.Fatal("mismatched PID process was terminated")
	default:
	}
}

func TestEnsureStopsStaleVersionBeforeReplacement(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	command, done := startDetachedSleeper(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(response, `{"service":"codexlink-bridge","version":"old","workspaceRef":"ref","pid":%d,"status":"ok"}`, command.Process.Pid)
	})
	mux.HandleFunc("/admin/shutdown", func(response http.ResponseWriter, _ *http.Request) {
		_ = requestProcessStop(command.Process.Pid)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"stopping":true}`))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := Write(State{
		Service: "codexlink-bridge", Version: "old", WorkspaceID: "upgrade", PublicRef: "ref",
		PID: command.Process.Pid, Port: port, AdminToken: "token", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(t.TempDir(), "missing-codexlink")
	_, err = Ensure(context.Background(), t.TempDir(), "upgrade", EnsureOptions{Executable: missing})
	if err == nil {
		t.Fatal("replacement with missing executable unexpectedly succeeded")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stale-version bridge was not stopped")
	}
	if state, readErr := Read("upgrade"); readErr != nil || state != nil {
		t.Fatalf("stale runtime state remained: %+v, %v", state, readErr)
	}
}
