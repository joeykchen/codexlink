package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joeykchen/codexlink/internal/logging"
)

func TestOAuthHTTPFlowAndBearerMiddleware(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	pairing := NewPairingManager("ws", PairingOptions{})
	logger := logging.Null()
	mux := http.NewServeMux()
	var server *httptest.Server
	oauth := NewOAuthServer(store, pairing, "demo", func(*http.Request) string { return server.URL }, logger)
	oauth.Register(mux)
	mux.Handle("/protected", BearerMiddleware(store, func(*http.Request) string { return server.URL }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("principal missing")
		}
		writeJSON(w, http.StatusOK, map[string]any{"client": principal.ClientID})
	})))
	server = httptest.NewServer(mux)
	defer server.Close()

	registration := map[string]any{
		"client_name":                "test",
		"redirect_uris":              []string{"http://127.0.0.1/callback"},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"application_type":           "native",
		"contacts":                   []string{"oauth@example.invalid"}, // unknown RFC metadata must be ignored
	}
	payload, _ := json.Marshal(registration)
	response, err := http.Post(server.URL+"/oauth/register", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("registration = %d %s", response.StatusCode, body)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("z", 64)
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {registered.ClientID},
		"redirect_uri":          {"http://127.0.0.1/callback"},
		"scope":                 {"workspace.read offline_access"},
		"state":                 {"state123"},
		"code_challenge":        {PKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"resource":              {server.URL + "/mcp"},
	}
	response, err = http.Get(server.URL + "/oauth/authorize?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorize start = %d %s", response.StatusCode, page)
	}
	match := regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("request id missing from page: %s", page)
	}
	code, _, err := pairing.Create()
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"request_id": {string(match[1])}, "pairing_code": {code}}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("authorize complete = %d %s", response.StatusCode, body)
	}
	location, _ := url.Parse(response.Header.Get("Location"))
	response.Body.Close()
	if location.Query().Get("state") != "state123" || location.Query().Get("code") == "" || location.Query().Get("iss") != server.URL {
		t.Fatalf("unexpected redirect: %s", location)
	}
	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {location.Query().Get("code")},
		"code_verifier": {verifier},
		"client_id":     {registered.ClientID},
		"redirect_uri":  {"http://127.0.0.1/callback"},
		"resource":      {server.URL + "/mcp"},
	}
	response, err = http.Post(server.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(tokenForm.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokens); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("unexpected token response: %d %#v", response.StatusCode, tokens)
	}

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/protected", nil)
	response, _ = http.DefaultClient.Do(request)
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(response.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("missing challenge: %d %s", response.StatusCode, response.Header.Get("WWW-Authenticate"))
	}
	response.Body.Close()
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/protected", nil)
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("protected = %d %s", response.StatusCode, body)
	}
}

func TestOAuthMetadataAdvertisesCIMDAndIssuerResponse(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	oauth := NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo", func(*http.Request) string { return server.URL }, logging.Null())
	oauth.Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()

	response, err := http.Get(server.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var metadata map[string]any
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["client_id_metadata_document_supported"] != true || metadata["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("metadata missing CIMD/issuer flags: %#v", metadata)
	}
}

func TestOAuthAuthorizationAcceptsCIMDClient(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	pairing := NewPairingManager("ws", PairingOptions{})
	clientID := "https://chatgpt.com/oauth/client.json"
	redirectURI := "https://chatgpt.com/backend-api/aip/oauth/callback"
	resolverCalls := 0
	resolver := ClientMetadataResolverFunc(func(_ context.Context, requested string) (Client, error) {
		resolverCalls++
		if requested != clientID {
			t.Fatalf("client id = %q", requested)
		}
		return Client{ID: requested, Name: "ChatGPT", RedirectURIs: []string{redirectURI}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}}, nil
	})
	mux := http.NewServeMux()
	var server *httptest.Server
	oauth := NewOAuthServer(store, pairing, "demo", func(*http.Request) string { return server.URL }, logging.Null(), WithClientMetadataResolver(resolver))
	oauth.Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()

	verifier := strings.Repeat("x", 64)
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"workspace.read offline_access"},
		"state":                 {"cimd-state"},
		"code_challenge":        {PKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"resource":              {server.URL + "/mcp"},
	}
	response, err := http.Get(server.URL + "/oauth/authorize?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || resolverCalls != 1 {
		t.Fatalf("authorize = %d calls=%d body=%s", response.StatusCode, resolverCalls, page)
	}
	match := regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("request id missing: %s", page)
	}
	pairingCode, _, err := pairing.Create()
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"request_id": {string(match[1])}, "pairing_code": {pairingCode}}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := url.Parse(response.Header.Get("Location"))
	response.Body.Close()
	if response.StatusCode != http.StatusFound || location.Query().Get("code") == "" || location.Query().Get("iss") != server.URL {
		t.Fatalf("callback = %d %s", response.StatusCode, location)
	}
}

func TestDynamicRegistrationRejectsMultipleJSONValues(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo", func(*http.Request) string { return server.URL }, logging.Null()).Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()
	body := `{"client_name":"x","redirect_uris":["https://example.com/callback"]} {}`
	response, err := http.Post(server.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestOAuthTokenJSONRejectsTrailingValues(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo", func(*http.Request) string { return server.URL }, logging.Null()).Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()

	response, err := http.Post(server.URL+"/oauth/token", "application/json", strings.NewReader(`{"grant_type":"refresh_token"} {}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "invalid_request" {
		t.Fatalf("response = %#v", body)
	}
}
