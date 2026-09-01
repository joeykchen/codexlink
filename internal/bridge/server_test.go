package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/runtime"
	"github.com/joeykchen/codexlink/internal/setupui"
	"github.com/joeykchen/codexlink/internal/workspace"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func TestServerHealthAdminAuthAndMCPWiring(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	root := t.TempDir()
	server, err := Start(Options{WorkspaceRoot: root, Port: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	response, err := http.Get(server.LocalBaseURL() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	var health runtime.Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || health.WorkspaceRef != server.PublicRef || health.Status != "ok" {
		t.Fatalf("health: %d %#v", response.StatusCode, health)
	}
	persisted, err := runtime.Read(server.Workspace.ID)
	if err != nil || persisted == nil || persisted.Port != server.Port || persisted.AdminToken != server.AdminToken {
		t.Fatalf("runtime state: %#v err=%v", persisted, err)
	}

	request, _ := http.NewRequest(http.MethodGet, server.LocalBaseURL()+"/admin/info", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthorized admin status = %d", response.StatusCode)
	}
	response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, server.LocalBaseURL()+"/admin/pairing", nil)
	request.Header.Set("Authorization", "Bearer "+server.AdminToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var pair map[string]any
	_ = json.NewDecoder(response.Body).Decode(&pair)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || pair["code"] == "" {
		t.Fatalf("pairing response: %d %#v", response.StatusCode, pair)
	}

	client, err := server.AuthStore.RegisterClient("integration", []string{"http://127.0.0.1/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("q", 64)
	audience := server.LocalBaseURL() + "/mcp"
	code, _ := server.AuthStore.CreateAuthorizationCode(auth.AuthorizationCodeRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: auth.PKCEChallenge(verifier), Scopes: []string{"workspace.read"}, PairingID: "pair", Audience: audience, RefreshAllowed: true})
	tokens, err := server.AuthStore.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience)
	if err != nil {
		t.Fatal(err)
	}
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover",
		"params": map[string]any{"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}},
	}
	encoded, _ := json.Marshal(rpc)
	request, _ = http.NewRequest(http.MethodPost, audience, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "server/discover")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"supportedVersions"`)) {
		t.Fatalf("MCP discover: %d %s", response.StatusCode, body)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("server did not close")
	}
	if state, err := runtime.Read(server.Workspace.ID); err != nil || state != nil {
		t.Fatalf("runtime state not cleared: %#v err=%v", state, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	request, _ = http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(server.Port)+"/health", nil)
	if _, err := http.DefaultClient.Do(request); err == nil {
		t.Fatal("closed server still accepted requests")
	}
}

func TestServerRejectsPublicBind(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	if _, err := Start(Options{WorkspaceRoot: t.TempDir(), Host: "0.0.0.0", Port: freePort(t)}); err == nil {
		t.Fatal("public bind should be rejected")
	}
}

func TestSetupSessionCreatesLocalPage(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	server, err := Start(Options{WorkspaceRoot: t.TempDir(), Port: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	payload := map[string]any{
		"connectorName":   "CodexLink · demo",
		"connectorAction": "create",
		"mcpUrl":          server.LocalBaseURL() + "/mcp",
	}
	encoded, _ := json.Marshal(payload)
	request, _ := http.NewRequest(http.MethodPost, server.LocalBaseURL()+"/admin/setup-session", bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+server.AdminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var setup struct {
		Code string `json:"code"`
		URL  string `json:"setupUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&setup); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || setup.Code == "" || setup.URL == "" {
		t.Fatalf("setup response = %d %+v", response.StatusCode, setup)
	}

	page, err := http.Get(setup.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("CodexLink · demo")) || !bytes.Contains(body, []byte(setup.Code)) {
		t.Fatalf("setup page = %d %s", page.StatusCode, body)
	}

	proxied, _ := http.NewRequest(http.MethodGet, setup.URL, nil)
	proxied.Header.Set("X-Forwarded-For", "127.0.0.1")
	proxiedResponse, err := http.DefaultClient.Do(proxied)
	if err != nil {
		t.Fatal(err)
	}
	proxiedResponse.Body.Close()
	if proxiedResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("proxied setup page = %d", proxiedResponse.StatusCode)
	}
}

func TestMCPOriginGuard(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	server, err := Start(Options{WorkspaceRoot: t.TempDir(), Port: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for _, test := range []struct {
		origin string
		want   int
	}{
		{origin: "https://evil.example", want: http.StatusForbidden},
		{origin: "https://chatgpt.com", want: http.StatusUnauthorized},
		{origin: server.LocalBaseURL(), want: http.StatusUnauthorized},
	} {
		request, _ := http.NewRequest(http.MethodPost, server.LocalBaseURL()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", test.origin)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.want {
			t.Fatalf("origin %q status = %d, want %d", test.origin, response.StatusCode, test.want)
		}
	}
}

func TestNormalizeOrigin(t *testing.T) {
	got, err := normalizeOrigin("HTTPS://ChatGPT.com/")
	if err != nil || got != "https://chatgpt.com" {
		t.Fatalf("normalizeOrigin = %q, %v", got, err)
	}
	for _, value := range []string{"null", "file:///tmp/a", "https://example.com/path", "https://user@example.com"} {
		if _, err := normalizeOrigin(value); err == nil {
			t.Fatalf("origin %q should be rejected", value)
		}
	}
}

func TestSetupStatusTracksOnlyTheCurrentMCPAudience(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	server, err := Start(Options{WorkspaceRoot: t.TempDir(), Port: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	mcpURL := server.LocalBaseURL() + "/mcp"
	payload, _ := json.Marshal(map[string]any{
		"connectorName": "CodexLink · demo",
		"mcpUrl":        mcpURL,
	})
	request, _ := http.NewRequest(http.MethodPost, server.LocalBaseURL()+"/admin/setup-session", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+server.AdminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var setup struct {
		URL string `json:"setupUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&setup); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	readStatus := func() setupui.Status {
		t.Helper()
		statusResponse, err := http.Get(setup.URL + "/status")
		if err != nil {
			t.Fatal(err)
		}
		defer statusResponse.Body.Close()
		var status setupui.Status
		if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if status := readStatus(); status.State != setupui.StateWaiting || status.Authorized {
		t.Fatalf("initial status = %+v", status)
	}

	client, err := server.AuthStore.RegisterClient("integration", []string{"http://127.0.0.1/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("w", 64)
	issue := func(audience string) {
		code, err := server.AuthStore.CreateAuthorizationCode(auth.AuthorizationCodeRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: auth.PKCEChallenge(verifier), Scopes: []string{"workspace.read"}, PairingID: "pair", Audience: audience, RefreshAllowed: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.AuthStore.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience); err != nil {
			t.Fatal(err)
		}
	}
	issue("https://old.invalid/mcp")
	if status := readStatus(); status.State != setupui.StateWaiting || status.Authorized {
		t.Fatalf("wrong-audience token changed status: %+v", status)
	}
	issue(mcpURL)
	if status := readStatus(); status.State != setupui.StateConnected || !status.Authorized || status.TokenCount != 1 {
		t.Fatalf("connected status = %+v", status)
	}

	infoRequest, _ := http.NewRequest(http.MethodGet, server.LocalBaseURL()+"/admin/info", nil)
	infoRequest.Header.Set("Authorization", "Bearer "+server.AdminToken)
	infoResponse, err := http.DefaultClient.Do(infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer infoResponse.Body.Close()
	var info struct {
		TokenCount       int                    `json:"tokenCount"`
		TokenRecordCount int                    `json:"tokenRecordCount"`
		WorkspaceMode    workspace.TopologyMode `json:"workspaceMode"`
		RepositoryCount  int                    `json:"repositoryCount"`
	}
	if err := json.NewDecoder(infoResponse.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.TokenCount != 1 || info.TokenRecordCount != 2 || info.WorkspaceMode != workspace.TopologyDirectory || info.RepositoryCount != 0 {
		t.Fatalf("admin info = %+v", info)
	}
}
