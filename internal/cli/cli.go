package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joeykchen/codexlink/internal/bridge"
	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/config"
	"github.com/joeykchen/codexlink/internal/logging"
	"github.com/joeykchen/codexlink/internal/openurl"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
	setupflow "github.com/joeykchen/codexlink/internal/setup"
	"github.com/joeykchen/codexlink/internal/workspace"
)

type App struct {
	Stdout  io.Writer
	Stderr  io.Writer
	OpenURL func(context.Context, string) error
}

func New() *App {
	return &App{Stdout: os.Stdout, Stderr: os.Stderr, OpenURL: openurl.Open}
}

func (a *App) Run(ctx context.Context, args []string) int {
	// State migration is intentionally best-effort. A stale or damaged legacy
	// directory must never prevent CodexLink from starting with a clean state.
	_, _ = config.MigrateLegacyState()
	if len(args) == 0 {
		return a.commandSetup(ctx, nil)
	}
	// Setup flags are accepted directly so `codexlink --json` and
	// `codexlink --workspace /path` remain true one-command workflows.
	if strings.HasPrefix(args[0], "-") && !isHelpArgument(args[0]) && args[0] != "--version" && args[0] != "-version" && args[0] != "-v" {
		return a.commandSetup(ctx, args)
	}
	switch args[0] {
	case "--version", "-version", "version", "-v":
		fmt.Fprintf(a.Stdout, "%s %s\n", buildinfo.ProductName, buildinfo.Version)
		return 0
	case "help", "--help", "-h":
		a.help()
		return 0
	case "serve":
		return a.commandServe(ctx, args[1:])
	case "start":
		return a.commandStart(ctx, args[1:])
	case "setup", "connect":
		return a.commandSetup(ctx, args[1:])
	case "install":
		return a.commandInstall(args[1:])
	case "stop":
		return a.commandStop(ctx, args[1:])
	case "restart":
		return a.commandRestart(ctx, args[1:])
	case "status":
		return a.commandStatus(ctx, args[1:])
	case "doctor":
		return a.commandDoctor(ctx, args[1:])
	case "pair":
		return a.commandPair(ctx, args[1:])
	case "unpair":
		return a.commandUnpair(ctx, args[1:])
	case "logs":
		return a.commandLogs(args[1:])
	case "workspace":
		return a.commandWorkspace(ctx, args[1:])
	case "sandbox-allow":
		return a.commandSandboxAllow(args[1:])
	case "session":
		return a.commandSession(args[1:])
	case "record":
		return a.commandRecord(args[1:])
	case "control":
		return a.commandControl(ctx, args[1:])
	case "tunnel":
		return a.commandTunnel(ctx, args[1:])
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n", args[0])
		a.help()
		return 2
	}
}

func (a *App) help() {
	fmt.Fprintln(a.Stdout, `CodexLink — securely connect a local coding workspace to ChatGPT for read-only planning and review.

Usage:
  codexlink [setup-options]
  codexlink <command> [options]

Run without a command for one-step local setup.

Core commands:
  setup, connect  Configure Codex, start bridge + tunnel, and open the setup page
  status          Show bridge, tunnel, endpoint, and authorization state
  stop            Stop the bridge for the current workspace
  doctor          Diagnose dependencies and safe local configuration
  pair            Generate a fresh one-time OAuth pairing code
  unpair          Revoke every OAuth token for this workspace

Advanced commands:
  start, restart  Manage the workspace bridge directly
  install         Install/update the Codex skill and sandbox allowlist
  workspace       Show detected project and Git metadata
  logs            Print recent redacted bridge logs
  session         Manage saved ChatGPT conversation metadata
  tunnel          Configure Cloudflare quick/named tunnels
  record          Append a local execution summary for independent review
  control         Prepare and receive structured planning/review responses

Examples:
  codexlink
  codexlink --json
  codexlink status
  codexlink stop

Run "codexlink <command> --help" for command-specific flags.`)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() {
		fmt.Fprintf(set.Output(), "Usage: codexlink %s [options]\n", name)
		set.PrintDefaults()
	}
	return set
}

func isHelpArgument(value string) bool {
	return value == "--help" || value == "-h" || value == "help"
}

func containsHelpArgument(args []string) bool {
	for _, arg := range args {
		if isHelpArgument(arg) {
			return true
		}
	}
	return false
}

