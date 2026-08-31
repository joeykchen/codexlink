package setup

import (
	"context"
	"net/http"
	"testing"

	"github.com/joeykchen/codexlink/internal/config"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
)

func TestServiceRunsOneCommandWorkflow(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	root := t.TempDir()
	service := New()
	service.ensure = func(_ context.Context, workspaceRoot, workspaceID string, options stateruntime.EnsureOptions) (stateruntime.EnsureResult, error) {
		if workspaceRoot != root || workspaceID == "" || options.Port != 49000 {
			t.Fatalf("unexpected ensure args: %q %q %+v", workspaceRoot, workspaceID, options)
		}
		return stateruntime.EnsureResult{State: stateruntime.State{WorkspaceID: workspaceID, WorkspaceRoot: workspaceRoot, Port: 49000, AdminToken: "test"}, Spawned: true}, nil
	}
	var setupRequest setupSessionRequest
	service.admin = func(_ context.Context, state stateruntime.State, method, route string, body, target any) error {
		if state.Port != 49000 {
			t.Fatalf("unexpected admin call: %s %s %+v", method, route, state)
		}
		switch route {
		case "/admin/info":
			if method != http.MethodGet {
				t.Fatalf("info method = %s", method)
			}
			*target.(*adminInfo) = adminInfo{}
		case "/admin/tunnel/start":
			if method != http.MethodPost {
				t.Fatalf("tunnel method = %s", method)
			}
			target.(*struct {
				URL string `json:"url"`
			}).URL = "https://public.example"
		case "/admin/setup-session":
			if method != http.MethodPost {
				t.Fatalf("setup method = %s", method)
			}
			setupRequest = body.(setupSessionRequest)
			reply := target.(*setupSessionReply)
			reply.Code = "ABCD-EFGH"
			reply.ExpiresAt = 123456789
			reply.SetupURL = "http://127.0.0.1:49000/setup/nonce"
		default:
			t.Fatalf("unexpected admin route %s", route)
		}
		return nil
	}

	result, err := service.Run(context.Background(), Options{WorkspaceRoot: root, Port: 49000, SkipCodex: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.MCPURL != "https://public.example/mcp" || result.ConnectorAction != "create" || result.PairingCode != "ABCD-EFGH" || !result.Spawned {
		t.Fatalf("unexpected result: %+v", result)
	}
	if setupRequest.MCPURL != result.MCPURL || setupRequest.ConnectorName != result.ConnectorName || setupRequest.ConnectorAction != "create" {
		t.Fatalf("unexpected setup request: %+v", setupRequest)
	}
	endpoint, err := config.ReadEndpoint(result.WorkspaceID)
	if err != nil || endpoint == nil || endpoint.MCPURL == nil || *endpoint.MCPURL != result.MCPURL {
		t.Fatalf("endpoint = %+v, %v", endpoint, err)
	}
}

func TestServiceLocalOnlyReusesEndpoint(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	root := t.TempDir()
	service := New()
	service.ensure = func(_ context.Context, workspaceRoot, workspaceID string, _ stateruntime.EnsureOptions) (stateruntime.EnsureResult, error) {
		return stateruntime.EnsureResult{State: stateruntime.State{WorkspaceID: workspaceID, WorkspaceRoot: workspaceRoot, Port: 48765}}, nil
	}
	service.admin = func(_ context.Context, _ stateruntime.State, method, route string, body, target any) error {
		if route == "/admin/info" {
			if method != http.MethodGet {
				t.Fatalf("info method = %s", method)
			}
			*target.(*adminInfo) = adminInfo{}
			return nil
		}
		if route != "/admin/setup-session" || method != http.MethodPost {
			t.Fatalf("local-only setup called %s %s", method, route)
		}
		request := body.(setupSessionRequest)
		reply := target.(*setupSessionReply)
		reply.Code, reply.ExpiresAt, reply.SetupURL = "AAAA-BBBB", 99, "http://127.0.0.1:48765/setup/x"
		if request.ConnectorAction != "create" && request.ConnectorAction != "none" {
			t.Fatalf("action = %q", request.ConnectorAction)
		}
		return nil
	}
	first, err := service.Run(context.Background(), Options{WorkspaceRoot: root, LocalOnly: true, SkipCodex: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Run(context.Background(), Options{WorkspaceRoot: root, LocalOnly: true, SkipCodex: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.ConnectorAction != "create" || second.ConnectorAction != "none" || second.MCPURL != "http://127.0.0.1:48765/mcp" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestServiceReusesHealthyAuthorizedConnectionWithoutNewPairing(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	root := t.TempDir()
	service := New()
	service.ensure = func(_ context.Context, workspaceRoot, workspaceID string, _ stateruntime.EnsureOptions) (stateruntime.EnsureResult, error) {
		return stateruntime.EnsureResult{State: stateruntime.State{
			WorkspaceID: workspaceID, WorkspaceRoot: workspaceRoot, Port: 48765,
			PublicURL: "https://stable.example",
		}}, nil
	}
	setupCalls := 0
	revokeCalls := 0
	service.admin = func(_ context.Context, _ stateruntime.State, method, route string, _ any, target any) error {
		switch route {
		case "/admin/info":
			if method != http.MethodGet {
				t.Fatalf("info method = %s", method)
			}
			info := target.(*adminInfo)
			info.PublicURL = "https://stable.example"
			info.TokenCount = 2
			info.Tunnel.Running = true
			info.Tunnel.URL = "https://stable.example"
		case "/admin/revoke-all":
			if method != http.MethodPost {
				t.Fatalf("revoke method = %s", method)
			}
			revokeCalls++
		case "/admin/setup-session":
			setupCalls++
			reply := target.(*setupSessionReply)
			reply.Code = "ABCD-EFGH"
			reply.ExpiresAt = 123456789
			reply.SetupURL = "http://127.0.0.1:48765/setup/forced"
		default:
			t.Fatalf("unexpected route %s", route)
		}
		return nil
	}
	first, err := service.Run(context.Background(), Options{WorkspaceRoot: root, SkipCodex: true, Reconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.SetupURL == "" {
		t.Fatal("forced reconnect should create a setup session")
	}
	// Simulate completion by persisting the endpoint; the admin info already
	// reports active tokens, so the normal idempotent run should be ready.
	second, err := service.Run(context.Background(), Options{WorkspaceRoot: root, SkipCodex: true})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Authorized || second.AuthorizationRequired || second.SetupURL != "" || setupCalls != 1 || revokeCalls != 1 {
		t.Fatalf("second=%+v setupCalls=%d revokeCalls=%d", second, setupCalls, revokeCalls)
	}
}
