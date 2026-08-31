package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/config"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
	"github.com/joeykchen/codexlink/internal/session"
	"github.com/joeykchen/codexlink/internal/tunnel"
	"github.com/joeykchen/codexlink/internal/workspace"
)

type pairingReply struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"`
}

func ensureBridge(ctx context.Context, root string, port int) (*workspace.Workspace, stateruntime.EnsureResult, error) {
	ws, err := workspace.New(root)
	if err != nil {
		return nil, stateruntime.EnsureResult{}, err
	}
	result, err := stateruntime.Ensure(ctx, ws.Root, ws.ID, stateruntime.EnsureOptions{Port: port})
	return ws, result, err
}

func ensureTunnel(ctx context.Context, state *stateruntime.State) (string, error) {
	var info struct {
		PublicURL string `json:"publicUrl"`
		Tunnel    struct {
			Running bool   `json:"running"`
			URL     string `json:"url"`
		} `json:"tunnel"`
	}
	if err := stateruntime.AdminRequest(ctx, *state, http.MethodGet, "/admin/info", nil, &info); err != nil {
		return "", err
	}
	if info.Tunnel.Running {
		url := strings.TrimRight(info.PublicURL, "/")
		if url == "" {
			url = strings.TrimRight(info.Tunnel.URL, "/")
		}
		if url != "" {
			state.PublicURL = url
			return url, nil
		}
	}
	var reply struct {
		URL string `json:"url"`
	}
	if err := stateruntime.AdminRequest(ctx, *state, http.MethodPost, "/admin/tunnel/start", nil, &reply); err != nil {
		return "", err
	}
	state.PublicURL = strings.TrimRight(reply.URL, "/")
	return state.PublicURL, nil
}

func createPairing(ctx context.Context, state stateruntime.State) (pairingReply, error) {
	var reply pairingReply
	err := stateruntime.AdminRequest(ctx, state, http.MethodPost, "/admin/pairing", nil, &reply)
	return reply, err
}

func localURL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }
func emptyNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *App) commandStatus(ctx context.Context, args []string) int {
	set := newFlagSet("status", a.Stderr)
	root := workspaceFlags(set)
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, err := workspace.New(*root)
	if err != nil {
		return a.fail(err)
	}
	repositories, err := ws.Repositories()
	if err != nil {
		return a.fail(err)
	}
	mode, err := ws.TopologyMode()
	if err != nil {
		return a.fail(err)
	}
	live, err := stateruntime.FindLive(ctx, ws.ID)
	if err != nil {
		return a.fail(err)
	}
	var info any
	if live != nil {
		var adminInfo map[string]any
		if err := stateruntime.AdminRequest(ctx, *live, http.MethodGet, "/admin/info", nil, &adminInfo); err == nil {
			info = adminInfo
		}
	}
	endpoint, endpointErr := config.ReadEndpoint(ws.ID)
	if endpointErr != nil {
		return a.fail(endpointErr)
	}
	savedSession, sessionErr := session.Read(ws.ID)
	if sessionErr != nil {
		return a.fail(sessionErr)
	}
	tunnelState, tunnelErr := tunnel.ReadState(ws.ID)
	if tunnelErr != nil {
		return a.fail(tunnelErr)
	}
	payload := map[string]any{
		"workspace": map[string]any{"id": ws.ID, "name": ws.Name, "root": ws.Root, "mode": mode, "repositoryCount": len(repositories)},
		"running":   live != nil, "bridge": info, "endpoint": endpoint,
		"conversation": session.Resolve(savedSession), "tunnelPreference": tunnelState,
	}
	if *jsonOutput {
		return a.emitJSON(payload)
	}
	fmt.Fprintf(a.Stdout, "Workspace: %s (%s)\nMode: %s; repositories: %d\n", ws.Name, ws.Root, mode, len(repositories))
	if live == nil {
		fmt.Fprintln(a.Stdout, "Bridge: stopped")
	} else {
		fmt.Fprintf(a.Stdout, "Bridge: running (pid %d, port %d)\n", live.PID, live.Port)
		if live.PublicURL != "" {
			fmt.Fprintf(a.Stdout, "Public MCP: %s/mcp\n", strings.TrimRight(live.PublicURL, "/"))
		}
	}
	fmt.Fprintf(a.Stdout, "Tunnel preference: %s\n", tunnelState.Preference)
	if endpoint != nil && endpoint.MCPURL != nil {
		fmt.Fprintf(a.Stdout, "Saved endpoint: %s\n", *endpoint.MCPURL)
	}
	view := session.Resolve(savedSession)
	fmt.Fprintf(a.Stdout, "Conversation mode: %s (%s)\n", view.Mode, view.Reason)
	return 0
}

