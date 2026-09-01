package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/control"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
)

func TestControlAdminScopeAndLifecycle(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	server, err := Start(Options{WorkspaceRoot: t.TempDir(), Port: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	runtimeState := stateruntime.State{Port: server.Port, AdminToken: server.AdminToken}
	prepare := map[string]any{"taskId": "cl_deadbeef", "iteration": 0, "ttlSeconds": 60}
	var request control.Request
	if err := stateruntime.AdminRequest(context.Background(), runtimeState, http.MethodPost, "/admin/control/prepare", prepare, &request); err == nil {
		t.Fatal("prepare succeeded without control scope")
	}
	client, _ := server.AuthStore.RegisterClient("control", []string{"http://127.0.0.1/callback"})
	verifier := strings.Repeat("v", 64)
	audience := server.LocalBaseURL() + "/mcp"
	code, _ := server.AuthStore.CreateAuthorizationCode(auth.AuthorizationCodeRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: auth.PKCEChallenge(verifier), Scopes: []string{auth.ScopeControlRespond}, PairingID: "p", Audience: audience})
	tokens, err := server.AuthStore.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateruntime.AdminRequest(context.Background(), runtimeState, http.MethodPost, "/admin/control/prepare", prepare, &request); err != nil {
		t.Fatal(err)
	}
	if request.Status != "pending" {
		t.Fatalf("request=%+v", request)
	}
	var longRequest control.Request
	if err := stateruntime.AdminRequest(context.Background(), runtimeState, http.MethodPost, "/admin/control/prepare", map[string]any{"taskId": "cl_feedface", "iteration": 0, "ttlSeconds": 14400}, &longRequest); err != nil {
		t.Fatalf("four-hour prepare failed: %v", err)
	}
	if lifetime := longRequest.ExpiresAt.Sub(longRequest.CreatedAt); lifetime != 4*time.Hour {
		t.Fatalf("long request lifetime=%v", lifetime)
	}
	if err := stateruntime.AdminRequest(context.Background(), runtimeState, http.MethodPost, "/admin/control/prepare", map[string]any{"taskId": "cl_cafebabe", "iteration": 0, "ttlSeconds": 14401}, &control.Request{}); err == nil {
		t.Fatal("prepare above four hours succeeded")
	}
	rpc, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "submit_control_response", "arguments": map[string]any{"request_id": request.RequestID, "task_id": request.TaskID, "iteration": 0, "state": "DONE", "summary": "done"}}})
	httpRequest, _ := http.NewRequest(http.MethodPost, audience, bytes.NewReader(rpc))
	httpRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := loopbackTestClient(t).Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var rpcResult map[string]any
	if err := json.NewDecoder(response.Body).Decode(&rpcResult); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || rpcResult["error"] != nil {
		t.Fatalf("MCP submit status=%d result=%v", response.StatusCode, rpcResult)
	}
	var received control.Request
	if err := stateruntime.AdminRequest(context.Background(), runtimeState, http.MethodPost, "/admin/control/wait", map[string]any{"requestId": request.RequestID, "taskId": request.TaskID, "iteration": 0, "waitMs": int((10 * time.Millisecond).Milliseconds())}, &received); err != nil {
		t.Fatal(err)
	}
	if received.Response == nil || received.Response.State != control.StateDone {
		t.Fatalf("received=%+v", received)
	}
}
