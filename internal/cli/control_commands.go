package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/joeykchen/codexlink/internal/control"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
	"github.com/joeykchen/codexlink/internal/workspace"
)

func (a *App) commandControl(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelpArgument(args[0]) {
		fmt.Fprintln(a.Stdout, "Usage: codexlink control <prepare|wait|get|cancel> [options]")
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "prepare":
		return a.controlPrepare(ctx, args[1:])
	case "wait", "get":
		return a.controlWait(ctx, args[0], args[1:])
	case "cancel":
		return a.controlCancel(ctx, args[1:])
	default:
		return a.fail(fmt.Errorf("unknown control command %q", args[0]))
	}
}

func (a *App) liveControl(ctx context.Context, root string) (stateruntime.State, error) {
	ws, err := workspace.New(root)
	if err != nil {
		return stateruntime.State{}, err
	}
	live, err := stateruntime.FindLive(ctx, ws.ID)
	if err != nil {
		return stateruntime.State{}, err
	}
	if live == nil {
		return stateruntime.State{}, fmt.Errorf("CodexLink bridge is not running for this workspace")
	}
	return *live, nil
}

func (a *App) controlPrepare(ctx context.Context, args []string) int {
	set := newFlagSet("control prepare", a.Stderr)
	root := workspaceFlags(set)
	task := set.String("task-id", "", "task identifier")
	iteration := set.Int("iteration", 0, "iteration number")
	ttl := set.Duration("ttl", control.DefaultTTL, "request lifetime")
	jsonOut := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	if *ttl < control.MinTTL || *ttl > control.MaxTTL {
		return a.fail(fmt.Errorf("--ttl must be between 1m and 4h"))
	}
	live, err := a.liveControl(ctx, *root)
	if err != nil {
		return a.fail(err)
	}
	var out control.Request
	err = stateruntime.AdminRequest(ctx, live, http.MethodPost, "/admin/control/prepare", map[string]any{"taskId": *task, "iteration": *iteration, "ttlSeconds": int(ttl.Seconds())}, &out)
	if err != nil {
		return a.fail(err)
	}
	if out.RequestID == "" || out.TaskID != *task || out.Iteration != *iteration || (out.Status != "pending" && out.Status != "submitted") {
		return a.fail(fmt.Errorf("bridge returned a mismatched control request"))
	}
	if *jsonOut {
		return a.emitJSON(out)
	}
	fmt.Fprintf(a.Stdout, "Prepared %s for %s iteration %d.\n", out.RequestID, out.TaskID, out.Iteration)
	return 0
}

func (a *App) controlWait(ctx context.Context, command string, args []string) int {
	set := newFlagSet("control "+command, a.Stderr)
	root := workspaceFlags(set)
	requestID := set.String("request-id", "", "prepared request identifier")
	task := set.String("task-id", "", "task identifier")
	iteration := set.Int("iteration", 0, "iteration number")
	timeout := set.Duration("timeout", control.DefaultWait, "overall wait (maximum 4h)")
	jsonOut := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	if command == "get" {
		*timeout = 0
	}
	if *timeout < 0 || *timeout > control.MaxWait {
		return a.fail(fmt.Errorf("--timeout must be between 0 and 4h"))
	}
	live, err := a.liveControl(ctx, *root)
	if err != nil {
		return a.fail(err)
	}
	deadline := time.Now().Add(*timeout)
	for {
		remaining := time.Until(deadline)
		wait := remaining
		if wait > 25*time.Second {
			wait = 25 * time.Second
		}
		if wait < 0 {
			wait = 0
		}
		var out control.Request
		err = stateruntime.AdminRequest(ctx, live, http.MethodPost, "/admin/control/wait", map[string]any{"requestId": *requestID, "taskId": *task, "iteration": *iteration, "waitMs": int(wait.Milliseconds())}, &out)
		if err != nil {
			return a.fail(err)
		}
		if out.RequestID != *requestID || out.TaskID != *task || out.Iteration != *iteration || (out.Status != "pending" && out.Status != "submitted") {
			return a.fail(fmt.Errorf("bridge returned a mismatched control response"))
		}
		if out.Status == "submitted" {
			if out.Response == nil || out.Response.RequestID != out.RequestID || out.Response.TaskID != out.TaskID || out.Response.Iteration != out.Iteration {
				return a.fail(fmt.Errorf("bridge returned an invalid submitted response"))
			}
			payload := map[string]any{"received": true, "requestId": out.RequestID, "taskId": out.TaskID, "iteration": out.Iteration, "state": out.Response.State, "summary": out.Response.Summary}
			if out.Response.State == control.StatePlan {
				payload["plan"] = out.Response.Plan
			}
			if *jsonOut {
				return a.emitJSON(payload)
			}
			fmt.Fprintf(a.Stdout, "Received %s for %s iteration %d.\n", out.Response.State, out.TaskID, out.Iteration)
			return 0
		}
		if command == "get" || !time.Now().Before(deadline) {
			payload := map[string]any{"received": false, "status": map[bool]string{true: "pending", false: "timeout"}[command == "get"], "requestId": *requestID, "taskId": *task, "iteration": *iteration}
			if *jsonOut {
				return a.emitJSON(payload)
			}
			fmt.Fprintln(a.Stdout, "No control response received.")
			return 0
		}
	}
}

func (a *App) controlCancel(ctx context.Context, args []string) int {
	set := newFlagSet("control cancel", a.Stderr)
	root := workspaceFlags(set)
	requestID := set.String("request-id", "", "prepared request identifier")
	task := set.String("task-id", "", "task identifier")
	iteration := set.Int("iteration", 0, "iteration number")
	jsonOut := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	live, err := a.liveControl(ctx, *root)
	if err != nil {
		return a.fail(err)
	}
	var out struct {
		Cancelled bool             `json:"cancelled"`
		Request   *control.Request `json:"request"`
	}
	err = stateruntime.AdminRequest(ctx, live, http.MethodPost, "/admin/control/cancel", map[string]any{"requestId": *requestID, "taskId": *task, "iteration": *iteration}, &out)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.emitJSON(out)
	}
	if out.Cancelled {
		fmt.Fprintln(a.Stdout, "Control request cancelled.")
	} else if out.Request != nil && out.Request.Status == "submitted" {
		fmt.Fprintln(a.Stdout, "A submitted control response won the cancellation race; use control get.")
	} else {
		fmt.Fprintln(a.Stdout, "Control request was not cancelled.")
	}
	return 0
}