func (a *App) commandPair(ctx context.Context, args []string) int {
	set := newFlagSet("pair", a.Stderr)
	root := workspaceFlags(set)
	port := set.Int("port", 0, "preferred port")
	noTunnel := set.Bool("no-tunnel", false, "create a code without starting a public tunnel")
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, ensured, err := ensureBridge(ctx, *root, *port)
	if err != nil {
		return a.fail(err)
	}
	state := ensured.State
	if !*noTunnel {
		if _, err := ensureTunnel(ctx, &state); err != nil {
			return a.fail(err)
		}
	}
	pairing, err := createPairing(ctx, state)
	if err != nil {
		return a.fail(err)
	}
	mcpURL := localURL(state.Port) + "/mcp"
	if state.PublicURL != "" {
		mcpURL = config.MCPURL(state.PublicURL)
	}
	payload := map[string]any{"workspaceId": ws.ID, "workspaceName": ws.Name, "mcpUrl": mcpURL, "pairingCode": pairing.Code, "expiresAt": pairing.ExpiresAt}
	if *jsonOutput {
		return a.emitJSON(payload)
	}
	fmt.Fprintf(a.Stdout, "MCP URL: %s\nPairing code: %s\n", mcpURL, pairing.Code)
	return 0
}

func (a *App) commandUnpair(ctx context.Context, args []string) int {
	set := newFlagSet("unpair", a.Stderr)
	root := workspaceFlags(set)
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, err := workspace.New(*root)
	if err != nil {
		return a.fail(err)
	}
	count := 0
	live, _ := stateruntime.FindLive(ctx, ws.ID)
	if live != nil {
		var reply struct {
			Revoked int `json:"revoked"`
		}
		if err := stateruntime.AdminRequest(ctx, *live, http.MethodPost, "/admin/revoke-all", nil, &reply); err != nil {
			return a.fail(err)
		}
		count = reply.Revoked
	} else {
		store, err := auth.NewStore(ws.ID, auth.StoreOptions{})
		if err != nil {
			return a.fail(err)
		}
		count, err = store.RevokeAll()
		if err != nil {
			return a.fail(err)
		}
	}
	if *jsonOutput {
		return a.emitJSON(map[string]any{"workspaceId": ws.ID, "revoked": count})
	}
	fmt.Fprintf(a.Stdout, "Revoked %d token record(s) for %s.\n", count, ws.Name)
	return 0
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fixed  bool   `json:"fixed,omitempty"`
}

