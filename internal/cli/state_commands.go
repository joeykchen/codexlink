package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joeykchen/codexlink/internal/execution"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
	"github.com/joeykchen/codexlink/internal/session"
	"github.com/joeykchen/codexlink/internal/tunnel"
	"github.com/joeykchen/codexlink/internal/workspace"
)

func (a *App) commandSession(args []string) int {
	if len(args) == 0 {
		a.sessionHelp(a.Stderr)
		return 2
	}
	if isHelpArgument(args[0]) {
		a.sessionHelp(a.Stdout)
		return 0
	}
	switch args[0] {
	case "get":
		set := newFlagSet("session get", a.Stderr)
		root := workspaceFlags(set)
		jsonOutput := set.Bool("json", false, "print JSON")
		if ok, code := a.parseFlags(set, args[1:]); !ok {
			return code
		}
		ws, err := workspace.New(*root)
		if err != nil {
			return a.fail(err)
		}
		saved, err := session.Read(ws.ID)
		if err != nil {
			return a.fail(err)
		}
		payload := map[string]any{"workspaceId": ws.ID, "saved": saved, "resolved": session.Resolve(saved)}
		if *jsonOutput {
			return a.emitJSON(payload)
		}
		view := session.Resolve(saved)
		fmt.Fprintf(a.Stdout, "Mode: %s (%s)\n", view.Mode, view.Reason)
		if view.ProjectURL != nil {
			fmt.Fprintf(a.Stdout, "Project: %s\n", *view.ProjectURL)
		}
		if view.ChatURL != nil {
			fmt.Fprintf(a.Stdout, "Chat: %s\n", *view.ChatURL)
		}
		return 0
	case "set":
		set := newFlagSet("session set", a.Stderr)
		root := workspaceFlags(set)
		urlValue := set.String("url", "", "ChatGPT conversation URL")
		title := set.String("title", "", "conversation title")
		taskID := set.String("task-id", "", "task identifier")
		iteration := set.Int("iteration", -1, "iteration number")
		lastState := set.String("last-state", "", "last workflow state")
		modeValue := set.String("mode", "", "long-chat or project")
		projectURL := set.String("project-url", "", "ChatGPT Project URL")
		connectorName := set.String("connector-name", "", "saved connector name")
		jsonOutput := set.Bool("json", false, "print JSON")
		if ok, code := a.parseFlags(set, args[1:]); !ok {
			return code
		}
		ws, err := workspace.New(*root)
		if err != nil {
			return a.fail(err)
		}
		previous, err := session.Read(ws.ID)
		if err != nil {
			return a.fail(err)
		}
		patch := session.Patch{}
		if *urlValue != "" {
			patch.URL = urlValue
		}
		if *title != "" {
			patch.Title = title
		}
		if *taskID != "" {
			patch.TaskID = taskID
		}
		if *iteration >= 0 {
			patch.Iteration = iteration
		}
		if *lastState != "" {
			patch.LastState = lastState
		}
		if *projectURL != "" {
			patch.ProjectURL = projectURL
		}
		if *connectorName != "" {
			patch.ConnectorName = connectorName
		}
		if *modeValue != "" {
			mode := session.Mode(*modeValue)
			if mode != session.ModeLongChat && mode != session.ModeProject {
				return a.fail(fmt.Errorf("mode must be long-chat or project"))
			}
			patch.ConversationMode = &mode
		}
		next, err := session.Merge(previous, patch)
		if err != nil {
			return a.fail(err)
		}
		saved, err := session.Write(ws.ID, *next)
		if err != nil {
			return a.fail(err)
		}
		if *jsonOutput {
			return a.emitJSON(map[string]any{"workspaceId": ws.ID, "saved": saved, "resolved": session.Resolve(saved)})
		}
		fmt.Fprintf(a.Stdout, "Saved %s conversation metadata for %s.\n", session.Resolve(saved).Mode, ws.Name)
		return 0
	case "clear":
		set := newFlagSet("session clear", a.Stderr)
		root := workspaceFlags(set)
		jsonOutput := set.Bool("json", false, "print JSON")
		if ok, code := a.parseFlags(set, args[1:]); !ok {
			return code
		}
		ws, err := workspace.New(*root)
		if err != nil {
			return a.fail(err)
		}
		cleared, keptProject, err := session.ClearChat(ws.ID)
		if err != nil {
			return a.fail(err)
		}
		if *jsonOutput {
			return a.emitJSON(map[string]any{"cleared": cleared, "keptProject": keptProject, "workspaceId": ws.ID})
		}
		if !cleared {
			fmt.Fprintln(a.Stdout, "No saved session was found.")
		} else if keptProject {
			fmt.Fprintln(a.Stdout, "Cleared the chat pointer and kept the Project binding.")
		} else {
			fmt.Fprintln(a.Stdout, "Cleared the saved session.")
		}
		return 0
	default:
		fmt.Fprintf(a.Stderr, "unknown session command %q\n", args[0])
		return 2
	}
}

