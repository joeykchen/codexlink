package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/logging"
)

type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

type BaseURLFunc func(*http.Request) string

type PendingAuthorization struct {
	ID             string
	ClientID       string
	RedirectURI    string
	Scopes         []string
	State          string
	CodeChallenge  string
	Audience       string
	RefreshAllowed bool
	ExpiresAt      time.Time
}

type OAuthServer struct {
	store            *Store
	pairing          *PairingManager
	workspaceName    string
	baseURL          BaseURLFunc
	logger           *logging.Logger
	metadataResolver ClientMetadataResolver
	mu               sync.Mutex
	pending          map[string]PendingAuthorization
	now              func() time.Time
	limits           OAuthLimits
	registerLimiter  *requestLimiter
	authorizeLimiter *requestLimiter
}

type OAuthServerOption func(*OAuthServer)

func WithClientMetadataResolver(resolver ClientMetadataResolver) OAuthServerOption {
	return func(server *OAuthServer) {
		if resolver != nil {
			server.metadataResolver = resolver
		}
	}
}

func WithOAuthLimits(limits OAuthLimits) OAuthServerOption {
	return func(server *OAuthServer) { server.limits = normalizeOAuthLimits(limits) }
}

func NewOAuthServer(store *Store, pairing *PairingManager, workspaceName string, baseURL BaseURLFunc, logger *logging.Logger, options ...OAuthServerOption) *OAuthServer {
	if logger == nil {
		logger = logging.Null()
	}
	server := &OAuthServer{
		store: store, pairing: pairing, workspaceName: workspaceName, baseURL: baseURL, logger: logger,
		metadataResolver: NewHTTPClientMetadataResolver(), pending: make(map[string]PendingAuthorization), now: time.Now,
		limits: defaultOAuthLimits(),
	}
	for _, option := range options {
		option(server)
	}
	server.limits = normalizeOAuthLimits(server.limits)
	server.registerLimiter = newRequestLimiter(server.limits.RegistrationPerIP, server.limits.RegistrationGlobal, server.limits.Window, server.limits.MaxTrackedAddresses)
	server.authorizeLimiter = newRequestLimiter(server.limits.AuthorizationPerIP, server.limits.AuthorizationGlobal, server.limits.Window, server.limits.MaxTrackedAddresses)
	return server
}

func (s *OAuthServer) Register(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.authorizationMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server/mcp", s.authorizationMetadata)
	mux.HandleFunc("/.well-known/openid-configuration", s.authorizationMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.resourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.resourceMetadata)
	mux.HandleFunc("/oauth/register", s.registerClient)
	mux.HandleFunc("/oauth/authorize", s.authorize)
	mux.HandleFunc("/oauth/token", s.token)
	mux.HandleFunc("/oauth/revoke", s.revoke)
}

func (s *OAuthServer) canonicalAudience(request *http.Request) string {
	base := strings.TrimRight(s.baseURL(request), "/")
	resource, err := CanonicalResource(base + "/mcp")
	if err != nil {
		return base + "/mcp"
	}
	return resource
}

func (s *OAuthServer) authorizationMetadata(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	base := strings.TrimRight(s.baseURL(request), "/")
	writeJSON(response, http.StatusOK, map[string]any{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/oauth/authorize",
		"token_endpoint":                                 base + "/oauth/token",
		"registration_endpoint":                          base + "/oauth/register",
		"revocation_endpoint":                            base + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
		"scopes_supported":                               SupportedScopes,
	})
}

func (s *OAuthServer) resourceMetadata(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	base := strings.TrimRight(s.baseURL(request), "/")
	writeJSON(response, http.StatusOK, map[string]any{
		"resource":                 s.canonicalAudience(request),
		"authorization_servers":    []string{base},
		"scopes_supported":         SupportedScopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            buildinfo.ProductName,
	})
}

