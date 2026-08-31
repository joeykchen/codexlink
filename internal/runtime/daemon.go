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

	"github.com/joeykchen/codexlink/internal/config"
)

type EnsureOptions struct{ Port int }

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
		return EnsureResult{State: *live}, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return EnsureResult{}, err
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
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return EnsureResult{}, ctx.Err()
		case waitErr := <-exitCh:
			if waitErr == nil {
				waitErr = errors.New("bridge exited before becoming healthy")
			}
			return EnsureResult{}, fmt.Errorf("%w; see %s", waitErr, logPath)
		case <-deadline.C:
			return EnsureResult{}, fmt.Errorf("bridge did not become healthy; see %s", logPath)
		case <-ticker.C:
			live, probeErr := FindLive(ctx, workspaceID)
			if probeErr == nil && live != nil {
				return EnsureResult{State: *live, Spawned: true}, nil
			}
		}
	}
}

func Stop(ctx context.Context, workspaceID string) (bool, error) {
	state, err := Read(workspaceID)
	if err != nil || state == nil {
		return false, err
	}
	health, probeErr := Probe(ctx, state.Port)
	if probeErr != nil || health.WorkspaceRef != state.PublicRef {
		_ = Clear(workspaceID)
		return false, nil
	}
	var result map[string]any
	if err := AdminRequest(ctx, *state, "POST", "/admin/shutdown", nil, &result); err != nil {
		return false, err
	}
	return true, nil
}
