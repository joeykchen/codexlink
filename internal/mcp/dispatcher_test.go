package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/logging"
	"github.com/joeykchen/codexlink/internal/workspace"
)

func rpcRequest(id string, method string, params any) RPCRequest {
	var raw json.RawMessage
	if params != nil {
		encoded, _ := json.Marshal(params)
		raw = encoded
	}
	return RPCRequest{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw}
}

func TestDispatcherScopesAndModernResultShape(t *testing.T) {
	registry, err := NewRegistry(
		Tool{Definition: ToolDefinition{Name: "read", Description: "read", InputSchema: map[string]any{"type": "object"}}, Scope: "workspace.read", Handler: func(context.Context, map[string]any) (any, error) { return map[string]any{"ok": true}, nil }},
		Tool{Definition: ToolDefinition{Name: "git", Description: "git", InputSchema: map[string]any{"type": "object"}}, Scope: "git.read", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(registry, logging.Null())
	principal := auth.Principal{Scopes: []string{"workspace.read"}}
	listed := dispatcher.Dispatch(context.Background(), principal, rpcRequest("1", "tools/list", nil), "2026-07-28")
	payload, ok := listed.Response.Result.(map[string]any)
	if !ok || payload["resultType"] != "complete" || payload["cacheScope"] != "private" {
		t.Fatalf("unexpected modern payload: %#v", listed.Response.Result)
	}
	tools := payload["tools"].([]ToolDefinition)
	if len(tools) != 1 || tools[0].Name != "read" {
		t.Fatalf("scope filtering failed: %#v", tools)
	}
	called := dispatcher.Dispatch(context.Background(), principal, rpcRequest("2", "tools/call", map[string]any{"name": "read", "arguments": map[string]any{}}), "2026-07-28")
	result := called.Response.Result.(map[string]any)
	if result["isError"] != false || result["resultType"] != "complete" {
		t.Fatalf("unexpected call result: %#v", result)
	}
	denied := dispatcher.Dispatch(context.Background(), principal, rpcRequest("3", "tools/call", map[string]any{"name": "git", "arguments": map[string]any{}}), "2025-06-18")
	if denied.Response.Result.(map[string]any)["isError"] != true {
		t.Fatalf("expected tool-level scope error: %#v", denied.Response)
	}
}

func TestWorkspaceToolsRejectUnknownArgumentsAndSensitiveReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "safe.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := WorkspaceTools(ws)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(registry, logging.Null())
	principal := auth.Principal{Scopes: []string{"workspace.read"}}
	unknown := dispatcher.Dispatch(context.Background(), principal, rpcRequest("1", "tools/call", map[string]any{
		"name": "read_file", "arguments": map[string]any{"path": "safe.txt", "surprise": true},
	}), "2025-06-18")
	unknownResult := unknown.Response.Result.(map[string]any)
	unknownStructured := unknownResult["structuredContent"].(map[string]any)
	unknownError := unknownStructured["error"].(map[string]any)
	if unknownResult["isError"] != true || unknownError["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("unknown argument should fail: %#v", unknownResult)
	}
	sensitive := dispatcher.Dispatch(context.Background(), principal, rpcRequest("2", "tools/call", map[string]any{
		"name": "read_file", "arguments": map[string]any{"path": ".env"},
	}), "2025-06-18")
	sensitiveError := sensitive.Response.Result.(map[string]any)["structuredContent"].(map[string]any)["error"].(map[string]any)
	if sensitiveError["code"] != string(workspace.ErrSensitiveFile) {
		t.Fatalf("sensitive file error: %#v", sensitiveError)
	}
}

func TestDispatcherNotificationsHaveNoResponse(t *testing.T) {
	registry, _ := NewRegistry(Tool{Definition: ToolDefinition{Name: "x", Description: "x", InputSchema: map[string]any{}}, Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	result := NewDispatcher(registry, logging.Null()).Dispatch(context.Background(), auth.Principal{}, RPCRequest{JSONRPC: "2.0", Method: "notifications/initialized"}, "2025-06-18")
	if !result.Notification {
		t.Fatal("notification should not produce a response")
	}
}

func TestWorkspaceGitToolsRequireRepositoryInGroup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "init", "-q")
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, output)
		}
	}
	ws, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := WorkspaceTools(ws)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(registry, logging.Null())
	principal := auth.Principal{Scopes: []string{"workspace.read", "git.read"}}

	info := dispatcher.Dispatch(context.Background(), principal, rpcRequest("1", "tools/call", map[string]any{
		"name": "workspace_info", "arguments": map[string]any{},
	}), "2025-06-18")
	infoStructured := info.Response.Result.(map[string]any)["structuredContent"].(map[string]any)
	if infoStructured["mode"] != workspace.TopologyGroup || infoStructured["repositoryCount"] != 2 {
		t.Fatalf("workspace info = %#v", infoStructured)
	}

	missing := dispatcher.Dispatch(context.Background(), principal, rpcRequest("2", "tools/call", map[string]any{
		"name": "git_status", "arguments": map[string]any{},
	}), "2025-06-18")
	missingResult := missing.Response.Result.(map[string]any)
	missingError := missingResult["structuredContent"].(map[string]any)["error"].(map[string]any)
	if missingResult["isError"] != true || missingError["code"] != string(workspace.ErrRepositoryNeeded) {
		t.Fatalf("missing repository = %#v", missingResult)
	}

	selected := dispatcher.Dispatch(context.Background(), principal, rpcRequest("3", "tools/call", map[string]any{
		"name": "git_status", "arguments": map[string]any{"repository": "web"},
	}), "2025-06-18")
	selectedStructured := selected.Response.Result.(map[string]any)["structuredContent"].(workspace.GitStatus)
	if selectedStructured.Repository != "web" || !selectedStructured.IsRepo {
		t.Fatalf("selected repository = %#v", selectedStructured)
	}
}

