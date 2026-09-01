package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/config"
)

const (
	defaultStartupTimeout = 20 * time.Second
	defaultProbeInterval  = 250 * time.Millisecond
	processStopGrace      = 2 * time.Second
)

type EnsureOptions struct {
	Port           int
	Executable     string
	StartupTimeout time.Duration
	ProbeInterval  time.Duration
}

type EnsureResult struct {
	State   State
	Spawned bool
}

func Ensure(ctx context.Context, workspaceRoot, workspaceID string, options EnsureOptions) (EnsureResult, error) {
	live, err := FindLive(ctx, workspaceID)
	if err != nil {
		return EnsureResult{}, err
	}
	if live != nil {
		if live.Version == buildinfo.Version {
			return EnsureResult{State: *live}, nil
		}
		stopped, stopErr := Stop(ctx, workspaceID)
		if stopErr != nil {
			return EnsureResult{}, fmt.Errorf("replace bridge version %q with %q: %w", live.Version, buildinfo.Version, stopErr)
		}
		if !stopped {
			return EnsureResult{}, fmt.Errorf("replace bridge version %q with %q: bridge did not stop", live.Version, buildinfo.Version)
		}
	}
	if err := ctx.Err(); err != nil {
		return EnsureResult{}, err
	}
	executable := options.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return EnsureResult{}, err
		}
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultStartupTimeout
	}
	if options.ProbeInterval <= 0 {
		options.ProbeInterval = defaultProbeInterval
	}

	logDir, err := config.StateSubdir("logs")
	if err != nil {
		return EnsureResult{}, err
	}
	logPath := filepath.Join(logDir, "bridge-"+workspaceID+".out.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return EnsureResult{}, err
	}
	_ = logFile.Chmod(0o600)

	args := []string{"serve", "--workspace", workspaceRoot}
	if options.Port > 0 {
		args = append(args, "--port", strconv.Itoa(options.Port))
	}
	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = os.Environ()
	configureDetached(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return EnsureResult{}, err
	}
	_ = logFile.Close()

	exitCh := make(chan error, 1)
	go func() { exitCh <- command.Wait() }()
	deadline := time.NewTimer(options.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(options.ProbeInterval)
	defer ticker.Stop()

	fail := func(cause error) (EnsureResult, error) {
		stopSpawnedProcess(command.Process.Pid, exitCh, processStopGrace)
		_ = Clear(workspaceID)
		return EnsureResult{}, fmt.Errorf("%w; see %s", cause, logPath)
	}

	for {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case waitErr := <-exitCh:
			_ = Clear(workspaceID)
			if waitErr == nil {
				waitErr = errors.New("bridge exited before becoming healthy")
			}
			return EnsureResult{}, fmt.Errorf("%w; see %s", waitErr, logPath)
		case <-deadline.C:
			return fail(errors.New("bridge did not become healthy"))
		case <-ticker.C:
			live, probeErr := FindLive(ctx, workspaceID)
			if probeErr == nil && live != nil {
				return EnsureResult{State: *live, Spawned: true}, nil
			}
		}
	}
}

func stopSpawnedProcess(pid int, exitCh <-chan error, grace time.Duration) {
	_ = requestProcessStop(pid)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exitCh:
		return
	case <-timer.C:
		_ = forceProcessStop(pid)
	}
	select {
	case <-exitCh:
	case <-time.After(time.Second):
	}
}

func Stop(ctx context.Context, workspaceID string) (bool, error) {
	value, err := Read(workspaceID)
	if err != nil || value == nil {
		return false, err
	}
	health, probeErr := Probe(ctx, value.Port)
	if probeErr != nil || health.WorkspaceRef != value.PublicRef {
		_ = Clear(workspaceID)
		return false, nil
	}
	var result map[string]any
	adminErr := AdminRequest(ctx, *value, "POST", "/admin/shutdown", nil, &result)
	if adminErr == nil && waitForProcessExit(value.PID, processStopGrace) {
		_ = Clear(workspaceID)
		return true, nil
	}
	// A PID returned by the health endpoint binds the local process to this
	// workspace. Without that binding, a stale runtime file must never be used
	// to terminate an unrelated process after PID reuse.
	if health.PID != value.PID || health.PID <= 1 {
		if adminErr != nil {
			return false, fmt.Errorf("admin shutdown failed: %w; refusing PID fallback without a matching health PID", adminErr)
		}
		return false, fmt.Errorf("bridge did not stop; refusing PID fallback without a matching health PID")
	}
	if stopErr := stopProcessByPID(value.PID, processStopGrace); stopErr != nil {
		if adminErr != nil {
			return false, fmt.Errorf("admin shutdown failed: %v; process fallback failed: %w", adminErr, stopErr)
		}
		return false, fmt.Errorf("bridge did not stop; process fallback failed: %w", stopErr)
	}
	_ = Clear(workspaceID)
	return true, nil
}

func waitForProcessExit(pid int, grace time.Duration) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	deadline := time.Now().Add(grace)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	return !processAlive(pid)
}

func stopProcessByPID(pid int, grace time.Duration) error {
	if pid <= 1 || pid == os.Getpid() {
		return fmt.Errorf("refusing to terminate unsafe PID %d", pid)
	}
	if !processAlive(pid) {
		return nil
	}
	if err := requestProcessStop(pid); err != nil && processAlive(pid) {
		return err
	}
	deadline := time.Now().Add(grace)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !processAlive(pid) {
		return nil
	}
	if err := forceProcessStop(pid); err != nil && processAlive(pid) {
		return err
	}
	forceDeadline := time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(forceDeadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if processAlive(pid) {
		return fmt.Errorf("process %d did not exit", pid)
	}
	return nil
}