func (a *App) commandRecord(args []string) int {
	set := newFlagSet("record", a.Stderr)
	root := workspaceFlags(set)
	taskID := set.String("task-id", "", "task identifier")
	iteration := set.Int("iteration", 0, "iteration number")
	changed := set.String("changed-files", "", "comma-separated paths or a numeric count")
	tests := set.String("tests", "", "test command/status summary")
	exitStatus := set.String("exit-status", "ok", "ok, failed, blocked, or another status")
	notes := set.String("notes", "", "optional notes")
	timestamp := set.String("timestamp", "", "RFC3339 timestamp; defaults to now")
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	if strings.TrimSpace(*taskID) == "" {
		return a.fail(fmt.Errorf("--task-id is required"))
	}
	if *iteration < 0 {
		return a.fail(fmt.Errorf("--iteration cannot be negative"))
	}
	ws, err := workspace.New(*root)
	if err != nil {
		return a.fail(err)
	}
	when := time.Now().UTC()
	if *timestamp != "" {
		when, err = time.Parse(time.RFC3339, *timestamp)
		if err != nil {
			return a.fail(fmt.Errorf("invalid timestamp: %w", err))
		}
	}
	var changedFiles any = []string{}
	if value := strings.TrimSpace(*changed); value != "" {
		if count, parseErr := strconv.Atoi(value); parseErr == nil {
			changedFiles = count
		} else {
			paths := make([]string, 0)
			for _, path := range strings.Split(value, ",") {
				if path = strings.TrimSpace(path); path != "" {
					paths = append(paths, path)
				}
			}
			changedFiles = paths
		}
	}
	var testPointer *string
	if *tests != "" {
		value := *tests
		testPointer = &value
	}
	record := execution.Record{TaskID: *taskID, Iteration: *iteration, ChangedFiles: changedFiles, Tests: testPointer, ExitStatus: *exitStatus, Timestamp: when.UTC().Format(time.RFC3339Nano), Notes: *notes}
	if err := execution.Append(ws.ID, record); err != nil {
		return a.fail(err)
	}
	if *jsonOutput {
		return a.emitJSON(map[string]any{"recorded": true, "workspaceId": ws.ID, "record": record})
	}
	fmt.Fprintf(a.Stdout, "Recorded task %s iteration %d for %s.\n", record.TaskID, record.Iteration, ws.Name)
	return 0
}