func TestDispatcherModernDiscoveryAndRemovedMethods(t *testing.T) {
	registry, err := NewRegistry(
		Tool{
			Definition: ToolDefinition{Name: "read", Description: "read", InputSchema: map[string]any{"type": "object"}},
			Scope:      "workspace.read",
			Handler:    func(context.Context, map[string]any) (any, error) { return map[string]any{"ok": true}, nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(registry, logging.Null())
	principal := auth.Principal{Scopes: []string{"workspace.read"}}

	discovered := dispatcher.Dispatch(context.Background(), principal, rpcRequest("1", "server/discover", map[string]any{}), "2026-07-28")
	if discovered.Response.Error != nil {
		t.Fatalf("server/discover error: %#v", discovered.Response.Error)
	}
	payload, ok := discovered.Response.Result.(map[string]any)
	if !ok {
		t.Fatalf("server/discover result type: %#v", discovered.Response.Result)
	}
	if payload["resultType"] != "complete" || payload["ttlMs"] != 60*60*1000 || payload["cacheScope"] != "private" {
		t.Fatalf("server/discover envelope: %#v", payload)
	}
	versions, ok := payload["supportedVersions"].([]string)
	if !ok || len(versions) == 0 || versions[0] != "2026-07-28" {
		t.Fatalf("supported versions: %#v", payload["supportedVersions"])
	}
	metadata, ok := payload["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("missing server metadata: %#v", payload["_meta"])
	}
	serverInfo, ok := metadata[serverInfoMetaKey].(map[string]any)
	if !ok || serverInfo["name"] != "codexlink-bridge" || serverInfo["version"] != buildinfo.Version {
		t.Fatalf("server info: %#v", metadata)
	}

	for _, method := range []string{"initialize", "ping"} {
		result := dispatcher.Dispatch(context.Background(), principal, rpcRequest("2", method, map[string]any{}), "2026-07-28")
		if result.Response.Error == nil || result.Response.Error.Code != -32601 {
			t.Fatalf("%s should be removed in modern protocol: %#v", method, result.Response)
		}
	}

	legacy := dispatcher.Dispatch(context.Background(), principal, rpcRequest("3", "server/discover", map[string]any{}), "2025-11-25")
	if legacy.Response.Error == nil || legacy.Response.Error.Code != -32601 {
		t.Fatalf("legacy server/discover should be unknown: %#v", legacy.Response)
	}
}
