package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/control"
	"github.com/joeykchen/codexlink/internal/logging"
	"github.com/joeykchen/codexlink/internal/workspace"
)

func TestWorkspaceToolDefinitionsMatchGolden(t *testing.T) {
	expectedData, err := os.ReadFile("testdata/tool_definitions.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err := json.Unmarshal(expectedData, &expected); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.New(t.TempDir())
	controls, _ := control.NewStore(t.TempDir(), ws.ID, ws.Root)
	registry, _ := WorkspaceTools(ws, controls)
	actualDefinitions := registry.List(nil)
	actualData, _ := json.Marshal(actualDefinitions)
	var actual any
	if err := json.Unmarshal(actualData, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		encoded, _ := json.MarshalIndent(actualDefinitions, "", "  ")
		t.Fatalf("tool definitions changed:\n%s", encoded)
	}
}

func TestToolConstructorsOwnSecurityMetadata(t *testing.T) {
	ws, _ := workspace.New(t.TempDir())
	registry, _ := WorkspaceTools(ws)
	first, _ := registry.Get("workspace_info")
	second, _ := registry.Get("read_file")
	first.Definition.Annotations["readOnlyHint"] = false
	if second.Definition.Annotations["readOnlyHint"] != true {
		t.Fatal("tool annotation maps must not be shared")
	}
	for _, name := range []string{"workspace_info", "list_directory", "read_file", "search_workspace", "find_files", "read_files", "file_outline", "git_status", "git_diff", "git_log"} {
		tool, _ := registry.Get(name)
		if count := strings.Count(tool.Definition.Description, untrustedWorkspaceDataNotice); count != 1 {
			t.Fatalf("%s notice count = %d", name, count)
		}
	}
	for _, name := range []string{"test_status", "execution_summary"} {
		tool, _ := registry.Get(name)
		if strings.Contains(tool.Definition.Description, untrustedWorkspaceDataNotice) {
			t.Fatalf("execution tool %s gained workspace notice", name)
		}
	}
}

func TestWorkspaceDataToolOwnsErrorAdaptation(t *testing.T) {
	tests := []struct {
		name        string
		handlerErr  error
		wantCode    string
		wantMessage string
	}{
		{name: "tool error", handlerErr: NewToolError("INVALID_ARGUMENT", "invalid value"), wantCode: "INVALID_ARGUMENT", wantMessage: "invalid value"},
		{name: "workspace error", handlerErr: workspace.NewError(workspace.ErrFileNotFound, "missing file"), wantCode: string(workspace.ErrFileNotFound), wantMessage: "missing file"},
		{name: "ordinary error", handlerErr: errors.New("failure"), wantCode: "OPERATION_FAILED", wantMessage: "failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := newWorkspaceDataTool(ToolDefinition{Name: "test", Description: "description", InputSchema: objectSchema(map[string]any{})}, scopeWorkspaceRead, func(context.Context, noArgs) (any, error) {
				return nil, test.handlerErr
			})
			_, err := tool.Handler(context.Background(), map[string]any{})
			toolErr, ok := err.(*ToolError)
			if !ok || toolErr.Code != test.wantCode || toolErr.Message != test.wantMessage {
				t.Fatalf("error = %#v, want %s/%q", err, test.wantCode, test.wantMessage)
			}
		})
	}
}