func (s *OAuthServer) registerClient(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodOptions {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	if !s.registerLimiter.Allow(ClientIP(request), s.now()) {
		response.Header().Set("Retry-After", fmt.Sprintf("%d", int(s.limits.Window.Seconds())))
		oauthError(response, http.StatusTooManyRequests, "temporarily_unavailable", "client registration rate limit exceeded")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 256*1024)
	defer request.Body.Close()
	var metadata ClientMetadata
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&metadata); err != nil {
		oauthError(response, http.StatusBadRequest, "invalid_client_metadata", "request body must be a JSON object")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		oauthError(response, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	client, err := s.store.RegisterClientMetadata(metadata)
	if err != nil {
		if errors.Is(err, ErrCapacity) {
			oauthError(response, http.StatusServiceUnavailable, "temporarily_unavailable", "client registration capacity reached")
			return
		}
		code := "invalid_client_metadata"
		if strings.Contains(err.Error(), "redirect URI") {
			code = "invalid_redirect_uri"
		}
		oauthError(response, http.StatusBadRequest, code, err.Error())
		return
	}
	s.logger.Info("registered OAuth public client %s", client.ID)
	issuedAt, _ := time.Parse(time.RFC3339Nano, client.CreatedAt)
	writeJSON(response, http.StatusCreated, map[string]any{
		"client_id":                  client.ID,
		"client_id_issued_at":        issuedAt.Unix(),
		"client_name":                client.Name,
		"redirect_uris":              client.RedirectURIs,
		"token_endpoint_auth_method": client.TokenEndpointAuthMethod,
		"grant_types":                client.GrantTypes,
		"response_types":             client.ResponseTypes,
		"application_type":           client.ApplicationType,
	})
}

func (s *OAuthServer) resolveClient(ctx context.Context, clientID string) (Client, error) {
	if client, ok := s.store.Client(clientID); ok {
		return client, nil
	}
	if !isMetadataClientID(clientID) {
		return Client{}, fmt.Errorf("client is not registered")
	}
	return s.metadataResolver.Resolve(ctx, clientID)
}

func (s *OAuthServer) authorize(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		s.authorizeStart(response, request)
	case http.MethodPost:
		s.authorizeComplete(response, request)
	default:
		methodNotAllowed(response, http.MethodGet, http.MethodPost)
	}
}

func (s *OAuthServer) prunePendingLocked() {
	now := s.now()
	for id, pending := range s.pending {
		if !now.Before(pending.ExpiresAt) {
			delete(s.pending, id)
		}
	}
}

func (s *OAuthServer) authorizeStart(response http.ResponseWriter, request *http.Request) {
	if !s.authorizeLimiter.Allow(ClientIP(request), s.now()) {
		response.Header().Set("Retry-After", fmt.Sprintf("%d", int(s.limits.Window.Seconds())))
		secureHTMLHeaders(response)
		http.Error(response, "Authorization rate limit exceeded. Try again shortly.", http.StatusTooManyRequests)
		return
	}
	query := request.URL.Query()
	clientID := query.Get("client_id")
	client, err := s.resolveClient(request.Context(), clientID)
	if err != nil {
		secureHTMLHeaders(response)
		http.Error(response, "Unknown or invalid OAuth client. Start the connection again.", http.StatusBadRequest)
		s.logger.Warn("OAuth client resolution failed: %v", err)
		return
	}
	redirect := query.Get("redirect_uri")
	if !RedirectURIMatches(client.RedirectURIs, redirect) {
		secureHTMLHeaders(response)
		http.Error(response, "The redirect URI is not registered for this client.", http.StatusBadRequest)
		return
	}
	fail := func(code, description string) {
		location, _ := url.Parse(redirect)
		values := location.Query()
		values.Set("error", code)
		values.Set("error_description", description)
		if state := query.Get("state"); state != "" {
			values.Set("state", state)
		}
		values.Set("iss", strings.TrimRight(s.baseURL(request), "/"))
		location.RawQuery = values.Encode()
		http.Redirect(response, request, location.String(), http.StatusFound)
	}
	if query.Get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	challenge := query.Get("code_challenge")
	if query.Get("code_challenge_method") != "S256" || !ValidChallenge(challenge) {
		fail("invalid_request", "PKCE S256 is required")
		return
	}
	expectedAudience := s.canonicalAudience(request)
	audience := query.Get("resource")
	if audience == "" {
		audience = expectedAudience // compatibility with pre-resource clients
	} else if !sameResource(audience, expectedAudience) {
		fail("invalid_target", "resource does not identify this MCP endpoint")
		return
	} else {
		audience = expectedAudience
	}
	scopes, err := ParseScopes(query.Get("scope"))
	if err != nil {
		fail("invalid_scope", err.Error())
		return
	}
	refreshAllowed := containsString(client.GrantTypes, "refresh_token")
	if containsScope(scopes, "offline_access") && !refreshAllowed {
		fail("invalid_scope", "offline_access requires the refresh_token grant")
		return
	}
	id, err := secureID(24)
	if err != nil {
		http.Error(response, "authorization service unavailable", http.StatusInternalServerError)
		return
	}
	pending := PendingAuthorization{
		ID: id, ClientID: client.ID, RedirectURI: redirect, Scopes: scopes,
		State: query.Get("state"), CodeChallenge: challenge, Audience: audience,
		RefreshAllowed: refreshAllowed, ExpiresAt: s.now().Add(10 * time.Minute),
	}
	s.mu.Lock()
	s.prunePendingLocked()
	if len(s.pending) >= s.limits.MaxPending {
		s.mu.Unlock()
		fail("temporarily_unavailable", "too many authorization requests are pending")
		return
	}
	s.pending[id] = pending
	s.mu.Unlock()
	secureHTMLHeaders(response)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, renderPairingPage(pairingPageData{RequestID: id, WorkspaceName: s.workspaceName, Scopes: pending.Scopes}))
}

