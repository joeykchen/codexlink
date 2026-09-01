package setupui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagerServesOnlyLocalExpiringSessions(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	manager := New()
	manager.now = func() time.Time { return now }
	session, err := manager.Create(Session{
		WorkspaceName: "demo <repo>", ConnectorName: "CodexLink · demo",
		ConnectorAction: "create", MCPURL: "https://example.test/mcp",
		PairingCode: "ABCD-EFGH", ExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/setup/"+session.ID, nil)
	request.RemoteAddr = "127.0.0.1:51000"
	response := httptest.NewRecorder()
	manager.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "CodexLink · demo") || !strings.Contains(body, "ABCD-EFGH") {
		t.Fatalf("setup page = %d %s", response.Code, body)
	}
	if strings.Contains(body, "demo <repo>") || !strings.Contains(body, "demo &lt;repo&gt;") {
		t.Fatalf("workspace name was not escaped: %s", body)
	}
	if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers missing: %v", response.Header())
	}

	proxied := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/setup/"+session.ID, nil)
	proxied.RemoteAddr = "127.0.0.1:51001"
	proxied.Header.Set("X-Forwarded-For", "127.0.0.1")
	proxiedResponse := httptest.NewRecorder()
	manager.ServeHTTP(proxiedResponse, proxied)
	if proxiedResponse.Code != http.StatusNotFound {
		t.Fatalf("proxied setup status = %d", proxiedResponse.Code)
	}

	now = now.Add(6 * time.Minute)
	expired := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/setup/"+session.ID, nil)
	expired.RemoteAddr = "127.0.0.1:51002"
	expiredResponse := httptest.NewRecorder()
	manager.ServeHTTP(expiredResponse, expired)
	if expiredResponse.Code != http.StatusGone {
		t.Fatalf("expired setup status = %d", expiredResponse.Code)
	}
}

func TestManagerValidatesSession(t *testing.T) {
	manager := New()
	_, err := manager.Create(Session{WorkspaceName: "x", ConnectorName: "x", MCPURL: "https://example.test/not-mcp", PairingCode: "x", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil {
		t.Fatal("invalid MCP URL should be rejected")
	}
}

func TestManagerReportsAuthorizationProgress(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	state := StateWaiting
	manager := New(func(Session) Status {
		switch state {
		case StateConnected:
			return Status{State: state, Authorized: true, TokenCount: 2}
		default:
			return Status{State: state}
		}
	})
	manager.now = func() time.Time { return now }
	session, err := manager.Create(Session{
		WorkspaceName: "demo", ConnectorName: "CodexLink · demo", ConnectorAction: "create",
		MCPURL: "https://example.test/mcp", PairingCode: "ABCD-EFGH", ExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func() string {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/setup/"+session.ID+"/status", nil)
		request.RemoteAddr = "127.0.0.1:51000"
		response := httptest.NewRecorder()
		manager.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status = %d headers=%v", response.Code, response.Header())
		}
		return response.Body.String()
	}
	if body := read(); !strings.Contains(body, `"state":"waiting"`) {
		t.Fatalf("waiting body = %s", body)
	}
	state = StateFinishing
	if body := read(); !strings.Contains(body, `"state":"finishing"`) {
		t.Fatalf("finishing body = %s", body)
	}
	state = StateConnected
	if body := read(); !strings.Contains(body, `"authorized":true`) || !strings.Contains(body, `"tokenCount":2`) {
		t.Fatalf("connected body = %s", body)
	}
}

func TestManagerReportsAuthorizationFailure(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	manager := New(func(Session) Status {
		return Status{State: StateFailed, Message: "token exchange failed"}
	})
	manager.now = func() time.Time { return now }
	session, err := manager.Create(Session{
		WorkspaceName: "demo", ConnectorName: "CodexLink · demo", ConnectorAction: "create",
		MCPURL: "https://example.test/mcp", PairingCode: "ABCD-EFGH", ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/setup/"+session.ID+"/status", nil)
	request.RemoteAddr = "127.0.0.1:51000"
	response := httptest.NewRecorder()
	manager.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"failed"`) || !strings.Contains(response.Body.String(), `"message":"token exchange failed"`) {
		t.Fatalf("failure status = %d %s", response.Code, response.Body.String())
	}
}