func TestWorkspaceToolsExposeReadOnlyCapabilityGroups(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.New(root)
	registry, err := WorkspaceTools(ws)
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.List(nil)
	if len(definitions) != 12 {
		t.Fatalf("tool count = %d", len(definitions))
	}
	expectedScopes := map[string]string{
		"workspace_info": "workspace.read", "list_directory": "workspace.read", "read_file": "workspace.read", "search_workspace": "workspace.search",
		"find_files": "workspace.search", "read_files": "workspace.read", "file_outline": "workspace.read",
		"git_status": "git.read", "git_diff": "git.read", "git_log": "git.read",
		"test_status": "execution.read", "execution_summary": "execution.read",
	}
	for name, expected := range expectedScopes {
		tool, ok := registry.Get(name)
		if !ok || tool.Scope != expected {
			t.Fatalf("scope for %s = %q, present=%v", name, tool.Scope, ok)
		}
		annotations := tool.Definition.Annotations
		if annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false || annotations["idempotentHint"] != true || annotations["openWorldHint"] != false {
			t.Fatalf("annotations for %s = %#v", name, annotations)
		}
	}
	for _, name := range []string{"find_files", "read_files", "file_outline", "git_log"} {
		_, ok := registry.Get(name)
		if !ok {
			t.Fatalf("missing tool %s", name)
		}
	}
	for _, check := range []struct{ tool, property string }{{"list_directory", "path"}, {"read_file", "path"}, {"git_status", "repository"}, {"git_diff", "repository"}} {
		definition, _ := registry.Get(check.tool)
		property := schemaProperty(t, definition.Definition.InputSchema, check.property)
		if property["description"] == "" {
			t.Fatalf("legacy schema description missing for %s.%s", check.tool, check.property)
		}
	}
	assertSchemaBound(t, registry, "find_files", "max_depth", "maximum", workspace.FindFilesMaxDepth)
	assertSchemaBound(t, registry, "find_files", "max_depth", "default", workspace.FindFilesDefaultDepth)
	assertSchemaBound(t, registry, "find_files", "offset", "maximum", workspace.FindFilesMaxOffset)
	assertSchemaBound(t, registry, "read_files", "max_bytes", "maximum", workspace.ReadFilesMaxBytes)
	assertSchemaBound(t, registry, "read_files", "max_bytes", "default", workspace.ReadFilesDefaultBytes)
	assertSchemaBound(t, registry, "file_outline", "limit", "maximum", workspace.FileOutlineMaxLimit)
	assertSchemaBound(t, registry, "git_log", "offset", "maximum", workspace.GitLogMaxOffset)
	for toolName, required := range map[string]string{"find_files": "glob", "read_files": "files", "file_outline": "path"} {
		tool, _ := registry.Get(toolName)
		if !schemaRequires(tool.Definition.InputSchema, required) {
			t.Fatalf("%s does not require %s", toolName, required)
		}
	}
	dispatcher := NewDispatcher(registry, logging.Null())
	for index, call := range []struct {
		name  string
		scope string
		args  map[string]any
	}{
		{"find_files", "workspace.search", map[string]any{"glob": "**/*.go"}},
		{"read_files", "workspace.read", map[string]any{"files": []map[string]any{{"path": "main.go"}}}},
		{"file_outline", "workspace.read", map[string]any{"path": "main.go"}},
		{"git_log", "git.read", map[string]any{}},
	} {
		allowed := auth.Principal{Scopes: []string{call.scope}}
		if !dispatcherListsTool(t, dispatcher, allowed, call.name) {
			t.Fatalf("%s missing with scope %s", call.name, call.scope)
		}
		called := dispatcher.Dispatch(context.Background(), allowed, rpcRequest(string(rune('1'+index)), "tools/call", map[string]any{"name": call.name, "arguments": call.args}), "2025-06-18")
		if called.Response.Result.(map[string]any)["isError"] != false {
			t.Fatalf("%s call = %#v", call.name, called.Response.Result)
		}
		if dispatcherListsTool(t, dispatcher, auth.Principal{}, call.name) {
			t.Fatalf("%s visible without %s", call.name, call.scope)
		}
		denied := dispatcher.Dispatch(context.Background(), auth.Principal{}, rpcRequest(string(rune('5'+index)), "tools/call", map[string]any{"name": call.name, "arguments": call.args}), "2025-06-18")
		result := denied.Response.Result.(map[string]any)
		errorPayload := result["structuredContent"].(map[string]any)["error"].(map[string]any)
		if result["isError"] != true || errorPayload["code"] != "INSUFFICIENT_SCOPE" {
			t.Fatalf("%s scope denial = %#v", call.name, result)
		}
	}
}

func dispatcherListsTool(t *testing.T, dispatcher *Dispatcher, principal auth.Principal, name string) bool {
	t.Helper()
	listed := dispatcher.Dispatch(context.Background(), principal, rpcRequest("10", "tools/list", nil), "2025-06-18")
	for _, definition := range listed.Response.Result.(map[string]any)["tools"].([]ToolDefinition) {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func schemaProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema)
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema property %s = %#v", name, properties[name])
	}
	return property
}

func assertSchemaBound(t *testing.T, registry *Registry, toolName, propertyName, key string, expected any) {
	t.Helper()
	tool, _ := registry.Get(toolName)
	property := schemaProperty(t, tool.Definition.InputSchema, propertyName)
	if property[key] != expected {
		t.Fatalf("%s.%s %s = %#v, want %#v", toolName, propertyName, key, property[key], expected)
	}
}

func schemaRequires(schema map[string]any, name string) bool {
	required, _ := schema["required"].([]string)
	for _, candidate := range required {
		if candidate == name {
			return true
		}
	}
	return false
}
