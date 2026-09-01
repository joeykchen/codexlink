package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("authorize complete = %d %s", response.StatusCode, body)
	}
	if response.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("authorize redirect content type = %q", response.Header.Get("Content-Type"))
	}
	location, _ := url.Parse(response.Header.Get("Location"))
	response.Body.Close()
	if location.Query().Get("state") != "state123" || location.Query().Get("code") == "" || location.Query().Get("iss") != server.URL {
		t.Fatalf("unexpected redirect: %s", location)
	}
	duplicateRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	duplicateRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err = client.Do(duplicateRequest)
	if err != nil {
		t.Fatal(err)
	}
	duplicateLocation, _ := url.Parse(response.Header.Get("Location"))
	response.Body.Close()
	if response.StatusCode != http.StatusOK || duplicateLocation.String() != location.String() {
		t.Fatalf("duplicate authorize complete = %d %s, want original redirect %s", response.StatusCode, duplicateLocation, location)
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
	if response.StatusCode != http.StatusOK || location.Query().Get("code") == "" || location.Query().Get("iss") != server.URL {
		t.Fatalf("callback = %d %s", response.StatusCode, location)
	}
}

type acceptedAssertionVerifier struct{}

func (acceptedAssertionVerifier) Verify(_ context.Context, assertion string, client Client, audience string, now time.Time) (string, time.Time, error) {
	if assertion != "signed-assertion" || client.ID != "https://chatgpt.com/oauth/client.json" || !strings.HasSuffix(audience, "/oauth/token") {
		return "", time.Time{}, errors.New("unexpected assertion input")
	}
	return "assertion-id", now.Add(time.Minute), nil
}

func TestOAuthTokenAcceptsPrivateKeyJWTClient(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	clientID := "https://chatgpt.com/oauth/client.json"
	redirectURI := "https://chatgpt.com/connector_platform_oauth_redirect"
	verifier := strings.Repeat("v", 64)
	audience := "https://bridge.example/mcp"
	code, err := store.CreateAuthorizationCode(AuthorizationCodeRequest{
		ClientID: clientID, RedirectURI: redirectURI, CodeChallenge: PKCEChallenge(verifier),
		Scopes: []string{"workspace.read"}, PairingID: "pair", Audience: audience,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := ClientMetadataResolverFunc(func(_ context.Context, requested string) (Client, error) {
		return Client{ID: requested, TokenEndpointAuthMethod: "private_key_jwt"}, nil
	})
	oauth := NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo",
		func(*http.Request) string { return "https://bridge.example" }, logging.Null(),
		WithClientMetadataResolver(resolver), WithClientAssertionVerifier(acceptedAssertionVerifier{}))
	mux := http.NewServeMux()
	oauth.Register(mux)

	form := url.Values{
		"grant_type":            {"authorization_code"},
		"code":                  {code},
		"code_verifier":         {verifier},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"resource":              {audience},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {"signed-assertion"},
	}
	request := httptest.NewRequest(http.MethodPost, "https://bridge.example/oauth/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("private_key_jwt token exchange = %d %s", response.Code, response.Body.String())
	}
	if store.TokenCount() != 1 {
		t.Fatalf("token count = %d, want 1", store.TokenCount())
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

func TestOAuthRejectsUnknownScopeAndRefreshScopeWithoutGrant(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.RegisterClientMetadata(ClientMetadata{
		ClientName: "code-only", RedirectURIs: []string{"https://client.example/callback"},
		GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo", func(*http.Request) string { return server.URL }, logging.Null()).Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, scope := range []string{"unknown", "workspace.read offline_access"} {
		query := url.Values{
			"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {client.RedirectURIs[0]},
			"scope": {scope}, "code_challenge": {PKCEChallenge(strings.Repeat("s", 64))},
			"code_challenge_method": {"S256"}, "resource": {server.URL + "/mcp"},
		}
		response, err := httpClient.Get(server.URL + "/oauth/authorize?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		location, _ := url.Parse(response.Header.Get("Location"))
		response.Body.Close()
		if response.StatusCode != http.StatusFound || location.Query().Get("error") != "invalid_scope" {
			t.Fatalf("scope %q response = %d %s", scope, response.StatusCode, location)
		}
	}
}

func TestOAuthRegistrationAndPendingAuthorizationAreBounded(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	limits := OAuthLimits{
		MaxPending: 1, RegistrationPerIP: 1, RegistrationGlobal: 10,
		AuthorizationPerIP: 10, AuthorizationGlobal: 10, MaxTrackedAddresses: 4, Window: time.Minute,
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo", func(*http.Request) string { return server.URL }, logging.Null(), WithOAuthLimits(limits)).Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()

	registration := `{"client_name":"test","redirect_uris":["https://client.example/callback"]}`
	first, err := http.Post(server.URL+"/oauth/register", "application/json", strings.NewReader(registration))
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	second, err := http.Post(server.URL+"/oauth/register", "application/json", strings.NewReader(registration))
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests || second.Header.Get("Retry-After") == "" {
		t.Fatalf("registration limit response = %d", second.StatusCode)
	}

	query := url.Values{
		"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {"https://client.example/callback"},
		"scope": {"workspace.read"}, "code_challenge": {PKCEChallenge(strings.Repeat("p", 64))},
		"code_challenge_method": {"S256"}, "resource": {server.URL + "/mcp"},
	}
	response, err := http.Get(server.URL + "/oauth/authorize?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first pending authorization = %d", response.StatusCode)
	}
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = httpClient.Get(server.URL + "/oauth/authorize?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	location, _ := url.Parse(response.Header.Get("Location"))
	response.Body.Close()
	if response.StatusCode != http.StatusFound || location.Query().Get("error") != "temporarily_unavailable" {
		t.Fatalf("pending limit response = %d %s", response.StatusCode, location)
	}
}

func TestOAuthTokenRequiresRedirectURI(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.RegisterClient("client", []string{"https://client.example/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("r", 64)
	code, err := store.CreateAuthorizationCode(AuthorizationCodeRequest{
		ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier),
		Scopes: []string{"workspace.read"}, Audience: "https://bridge.example/mcp", RefreshAllowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var server *httptest.Server
	NewOAuthServer(store, NewPairingManager("ws", PairingOptions{}), "demo", func(*http.Request) string { return server.URL }, logging.Null()).Register(mux)
	server = httptest.NewServer(mux)
	defer server.Close()
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {client.ID}, "resource": {"https://bridge.example/mcp"},
	}
	response, err := http.Post(server.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("token response = %d %#v", response.StatusCode, body)
	}
}