func (s *OAuthServer) authorizeComplete(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, 64*1024)
	if err := request.ParseForm(); err != nil {
		http.Error(response, "invalid authorization form", http.StatusBadRequest)
		return
	}
	requestID := request.Form.Get("request_id")
	s.mu.Lock()
	s.prunePendingLocked()
	pending, ok := s.pending[requestID]
	s.mu.Unlock()
	if !ok {
		secureHTMLHeaders(response)
		http.Error(response, "This authorization request expired. Start the connection again.", http.StatusBadRequest)
		return
	}
	verdict := s.pairing.Verify(request.Form.Get("pairing_code"), ClientIP(request))
	if !verdict.OK {
		message := pairingFailureMessage(verdict)
		status := http.StatusGone
		if verdict.Failure == PairingInvalid {
			status = http.StatusUnauthorized
		}
		secureHTMLHeaders(response)
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.WriteHeader(status)
		_, _ = io.WriteString(response, renderPairingPage(pairingPageData{RequestID: requestID, WorkspaceName: s.workspaceName, Scopes: pending.Scopes, Error: message}))
		return
	}
	s.mu.Lock()
	delete(s.pending, requestID)
	s.mu.Unlock()
	code, err := s.store.CreateAuthorizationCode(AuthorizationCodeRequest{
		ClientID: pending.ClientID, RedirectURI: pending.RedirectURI, CodeChallenge: pending.CodeChallenge,
		Scopes: pending.Scopes, PairingID: verdict.SessionID, Audience: pending.Audience, RefreshAllowed: pending.RefreshAllowed,
	})
	if err != nil {
		http.Error(response, "authorization service unavailable", http.StatusInternalServerError)
		return
	}
	location, _ := url.Parse(pending.RedirectURI)
	values := location.Query()
	values.Set("code", code)
	if pending.State != "" {
		values.Set("state", pending.State)
	}
	values.Set("iss", strings.TrimRight(s.baseURL(request), "/"))
	location.RawQuery = values.Encode()
	s.logger.Info("pairing accepted for OAuth client %s", pending.ClientID)
	http.Redirect(response, request, location.String(), http.StatusFound)
}