func (a *App) commandDoctor(ctx context.Context, args []string) int {
	set := newFlagSet("doctor", a.Stderr)
	root := workspaceFlags(set)
	noFix := set.Bool("no-fix", false, "do not make safe configuration fixes")
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	checks := make([]doctorCheck, 0)
	ws, err := workspace.New(*root)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "workspace", Status: "error", Detail: err.Error()})
		if *jsonOutput {
			return a.emitJSON(map[string]any{"ok": false, "checks": checks})
		}
		return a.printDoctor(checks)
	}
	checks = append(checks, doctorCheck{Name: "workspace", Status: "ok", Detail: ws.Root})
	repositories, repositoryErr := ws.Repositories()
	if repositoryErr != nil {
		checks = append(checks, doctorCheck{Name: "repositories", Status: "error", Detail: repositoryErr.Error()})
	} else {
		mode, _ := ws.TopologyMode()
		checks = append(checks, doctorCheck{Name: "repositories", Status: "ok", Detail: fmt.Sprintf("%s (%d)", mode, len(repositories))})
	}
	stateDir := config.StateDir()
	if _, err := config.EnsureDir(stateDir); err != nil {
		checks = append(checks, doctorCheck{Name: "state-directory", Status: "error", Detail: err.Error()})
	} else {
		probe := filepath.Join(stateDir, ".doctor-write")
		err := config.WriteSecureFile(probe, []byte("ok\n"))
		_ = os.Remove(probe)
		if err != nil {
			checks = append(checks, doctorCheck{Name: "state-directory", Status: "error", Detail: err.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: "state-directory", Status: "ok", Detail: stateDir})
		}
	}
	if git, err := exec.LookPath("git"); err != nil {
		checks = append(checks, doctorCheck{Name: "git", Status: "error", Detail: "git executable not found"})
	} else {
		checks = append(checks, doctorCheck{Name: "git", Status: "ok", Detail: git})
	}
	tunnelState, _ := tunnel.ReadState(ws.ID)
	cloudflared := tunnel.FindBinary("cloudflared")
	if cloudflared == "" {
		status := "warning"
		if tunnelState.Preference == tunnel.PreferenceQuick || tunnelState.Preference == tunnel.PreferenceNamed {
			status = "error"
		}
		checks = append(checks, doctorCheck{Name: "cloudflared", Status: status, Detail: "not installed; public connectivity is unavailable"})
	} else {
		checks = append(checks, doctorCheck{Name: "cloudflared", Status: "ok", Detail: cloudflared})
	}
	live, _ := stateruntime.FindLive(ctx, ws.ID)
	if live == nil {
		checks = append(checks, doctorCheck{Name: "bridge", Status: "warning", Detail: "not running"})
	} else {
		checks = append(checks, doctorCheck{Name: "bridge", Status: "ok", Detail: fmt.Sprintf("pid %d, port %d", live.PID, live.Port)})
	}
	skillPath := config.CodexLinkSkillPath()
	skillContent, skillReadErr := os.ReadFile(skillPath)
	if skillReadErr == nil && bytes.Equal(skillContent, config.CodexLinkSkillContent()) {
		checks = append(checks, doctorCheck{Name: "codex-skill", Status: "ok", Detail: skillPath})
	} else if *noFix {
		checks = append(checks, doctorCheck{Name: "codex-skill", Status: "warning", Detail: "not installed or out of date"})
	} else {
		result, fixErr := config.EnsureCodexLinkSkill()
		if fixErr != nil {
			checks = append(checks, doctorCheck{Name: "codex-skill", Status: "error", Detail: fixErr.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: "codex-skill", Status: "ok", Detail: result.Path, Fixed: result.Installed || result.Updated})
		}
	}
	configPath := config.CodexConfigPath()
	content, _ := os.ReadFile(configPath)
	if config.IsCodexStateAllowed(string(content), stateDir) {
		checks = append(checks, doctorCheck{Name: "codex-sandbox", Status: "ok", Detail: "state directory is writable from Codex"})
	} else if *noFix {
		checks = append(checks, doctorCheck{Name: "codex-sandbox", Status: "warning", Detail: "state directory is not in writable_roots"})
	} else {
		result, fixErr := config.EnsureCodexSandboxAllowlist(configPath, stateDir)
		if fixErr != nil {
			checks = append(checks, doctorCheck{Name: "codex-sandbox", Status: "error", Detail: fixErr.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: "codex-sandbox", Status: "ok", Detail: result.ConfigPath, Fixed: result.Added})
		}
	}
	ok := true
	for _, check := range checks {
		if check.Status == "error" {
			ok = false
		}
	}
	if *jsonOutput {
		return a.emitJSON(map[string]any{"ok": ok, "workspaceId": ws.ID, "checks": checks})
	}
	code := a.printDoctor(checks)
	if !ok {
		return 1
	}
	return code
}

func (a *App) printDoctor(checks []doctorCheck) int {
	for _, check := range checks {
		mark := "✓"
		if check.Status == "warning" {
			mark = "!"
		}
		if check.Status == "error" {
			mark = "✗"
		}
		fixed := ""
		if check.Fixed {
			fixed = " (fixed)"
		}
		fmt.Fprintf(a.Stdout, "%s %-18s %s%s\n", mark, check.Name, check.Detail, fixed)
	}
	return 0
}

func (a *App) commandLogs(args []string) int {
	set := newFlagSet("logs", a.Stderr)
	lines := set.Int("n", 100, "number of recent lines")
	verbose := set.Bool("verbose", false, "include daemon stdout logs")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	if *lines < 1 {
		*lines = 1
	}
	if *lines > 5000 {
		*lines = 5000
	}
	logDir := filepath.Join(config.StateDir(), "logs")
	patterns := []string{filepath.Join(logDir, "bridge.log")}
	if *verbose {
		patterns = append(patterns, filepath.Join(logDir, "bridge-*.out.log"))
	}
	files := make([]string, 0)
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		files = append(files, matches...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintln(a.Stdout, "No bridge logs found.")
		return 0
	}
	for _, path := range files {
		content, err := tailFile(path, *lines)
		if err != nil {
			return a.fail(err)
		}
		if len(files) > 1 {
			fmt.Fprintf(a.Stdout, "==> %s <==\n", path)
		}
		fmt.Fprint(a.Stdout, content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Fprintln(a.Stdout)
		}
	}
	return 0
}