func (a *App) parseFlags(set *flag.FlagSet, args []string) (bool, int) {
	if containsHelpArgument(args) {
		set.SetOutput(a.Stdout)
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func workspaceFlags(set *flag.FlagSet) *string {
	workspaceRoot := set.String("workspace", ".", "workspace root")
	set.StringVar(workspaceRoot, "w", ".", "workspace root (shorthand)")
	return workspaceRoot
}

func (a *App) fail(err error) int {
	fmt.Fprintf(a.Stderr, "error: %v\n", err)
	return 1
}

func (a *App) emitJSON(value any) int {
	encoder := json.NewEncoder(a.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *App) commandServe(ctx context.Context, args []string) int {
	set := newFlagSet("serve", a.Stderr)
	root := workspaceFlags(set)
	port := set.Int("port", config.DefaultPort, "preferred loopback port")
	host := set.String("host", config.DefaultHost, "loopback host")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	logger, err := logging.New("serve", true)
	if err != nil {
		return a.fail(err)
	}
	server, err := bridge.Start(bridge.Options{WorkspaceRoot: *root, Port: *port, Host: *host, Logger: logger})
	if err != nil {
		_ = logger.Close()
		return a.fail(err)
	}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		_ = server.Close()
	case <-server.Done():
	}
	return 0
}

func (a *App) commandStart(ctx context.Context, args []string) int {
	set := newFlagSet("start", a.Stderr)
	root := workspaceFlags(set)
	port := set.Int("port", 0, "preferred port")
	withTunnel := set.Bool("tunnel", false, "also start the configured public tunnel")
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, result, err := ensureBridge(ctx, *root, *port)
	if err != nil {
		return a.fail(err)
	}
	state := result.State
	if *withTunnel {
		if _, err := ensureTunnel(ctx, &state); err != nil {
			return a.fail(err)
		}
	}
	if *jsonOutput {
		return a.emitJSON(map[string]any{"workspaceId": ws.ID, "workspaceName": ws.Name, "spawned": result.Spawned, "port": state.Port, "publicUrl": emptyNil(state.PublicURL)})
	}
	action := "reused"
	if result.Spawned {
		action = "started"
	}
	fmt.Fprintf(a.Stdout, "Bridge %s for %s on %s\n", action, ws.Name, localURL(state.Port))
	if state.PublicURL != "" {
		fmt.Fprintf(a.Stdout, "Public MCP endpoint: %s/mcp\n", strings.TrimRight(state.PublicURL, "/"))
	}
	return 0
}

func (a *App) commandSetup(ctx context.Context, args []string) int {
	set := newFlagSet("setup", a.Stderr)
	root := workspaceFlags(set)
	port := set.Int("port", 0, "preferred loopback port")
	noTunnel := set.Bool("no-tunnel", false, "local-only mode; ChatGPT cannot reach this endpoint")
	noOpen := set.Bool("no-open", false, "do not open the local setup page")
	noCodex := set.Bool("no-codex", false, "skip Codex skill and sandbox configuration")
	reconnect := set.Bool("reconnect", false, "force a fresh ChatGPT authorization")
	jsonOutput := set.Bool("json", false, "print JSON and do not open a browser")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}

	result, err := setupflow.New().Run(ctx, setupflow.Options{
		WorkspaceRoot: *root,
		Port:          *port,
		LocalOnly:     *noTunnel,
		SkipCodex:     *noCodex,
		Reconnect:     *reconnect,
	})
	if err != nil {
		return a.fail(err)
	}

	opened := false
	openError := ""
	shouldOpen := result.AuthorizationRequired && result.SetupURL != "" && !*jsonOutput && !*noOpen && os.Getenv("CODEXLINK_NO_BROWSER") != "1"
	if shouldOpen {
		opener := a.OpenURL
		if opener == nil {
			opener = openurl.Open
		}
		openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := opener(openCtx, result.SetupURL)
		cancel()
		if err != nil {
			openError = err.Error()
		} else {
			opened = true
		}
	}

	payload := map[string]any{
		"ok": true, "workspaceId": result.WorkspaceID, "workspaceName": result.WorkspaceName,
		"workspaceRoot": result.WorkspaceRoot, "workspaceMode": result.WorkspaceMode,
		"repositoryCount": result.RepositoryCount, "spawned": result.Spawned, "port": result.Port,
		"publicUrl": emptyNil(result.PublicURL), "mcpUrl": result.MCPURL,
		"connectorName": result.ConnectorName, "connectorAction": result.ConnectorAction,
		"authorized": result.Authorized, "authorizationRequired": result.AuthorizationRequired,
		"tokenCount": result.TokenCount, "controlResponseAuthorized": result.ControlResponseAuthorized, "setupPageOpened": opened,
		"localOnly": result.LocalOnly, "codex": result.Codex,
	}
	if result.AuthorizationRequired {
		payload["pairingCode"] = result.PairingCode
		payload["pairingExpiresAt"] = result.PairingExpiresAt
		payload["setupUrl"] = result.SetupURL
	}
	if openError != "" {
		payload["openError"] = openError
	}
	if *jsonOutput {
		return a.emitJSON(payload)
	}

	fmt.Fprintln(a.Stdout, "CodexLink")
	fmt.Fprintln(a.Stdout)
	fmt.Fprintf(a.Stdout, "✓ Workspace       %s (%s, %d repositories)\n", result.WorkspaceName, result.WorkspaceMode, result.RepositoryCount)
	fmt.Fprintf(a.Stdout, "✓ Bridge          running on 127.0.0.1:%d\n", result.Port)
	if result.LocalOnly {
		fmt.Fprintln(a.Stdout, "! Public access   disabled (local-only mode)")
	} else {
		fmt.Fprintln(a.Stdout, "✓ Public access   connected")
	}
	if result.Codex.Configured {
		fmt.Fprintln(a.Stdout, "✓ Codex           skill and sandbox configured")
	} else if !result.Codex.Skipped {
		fmt.Fprintln(a.Stdout, "! Codex           bridge ready; integration needs attention")
	}
	fmt.Fprintln(a.Stdout)
	if !result.AuthorizationRequired {
		fmt.Fprintln(a.Stdout, "✓ ChatGPT         already authorized")
		fmt.Fprintln(a.Stdout)
		fmt.Fprintln(a.Stdout, "Ready.")
		return 0
	}
	if opened {
		fmt.Fprintln(a.Stdout, "The local setup page was opened in your browser.")
	} else {
		fmt.Fprintf(a.Stdout, "Open this local setup page:\n%s\n", result.SetupURL)
		if openError != "" {
			fmt.Fprintf(a.Stderr, "warning: could not open browser: %s\n", openError)
		}
	}
	fmt.Fprintln(a.Stdout, "Complete the one-time ChatGPT app authorization shown on that page.")
	return 0
}

func (a *App) commandStop(ctx context.Context, args []string) int {
	set := newFlagSet("stop", a.Stderr)
	root := workspaceFlags(set)
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, err := workspace.New(*root)
	if err != nil {
		return a.fail(err)
	}
	stopped, err := stateruntime.Stop(ctx, ws.ID)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOutput {
		return a.emitJSON(map[string]any{"stopped": stopped, "workspaceId": ws.ID})
	}
	if stopped {
		fmt.Fprintf(a.Stdout, "Stopped bridge for %s.\n", ws.Name)
	} else {
		fmt.Fprintf(a.Stdout, "No live bridge found for %s.\n", ws.Name)
	}
	return 0
}

func (a *App) commandRestart(ctx context.Context, args []string) int {
	set := newFlagSet("restart", a.Stderr)
	root := workspaceFlags(set)
	port := set.Int("port", 0, "preferred port")
	withTunnel := set.Bool("tunnel", false, "force tunnel startup")
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, err := workspace.New(*root)
	if err != nil {
		return a.fail(err)
	}
	previous, _ := stateruntime.Read(ws.ID)
	shouldTunnel := *withTunnel || (previous != nil && previous.PublicURL != "")
	_, _ = stateruntime.Stop(ctx, ws.ID)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if live, _ := stateruntime.FindLive(ctx, ws.ID); live == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, ensured, err := ensureBridge(ctx, ws.Root, *port)
	if err != nil {
		return a.fail(err)
	}
	state := ensured.State
	if shouldTunnel {
		if _, err := ensureTunnel(ctx, &state); err != nil {
			return a.fail(err)
		}
	}
	if *jsonOutput {
		return a.emitJSON(map[string]any{"restarted": true, "workspaceId": ws.ID, "port": state.Port, "publicUrl": emptyNil(state.PublicURL)})
	}
	fmt.Fprintf(a.Stdout, "Restarted bridge for %s on port %d.\n", ws.Name, state.Port)
	if state.PublicURL != "" {
		fmt.Fprintf(a.Stdout, "Public MCP endpoint: %s/mcp\n", strings.TrimRight(state.PublicURL, "/"))
	}
	return 0
}