func (s *OAuthServer) token(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	parameters, err := parseOAuthParameters(response, request)
	if err != nil {
		oauthError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	grantType := parameters.Get("grant_type")
	switch grantType {
	case "authorization_code":
		code, verifier, clientID := parameters.Get("code"), parameters.Get("code_verifier"), parameters.Get("client_id")
		redirectURI := parameters.Get("redirect_uri")
		if code == "" || verifier == "" || clientID == "" || redirectURI == "" {
			oauthError(response, http.StatusBadRequest, "invalid_request", "code, code_verifier, client_id, and redirect_uri are required")
			return
		}
		set, exchangeErr := s.store.ExchangeAuthorizationCode(code, clientID, redirectURI, verifier, parameters.Get("resource"))
		if exchangeErr != nil {
			oauthError(response, oauthStatus(exchangeErr), oauthCode(exchangeErr), "authorization code exchange failed")
			return
		}
		writeTokenSet(response, set)
	case "refresh_token":
		refresh, clientID := parameters.Get("refresh_token"), parameters.Get("client_id")
		if refresh == "" || clientID == "" {
			oauthError(response, http.StatusBadRequest, "invalid_request", "refresh_token and client_id are required")
			return
		}
		set, refreshErr := s.store.Refresh(refresh, clientID, parameters.Get("resource"))
		if refreshErr != nil {
			oauthError(response, oauthStatus(refreshErr), oauthCode(refreshErr), "refresh token exchange failed")
			return
		}
		writeTokenSet(response, set)
	default:
		oauthError(response, http.StatusBadRequest, "unsupported_grant_type", "supported grants: authorization_code, refresh_token")
	}
}

func (s *OAuthServer) revoke(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	parameters, err := parseOAuthParameters(response, request)
	if err == nil && parameters.Get("token") != "" {
		_ = s.store.Revoke(parameters.Get("token"))
	}
	writeJSON(response, http.StatusOK, map[string]any{})
}

func BearerMiddleware(store *Store, baseURL BaseURLFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		base := strings.TrimRight(baseURL(request), "/")
		audience, _ := CanonicalResource(base + "/mcp")
		challenge := func(description string) {
			metadata := base + "/.well-known/oauth-protected-resource/mcp"
			response.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="codexlink", error="invalid_token", error_description=%q, resource_metadata=%q, scope=%q`, description, metadata, strings.Join(SupportedScopes, " ")))
			writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "error_description": description})
		}
		header := strings.TrimSpace(request.Header.Get("Authorization"))
		if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
			challenge("authentication is required")
			return
		}
		token := strings.TrimSpace(header[7:])
		principal, err := store.VerifyAccess(token, audience)
		if err != nil {
			challenge("the access token is invalid or expired")
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func ClientIP(request *http.Request) string {
	if value := strings.TrimSpace(request.Header.Get("CF-Connecting-IP")); value != "" {
		if ip := net.ParseIP(value); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func writeTokenSet(response http.ResponseWriter, set TokenSet) {
	payload := map[string]any{
		"access_token": set.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   set.ExpiresIn,
		"scope":        strings.Join(set.Scopes, " "),
	}
	if set.RefreshToken != "" {
		payload["refresh_token"] = set.RefreshToken
	}
	writeJSON(response, http.StatusOK, payload)
}

func parseOAuthParameters(response http.ResponseWriter, request *http.Request) (url.Values, error) {
	request.Body = http.MaxBytesReader(response, request.Body, 256*1024)
	contentType := strings.ToLower(request.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		defer request.Body.Close()
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		if err := decoder.Decode(&body); err != nil {
			return nil, err
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return nil, err
		}
		values := url.Values{}
		for key, value := range body {
			if text, ok := value.(string); ok {
				values.Set(key, text)
			}
		}
		return values, nil
	}
	if err := request.ParseForm(); err != nil {
		return nil, err
	}
	return request.PostForm, nil
}

func secureID(bytesCount int) (string, error) {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func methodNotAllowed(response http.ResponseWriter, allowed ...string) {
	response.Header().Set("Allow", strings.Join(allowed, ", "))
	writeJSON(response, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
}

func oauthCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidGrant):
		return "invalid_grant"
	case errors.Is(err, ErrInvalidClient):
		return "invalid_client"
	case errors.Is(err, ErrInvalidTarget):
		return "invalid_target"
	case errors.Is(err, ErrCapacity):
		return "temporarily_unavailable"
	default:
		return "server_error"
	}
}

func oauthStatus(err error) int {
	if errors.Is(err, ErrCapacity) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

func oauthError(response http.ResponseWriter, status int, code, description string) {
	writeJSON(response, status, map[string]any{"error": code, "error_description": description})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func secureHTMLHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}

type pairingPageData struct {
	RequestID     string
	WorkspaceName string
	Scopes        []string
	Error         string
}

var pairingTemplate = template.Must(template.New("pairing").Funcs(template.FuncMap{"scopeLabel": scopeLabel}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>CodexLink authorization</title>
<style>
:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;color-scheme:light dark}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:linear-gradient(140deg,#eef3ff,#f8f9fb);color:#172033}
main{width:min(92vw,480px);padding:32px;border:1px solid rgba(90,105,130,.2);border-radius:20px;background:rgba(255,255,255,.92);box-shadow:0 24px 80px rgba(30,45,70,.14)}
h1{font-size:1.35rem;margin:0 0 8px}.muted{color:#667085;font-size:.92rem;line-height:1.45}ul{padding-left:20px;font-size:.88rem;line-height:1.55}
input{width:100%;padding:14px 16px;border-radius:12px;border:1px solid #aab4c7;font:700 1.35rem ui-monospace,monospace;letter-spacing:.18em;text-align:center;text-transform:uppercase;background:transparent;color:inherit}
button{width:100%;margin-top:14px;padding:13px;border:0;border-radius:12px;background:#315eea;color:white;font-weight:700;cursor:pointer}.error{color:#b42318;font-size:.88rem}.foot{text-align:center;font-size:.78rem;color:#7a8497;margin:16px 0 0}
@media(prefers-color-scheme:dark){body{background:#101522;color:#edf2ff}main{background:#171e2e;border-color:#303a52}.muted,.foot{color:#aeb8cc}}
</style>
</head>
<body><main>
<h1>Authorize a read-only workspace connection</h1>
<p class="muted">A client is requesting access to <strong>{{.WorkspaceName}}</strong>. It may:</p>
<ul>{{range .Scopes}}<li>{{scopeLabel .}}</li>{{end}}</ul>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="request_id" value="{{.RequestID}}">
<label for="pairing_code" class="muted">Enter the one-time code shown by the local CLI</label>
<input id="pairing_code" name="pairing_code" autocomplete="one-time-code" inputmode="text" maxlength="9" placeholder="XXXX-XXXX" required autofocus>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<button type="submit">Authorize connection</button>
</form><p class="foot">The code expires quickly and is destroyed after a successful use.</p>
</main></body></html>`))

func renderPairingPage(data pairingPageData) string {
	var builder strings.Builder
	if err := pairingTemplate.Execute(&builder, data); err != nil {
		return "authorization page unavailable"
	}
	return builder.String()
}

func scopeLabel(scope string) string {
	labels := map[string]string{
		"workspace.read":   "Read non-sensitive files and project metadata",
		"workspace.search": "Search text inside the workspace",
		"git.read":         "Inspect Git status and safe diffs",
		"execution.read":   "Read local execution and test summaries",
		"offline_access":   "Refresh the connection without re-pairing",
	}
	if label := labels[scope]; label != "" {
		return label
	}
	return scope
}

func pairingFailureMessage(result PairingResult) string {
	switch result.Failure {
	case PairingInvalid:
		return fmt.Sprintf("The code is not correct. %d attempt(s) remain.", result.AttemptsLeft)
	case PairingExpired:
		return "The code expired. Generate a new code in the local CLI."
	case PairingTooManyAttempts:
		return "The code was locked after too many attempts. Generate a new one."
	case PairingRateLimited:
		return "Too many attempts from this address. Try again after a short pause."
	default:
		return "No active pairing code exists. Generate one in the local CLI."
	}
}
