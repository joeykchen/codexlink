package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/control"
	"github.com/joeykchen/codexlink/internal/logging"
)

func TestControlToolScopeAndSubmission(t *testing.T) {
	store, _ := control.NewStore(t.TempDir(), "w", t.TempDir())
	request, _ := store.Prepare("cl_deadbeef", 0, time.Minute)
	registry, _ := NewRegistry(controlToolset(store)...)
	dispatcher := NewDispatcher(registry, logging.Null())
	if dispatcherListsTool(t, dispatcher, auth.Principal{}, "submit_control_response") {
		t.Fatal("tool visible without scope")
	}
	principal := auth.Principal{Scopes: []string{auth.ScopeControlRespond}}
	if !dispatcherListsTool(t, dispatcher, principal, "submit_control_response") {
		t.Fatal("tool missing with scope")
	}
	result := dispatcher.Dispatch(context.Background(), principal, rpcRequest("1", "tools/call", map[string]any{"name": "submit_control_response", "arguments": map[string]any{"request_id": request.RequestID, "task_id": request.TaskID, "iteration": 0, "state": "DONE", "summary": "complete"}}), "2025-06-18")
	if result.Response.Result.(map[string]any)["isError"] != false {
		t.Fatalf("result=%#v", result.Response.Result)
	}
	got, _ := store.Get(request.RequestID, request.TaskID, 0)
	if got.Status != "submitted" {
		t.Fatalf("status=%s", got.Status)
	}
}