func tailFile(path string, count int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	ring := make([]string, count)
	total := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		ring[total%count] = scanner.Text()
		total++
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	size := total
	if size > count {
		size = count
	}
	start := total - size
	var builder strings.Builder
	for i := 0; i < size; i++ {
		builder.WriteString(ring[(start+i)%count])
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}

func (a *App) commandWorkspace(ctx context.Context, args []string) int {
	set := newFlagSet("workspace", a.Stderr)
	root := workspaceFlags(set)
	jsonOutput := set.Bool("json", false, "print JSON")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	ws, err := workspace.New(*root)
	if err != nil {
		return a.fail(err)
	}
	topology, err := ws.InspectTopology(ctx)
	if err != nil {
		return a.fail(err)
	}
	payload := map[string]any{
		"workspaceId": ws.ID, "name": ws.Name, "root": ws.Root,
		"mode": topology.Mode, "repositoryCount": topology.RepositoryCount,
		"defaultRepository": topology.DefaultRepository, "repositories": topology.Repositories, "relations": topology.Relations,
		"project":       ws.DetectProject(),
		"maxIterations": ws.ProjectConfig.MaxIterations, "chatgptProfile": ws.ProjectConfig.ChatGPTProfile,
	}
	if *jsonOutput {
		return a.emitJSON(payload)
	}
	fmt.Fprintf(a.Stdout, "Workspace: %s\nRoot: %s\nID: %s\nMode: %s; repositories: %d\n", ws.Name, ws.Root, ws.ID, topology.Mode, topology.RepositoryCount)
	for _, repository := range topology.Repositories {
		branch := "detached"
		if repository.Git.Branch != nil {
			branch = *repository.Git.Branch
		}
		fmt.Fprintf(a.Stdout, "- %s (%s, %s)\n", repository.Path, repository.Project.ProjectType, branch)
	}
	fmt.Fprintf(a.Stdout, "ChatGPT profile: %s; max iterations: %d\n", ws.ProjectConfig.ChatGPTProfile, ws.ProjectConfig.MaxIterations)
	return 0
}

func (a *App) commandInstall(args []string) int {
	set := newFlagSet("install", a.Stderr)
	jsonOutput := set.Bool("json", false, "print JSON")
	configPath := set.String("config", "", "Codex config.toml path")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	skill, skillErr := config.EnsureCodexLinkSkill()
	sandbox, sandboxErr := config.EnsureCodexSandboxAllowlist(*configPath, config.StateDir())
	payload := map[string]any{"skill": skill, "sandbox": sandbox, "ok": skillErr == nil && sandboxErr == nil}
	if skillErr != nil {
		payload["skillError"] = skillErr.Error()
	}
	if sandboxErr != nil {
		payload["sandboxError"] = sandboxErr.Error()
	}
	if *jsonOutput {
		code := a.emitJSON(payload)
		if code != 0 {
			return code
		}
		if skillErr != nil || sandboxErr != nil {
			return 1
		}
		return 0
	}
	if skillErr == nil {
		fmt.Fprintf(a.Stdout, "✓ Codex skill      %s\n", skill.Path)
	} else {
		fmt.Fprintf(a.Stderr, "✗ Codex skill      %v\n", skillErr)
	}
	if sandboxErr == nil {
		fmt.Fprintf(a.Stdout, "✓ Codex sandbox    %s\n", sandbox.ConfigPath)
	} else {
		fmt.Fprintf(a.Stderr, "✗ Codex sandbox    %v\n", sandboxErr)
	}
	if skillErr != nil || sandboxErr != nil {
		return 1
	}
	return 0
}

func (a *App) commandSandboxAllow(args []string) int {
	set := newFlagSet("sandbox-allow", a.Stderr)
	jsonOutput := set.Bool("json", false, "print JSON")
	configPath := set.String("config", "", "Codex config.toml path")
	if ok, code := a.parseFlags(set, args); !ok {
		return code
	}
	result, err := config.EnsureCodexSandboxAllowlist(*configPath, config.StateDir())
	if err != nil {
		return a.fail(err)
	}
	if *jsonOutput {
		return a.emitJSON(result)
	}
	if result.Added {
		fmt.Fprintf(a.Stdout, "Added %s to writable_roots in %s.\n", result.StateDir, result.ConfigPath)
	} else {
		fmt.Fprintf(a.Stdout, "%s is already allowed in %s.\n", result.StateDir, result.ConfigPath)
	}
	return 0
}
