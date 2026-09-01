package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/logging"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	registry, err := NewRegistry(
		Tool{
			Definition: ToolDefinition{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}},
			Handler:    func(_ context.Context, args map[string]any) (any, error) { return args, nil },
		},
		Tool{
			Definition: ToolDefinition{Name: "回声", Description: "unicode echo", InputSchema: map[string]any{"type": "object"}},
			Handler:    func(_ context.Context, args map[string]any) (any, error) { return args, nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(registry, logging.Null())
}

func postRPC(t *testing.T, handler http.Handler, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func modernMeta(version string) map[string]any {
	return map[string]any{
		protocolVersionMetaKey: version,
		clientInfoMetaKey: map[string]any{
			"name":    "test-client",
			"version": "test",
		},
		clientCapabilitiesMetaKey: map[string]any{},
	}
}

func modernHeaders(method string) map[string]string {
	return map[string]string{
		"MCP-Protocol-Version": buildinfo.ModernProtocol,
		"Mcp-Method":           method,
	}
}

func decodeRPCResponse(t *testing.T, recorder *httptest.ResponseRecorder) RPCResponse {
	t.Helper()
	var response RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v: %s", err, recorder.Body.String())
	}
	return response
}

func TestRoutingHeaderMismatchRejectedForLegacy(t *testing.T) {
	handler := testHandler(t)
	recorder := postRPC(t, handler, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, map[string]string{"Mcp-Method": "tools/list"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if response := decodeRPCResponse(t, recorder); response.Error == nil || response.Error.Code != headerMismatchCode {
		t.Fatalf("response = %#v", response)
	}
}

func TestModernRequestRequiresMetadataAndRoutingHeaders(t *testing.T) {
	handler := testHandler(t)
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "echo", "arguments": map[string]any{"x": 1},
			"_meta": modernMeta(buildinfo.ModernProtocol),
		},
	}

	missingHeaders := postRPC(t, handler, body, map[string]string{"MCP-Protocol-Version": buildinfo.ModernProtocol})
	if missingHeaders.Code != http.StatusBadRequest {
		t.Fatalf("missing headers status = %d body=%s", missingHeaders.Code, missingHeaders.Body.String())
	}
	if response := decodeRPCResponse(t, missingHeaders); response.Error == nil || response.Error.Code != headerMismatchCode {
		t.Fatalf("missing headers response = %#v", response)
	}

	headers := modernHeaders("tools/call")
	headers["Mcp-Name"] = "echo"
	ok := postRPC(t, handler, body, headers)
	if ok.Code != http.StatusOK {
		t.Fatalf("modern request failed: %d %s", ok.Code, ok.Body.String())
	}
	if ok.Header().Get("MCP-Protocol-Version") != buildinfo.ModernProtocol {
		t.Fatalf("protocol response header missing: %v", ok.Header())
	}
	response := decodeRPCResponse(t, ok)
	result := response.Result.(map[string]any)
	if result["resultType"] != "complete" || result["_meta"] == nil {
		t.Fatalf("modern result = %#v", result)
	}

	withoutMeta := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	}
	invalid := postRPC(t, handler, withoutMeta, modernHeaders("tools/list"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("missing metadata status = %d body=%s", invalid.Code, invalid.Body.String())
	}
	if response := decodeRPCResponse(t, invalid); response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("missing metadata response = %#v", response)
	}
}

func TestModernProtocolMismatchAndUnsupportedVersion(t *testing.T) {
	handler := testHandler(t)
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		"params": map[string]any{"_meta": modernMeta(buildinfo.ModernProtocol)},
	}
	mismatchHeaders := modernHeaders("tools/list")
	mismatchHeaders["MCP-Protocol-Version"] = "2025-11-25"
	mismatch := postRPC(t, handler, body, mismatchHeaders)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatch status = %d body=%s", mismatch.Code, mismatch.Body.String())
	}
	if response := decodeRPCResponse(t, mismatch); response.Error == nil || response.Error.Code != headerMismatchCode {
		t.Fatalf("mismatch response = %#v", response)
	}

	unknown := "2099-01-01"
	body["params"] = map[string]any{"_meta": modernMeta(unknown)}
	unsupportedHeaders := modernHeaders("tools/list")
	unsupportedHeaders["MCP-Protocol-Version"] = unknown
	unsupported := postRPC(t, handler, body, unsupportedHeaders)
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported status = %d body=%s", unsupported.Code, unsupported.Body.String())
	}
	response := decodeRPCResponse(t, unsupported)
	if response.Error == nil || response.Error.Code != unsupportedProtocolCode {
		t.Fatalf("unsupported response = %#v", response)
	}
	data := response.Error.Data.(map[string]any)
	if data["requested"] != unknown {
		t.Fatalf("unsupported data = %#v", data)
	}
}

func TestModernMcpNameSupportsBase64Sentinel(t *testing.T) {
	handler := testHandler(t)
	name := "回声"
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": name, "arguments": map[string]any{},
			"_meta": modernMeta(buildinfo.ModernProtocol),
		},
	}
	headers := modernHeaders("tools/call")
	headers["Mcp-Name"] = "=?base64?" + base64.StdEncoding.EncodeToString([]byte(name)) + "?="
	recorder := postRPC(t, handler, body, headers)
	if recorder.Code != http.StatusOK {
		t.Fatalf("base64 name failed: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestModernDiscoverAndUnknownMethodHTTPStatus(t *testing.T) {
	handler := testHandler(t)
	discover := map[string]any{
		"jsonrpc": "2.0", "id": "discover-1", "method": "server/discover",
		"params": map[string]any{"_meta": modernMeta(buildinfo.ModernProtocol)},
	}
	recorder := postRPC(t, handler, discover, modernHeaders("server/discover"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("discover = %d %s", recorder.Code, recorder.Body.String())
	}
	result := decodeRPCResponse(t, recorder).Result.(map[string]any)
	if result["resultType"] != "complete" || result["supportedVersions"] == nil || result["_meta"] == nil {
		t.Fatalf("discover result = %#v", result)
	}

	unknown := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "unknown/method",
		"params": map[string]any{"_meta": modernMeta(buildinfo.ModernProtocol)},
	}
	recorder = postRPC(t, handler, unknown, modernHeaders("unknown/method"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown method = %d %s", recorder.Code, recorder.Body.String())
	}
	if response := decodeRPCResponse(t, recorder); response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("unknown method response = %#v", response)
	}
}

func TestLegacyNotificationAndBatchRemainCompatible(t *testing.T) {
	handler := testHandler(t)
	notification := postRPC(t, handler, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil)
	if notification.Code != http.StatusAccepted || notification.Body.Len() != 0 {
		t.Fatalf("notification = %d %q", notification.Code, notification.Body.String())
	}
	batch := []map[string]any{
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": 2, "method": "ping"},
	}
	recorder := postRPC(t, handler, batch, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", recorder.Code, recorder.Body.String())
	}
	var responses []RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &responses); err != nil || len(responses) != 1 {
		t.Fatalf("batch response = %#v err=%v", responses, err)
	}
}

func TestModernBatchIsRejectedWithoutExecution(t *testing.T) {
	handler := testHandler(t)
	batch := []map[string]any{{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		"params": map[string]any{"_meta": modernMeta(buildinfo.ModernProtocol)},
	}}
	recorder := postRPC(t, handler, batch, modernHeaders("tools/list"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("modern batch = %d %s", recorder.Code, recorder.Body.String())
	}
}
