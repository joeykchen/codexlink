package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/bridge"
	"github.com/joeykchen/codexlink/internal/control"
)

func TestControlPrepareAndGetJSON(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	root := t.TempDir()
	server, err := bridge.Start(bridge.Options{WorkspaceRoot: root, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, _ := server.AuthStore.RegisterClient("cli", []string{"http://127.0.0.1/callback"})
	verifier := strings.Repeat("x", 64)
	audience := server.LocalBaseURL() + "/mcp"
	code, _ := server.AuthStore.CreateAuthorizationCode(auth.AuthorizationCodeRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: auth.PKCEChallenge(verifier), Scopes: []string{auth.ScopeControlRespond}, PairingID: "p", Audience: audience})
	if _, err := server.AuthStore.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}
	if code := app.commandControl(context.Background(), []string{"prepare", "--workspace", root, "--task-id", "cl_deadbeef", "--iteration", "3", "--ttl", "1m", "--json"}); code != 0 {
		t.Fatalf("prepare code=%d stderr=%s", code, stderr.String())
	}
	var request control.Request
	if err := json.Unmarshal(stdout.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ControlStore.Submit(control.Submission{RequestID: request.RequestID, TaskID: request.TaskID, Iteration: 3, State: control.StateDone, Summary: "complete"}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.commandControl(context.Background(), []string{"get", "--workspace", root, "--request-id", request.RequestID, "--task-id", request.TaskID, "--iteration", "3", "--json"}); code != 0 {
		t.Fatalf("get code=%d stderr=%s", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["received"] != true || result["state"] != "DONE" {
		t.Fatalf("result=%v", result)
	}
	if _, exists := result["plan"]; exists {
		t.Fatalf("DONE response contains plan: %v", result)
	}
}

func TestControlRejectsDurationsAboveFourHours(t *testing.T) {
	for _, args := range [][]string{
		{"prepare", "--task-id", "cl_deadbeef", "--ttl", "4h1s"},
		{"wait", "--request-id", "cr_12345678901234567890123456789012", "--task-id", "cl_deadbeef", "--timeout", "4h1s"},
	} {
		var stdout, stderr bytes.Buffer
		app := &App{Stdout: &stdout, Stderr: &stderr}
		if code := app.commandControl(context.Background(), args); code == 0 {
			t.Fatalf("accepted arguments %v", args)
		}
		if !strings.Contains(stderr.String(), "4h") {
			t.Fatalf("missing four-hour limit for %v: %s", args, stderr.String())
		}
	}
}