func (a *App) commandTunnel(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.tunnelHelp(a.Stderr)
		return 2
	}
	if isHelpArgument(args[0]) {
		a.tunnelHelp(a.Stdout)
		return 0
	}
	switch args[0] {
	case "status":
		set := newFlagSet("tunnel status", a.Stderr)
		root := workspaceFlags(set)
		jsonOutput := set.Bool("json", false, "print JSON")
		if ok, code := a.parseFlags(set, args[1:]); !ok {
			return code
		}
		ws, err := workspace.New(*root)
		if err != nil {
			return a.fail(err)
		}
		state, err := tunnel.ReadState(ws.ID)
		if err != nil {
			return a.fail(err)
		}
		var liveStatus any
		live, _ := stateruntime.FindLive(ctx, ws.ID)
		if live != nil {
			var info struct {
				Tunnel    any `json:"tunnel"`
				PublicURL any `json:"publicUrl"`
			}
			if err := stateruntime.AdminRequest(ctx, *live, http.MethodGet, "/admin/info", nil, &info); err == nil {
				liveStatus = info.Tunnel
			}
		}
		payload := map[string]any{"workspaceId": ws.ID, "configuration": state, "live": liveStatus, "restartRequired": false}
		if *jsonOutput {
			return a.emitJSON(payload)
		}
		fmt.Fprintf(a.Stdout, "Preference: %s\nProvider: %s\n", state.Preference, state.Provider)
		if state.Hostname != "" {
			fmt.Fprintf(a.Stdout, "Hostname: %s\n", state.Hostname)
		}
		if liveStatus == nil {
			fmt.Fprintln(a.Stdout, "Live tunnel: unavailable (bridge is stopped)")
		} else {
			fmt.Fprintf(a.Stdout, "Live tunnel: %v\n", liveStatus)
		}
		return 0
	case "choose":
		if len(args) < 2 {
			a.tunnelChooseHelp(a.Stderr)
			return 2
		}
		if isHelpArgument(args[1]) {
			a.tunnelChooseHelp(a.Stdout)
			return 0
		}
		choice := args[1]
		set := newFlagSet("tunnel choose", a.Stderr)
		root := workspaceFlags(set)
		zone := set.String("zone", "", "Cloudflare DNS zone for named mode")
		hostname := set.String("hostname", "", "optional full hostname for named mode")
		jsonOutput := set.Bool("json", false, "print JSON")
		if ok, code := a.parseFlags(set, args[2:]); !ok {
			return code
		}
		ws, err := workspace.New(*root)
		if err != nil {
			return a.fail(err)
		}
		live, _ := stateruntime.FindLive(ctx, ws.ID)
		restartRequired := live != nil
		if choice == "quick" {
			state, err := tunnel.ChooseQuick(ws.ID, "user_choice")
			if err != nil {
				return a.fail(err)
			}
			payload := map[string]any{"ok": true, "state": state, "restartRequired": restartRequired}
			if *jsonOutput {
				return a.emitJSON(payload)
			}
			fmt.Fprintln(a.Stdout, "Selected Cloudflare Quick Tunnel. Restart the bridge to apply the provider change.")
			return 0
		}
		if choice != "named" {
			return a.fail(fmt.Errorf("choice must be quick or named"))
		}
		if strings.TrimSpace(*zone) == "" {
			return a.fail(fmt.Errorf("--zone is required for named mode"))
		}
		result := tunnel.ProvisionNamed(ctx, ws.ID, ws.Name, *zone, *hostname, nil)
		payload := map[string]any{"result": result, "restartRequired": restartRequired}
		if *jsonOutput {
			return a.emitJSON(payload)
		}
		if result.Fallback {
			fmt.Fprintf(a.Stdout, "%s\nReason: %s\n", result.UserMessage, result.Error)
			return 0
		}
		if !result.OK {
			return a.fail(fmt.Errorf("named tunnel setup failed: %s", result.Error))
		}
		fmt.Fprintf(a.Stdout, "Configured named tunnel at https://%s. Restart the bridge to apply it.\n", result.State.Hostname)
		return 0
	case "login":
		set := newFlagSet("tunnel login", a.Stderr)
		jsonOutput := set.Bool("json", false, "print JSON")
		if ok, code := a.parseFlags(set, args[1:]); !ok {
			return code
		}
		account := tunnel.ProcessAccount{}
		if err := account.Login(ctx); err != nil {
			return a.fail(err)
		}
		if *jsonOutput {
			return a.emitJSON(map[string]any{"ok": true, "certificatePresent": account.HasCert()})
		}
		fmt.Fprintln(a.Stdout, "Cloudflare account authorization is ready.")
		return 0
	default:
		fmt.Fprintf(a.Stderr, "unknown tunnel command %q\n", args[0])
		return 2
	}
}

func (a *App) sessionHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage: codexlink session <command> [options]

Commands:
  get     Show saved ChatGPT conversation and Project metadata
  set     Save or update conversation and Project metadata
  clear   Forget the current chat pointer while preserving a Project binding`)
}

func (a *App) tunnelHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage: codexlink tunnel <command> [options]

Commands:
  status              Show configured and live tunnel state
  choose quick        Select a temporary Cloudflare Quick Tunnel
  choose named        Provision or select a stable named tunnel
  login               Authorize cloudflared for named-tunnel management`)
}

func (a *App) tunnelChooseHelp(output io.Writer) {
	fmt.Fprintln(output, `Usage: codexlink tunnel choose <quick|named> [options]

Options:
  --workspace, -w PATH   workspace root
  --zone DOMAIN          Cloudflare DNS zone (required for named mode)
  --hostname HOST        optional full hostname override
  --json                 print JSON`)
}
