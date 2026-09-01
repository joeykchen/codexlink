package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/buildinfo"
)

const (
	maxClientMetadataURIBytes     = 2048
	maxClientMetadataNameBytes    = 1024
	maxClientMetadataValueBytes   = 128
	maxClientMetadataRedirectURIs = 32
	maxClientMetadataValues       = 16
	maxClientMetadataScopeBytes   = 4096
)

// ClientMetadata is the registration surface shared by Dynamic Client
// Registration and Client ID Metadata Documents.
type ClientMetadata struct {
	ClientID                          string   `json:"client_id,omitempty"`
	ClientName                        string   `json:"client_name,omitempty"`
	ClientURI                         string   `json:"client_uri,omitempty"`
	LogoURI                           string   `json:"logo_uri,omitempty"`
	RedirectURIs                      []string `json:"redirect_uris"`
	GrantTypes                        []string `json:"grant_types,omitempty"`
	ResponseTypes                     []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod           string   `json:"token_endpoint_auth_method,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	ApplicationType                   string   `json:"application_type,omitempty"`
	Scope                             string   `json:"scope,omitempty"`
}

func (m ClientMetadata) normalized() (ClientMetadata, error) {
	if err := m.validateBounds(); err != nil {
		return ClientMetadata{}, err
	}
	m.ClientID = strings.TrimSpace(m.ClientID)
	m.ClientName = truncateRunes(strings.TrimSpace(m.ClientName), 200)
	m.TokenEndpointAuthMethod = strings.TrimSpace(m.TokenEndpointAuthMethod)
	m.TokenEndpointAuthMethodsSupported = uniqueStrings(m.TokenEndpointAuthMethodsSupported)
	m.ApplicationType = strings.TrimSpace(m.ApplicationType)
	m.GrantTypes = uniqueStrings(m.GrantTypes)
	m.ResponseTypes = uniqueStrings(m.ResponseTypes)
	m.RedirectURIs = uniqueStrings(m.RedirectURIs)
	if len(m.RedirectURIs) == 0 {
		return ClientMetadata{}, fmt.Errorf("at least one redirect URI is required")
	}
	for _, redirect := range m.RedirectURIs {
		if !AllowedRedirectURI(redirect) {
			return ClientMetadata{}, fmt.Errorf("invalid redirect URI: %s", redirect)
		}
	}
	// ChatGPT's CIMD metadata can publish both the transition-era singular
	// preference and the standards-track plural capability list. CodexLink is a
	// public OAuth client server and supports only `none`; select it whenever it
	// appears in the capability intersection rather than treating a different
	// singular preference as binding.
	if len(m.TokenEndpointAuthMethodsSupported) > 0 {
		if !containsString(m.TokenEndpointAuthMethodsSupported, "none") {
			return ClientMetadata{}, fmt.Errorf("client does not support token endpoint authentication method none")
		}
		m.TokenEndpointAuthMethod = "none"
	} else {
		if m.TokenEndpointAuthMethod == "" {
			m.TokenEndpointAuthMethod = "none"
		}
		if m.TokenEndpointAuthMethod != "none" {
			return ClientMetadata{}, fmt.Errorf("unsupported token_endpoint_auth_method: %s", m.TokenEndpointAuthMethod)
		}
	}
	if len(m.GrantTypes) == 0 {
		m.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	if !containsString(m.GrantTypes, "authorization_code") {
		return ClientMetadata{}, fmt.Errorf("grant_types must include authorization_code")
	}
	for _, grant := range m.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			return ClientMetadata{}, fmt.Errorf("unsupported grant_type: %s", grant)
		}
	}
	if len(m.ResponseTypes) == 0 {
		m.ResponseTypes = []string{"code"}
	}
	if !containsString(m.ResponseTypes, "code") {
		return ClientMetadata{}, fmt.Errorf("response_types must include code")
	}
	for _, responseType := range m.ResponseTypes {
		if responseType != "code" {
			return ClientMetadata{}, fmt.Errorf("unsupported response_type: %s", responseType)
		}
	}
	if m.ApplicationType != "" && m.ApplicationType != "native" && m.ApplicationType != "web" {
		return ClientMetadata{}, fmt.Errorf("application_type must be native or web")
	}
	return m, nil
}

func (m ClientMetadata) validateBounds() error {
	uriFields := []struct {
		name  string
		value string
	}{
		{"client_id", m.ClientID},
		{"client_uri", m.ClientURI},
		{"logo_uri", m.LogoURI},
	}
	for _, field := range uriFields {
		if len(field.value) > maxClientMetadataURIBytes {
			return fmt.Errorf("%s exceeds %d bytes", field.name, maxClientMetadataURIBytes)
		}
	}
	if len(m.ClientName) > maxClientMetadataNameBytes {
		return fmt.Errorf("client_name exceeds %d bytes", maxClientMetadataNameBytes)
	}
	if len(m.Scope) > maxClientMetadataScopeBytes {
		return fmt.Errorf("scope exceeds %d bytes", maxClientMetadataScopeBytes)
	}
	if len(m.RedirectURIs) > maxClientMetadataRedirectURIs {
		return fmt.Errorf("redirect_uris exceeds %d entries", maxClientMetadataRedirectURIs)
	}
	for _, redirect := range m.RedirectURIs {
		if len(redirect) > maxClientMetadataURIBytes {
			return fmt.Errorf("redirect URI exceeds %d bytes", maxClientMetadataURIBytes)
		}
	}
	valueFields := []struct {
		name   string
		values []string
	}{
		{"grant_types", m.GrantTypes},
		{"response_types", m.ResponseTypes},
		{"token_endpoint_auth_methods_supported", m.TokenEndpointAuthMethodsSupported},
	}
	for _, field := range valueFields {
		if len(field.values) > maxClientMetadataValues {
			return fmt.Errorf("%s exceeds %d entries", field.name, maxClientMetadataValues)
		}
		for _, value := range field.values {
			if len(value) > maxClientMetadataValueBytes {
				return fmt.Errorf("%s contains a value longer than %d bytes", field.name, maxClientMetadataValueBytes)
			}
		}
	}
	scalarFields := []struct {
		name  string
		value string
	}{
		{"token_endpoint_auth_method", m.TokenEndpointAuthMethod},
		{"application_type", m.ApplicationType},
	}
	for _, field := range scalarFields {
		if len(field.value) > maxClientMetadataValueBytes {
			return fmt.Errorf("%s exceeds %d bytes", field.name, maxClientMetadataValueBytes)
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// ClientMetadataResolver resolves HTTPS client_id values used by OAuth Client
// ID Metadata Documents.
type ClientMetadataResolver interface {
	Resolve(context.Context, string) (Client, error)
}

type ClientMetadataResolverFunc func(context.Context, string) (Client, error)

func (f ClientMetadataResolverFunc) Resolve(ctx context.Context, clientID string) (Client, error) {
	return f(ctx, clientID)
}

type metadataCacheEntry struct {
	client    Client
	expiresAt time.Time
	lastUsed  time.Time
}

// HTTPClientMetadataResolver fetches client metadata with a deliberately
// restrictive network policy. It rejects redirects, non-HTTPS identifiers and
// destinations that resolve to local/private/reserved addresses.
type HTTPClientMetadataResolver struct {
	Resolver        *net.Resolver
	Now             func() time.Time
	Timeout         time.Duration
	CacheTTL        time.Duration
	MaxCacheEntries int

	mu    sync.Mutex
	cache map[string]metadataCacheEntry
}

func NewHTTPClientMetadataResolver() *HTTPClientMetadataResolver {
	return &HTTPClientMetadataResolver{
		Resolver:        net.DefaultResolver,
		Now:             time.Now,
		Timeout:         8 * time.Second,
		CacheTTL:        5 * time.Minute,
		MaxCacheEntries: 128,
		cache:           make(map[string]metadataCacheEntry),
	}
}

func (r *HTTPClientMetadataResolver) Resolve(ctx context.Context, clientID string) (Client, error) {
	parsed, err := parseMetadataClientID(clientID)
	if err != nil {
		return Client{}, err
	}
	r.mu.Lock()
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Resolver == nil {
		r.Resolver = net.DefaultResolver
	}
	if r.Timeout <= 0 {
		r.Timeout = 8 * time.Second
	}
	if r.CacheTTL <= 0 {
		r.CacheTTL = 5 * time.Minute
	}
	if r.MaxCacheEntries <= 0 {
		r.MaxCacheEntries = 128
	}
	if r.cache == nil {
		r.cache = make(map[string]metadataCacheEntry)
	}
	nowFunc, resolver := r.Now, r.Resolver
	timeout, cacheTTL, maxCacheEntries := r.Timeout, r.CacheTTL, r.MaxCacheEntries
	now := nowFunc()
	r.pruneCacheLocked(now)
	if cached, ok := r.cache[clientID]; ok {
		cached.lastUsed = now
		r.cache[clientID] = cached
		r.mu.Unlock()
		return cloneClient(cached.client), nil
	}
	r.mu.Unlock()

	addresses, err := resolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		if err == nil {
			err = errors.New("client metadata host has no addresses")
		}
		return Client{}, fmt.Errorf("resolve client metadata host: %w", err)
	}
	approved := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !publicMetadataIP(address.IP) {
			return Client{}, fmt.Errorf("client metadata host resolves to a non-public address")
		}
		approved = append(approved, append(net.IP(nil), address.IP...))
	}

	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       10 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	defer transport.CloseIdleConnections()
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 10 * time.Second}
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		_, port, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		var lastErr error
		for _, ip := range approved {
			connection, dialErr := dialer.DialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Client{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.ProductName+"/"+buildinfo.Version)
	response, err := client.Do(request)
	if err != nil {
		return Client{}, fmt.Errorf("fetch client metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Client{}, fmt.Errorf("client metadata returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, 256*1024+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Client{}, err
	}
	if len(body) > 256*1024 {
		return Client{}, fmt.Errorf("client metadata document is too large")
	}
	var metadata ClientMetadata
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&metadata); err != nil {
		return Client{}, fmt.Errorf("invalid client metadata JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Client{}, err
	}
	metadata, err = metadata.normalized()
	if err != nil {
		return Client{}, err
	}
	if metadata.ClientID != clientID {
		return Client{}, fmt.Errorf("client metadata client_id does not match document URL")
	}
	if metadata.ClientName == "" {
		return Client{}, fmt.Errorf("client metadata requires client_name")
	}
	resolved := Client{
		ID:                      clientID,
		Name:                    metadata.ClientName,
		RedirectURIs:            metadata.RedirectURIs,
		TokenEndpointAuthMethod: metadata.TokenEndpointAuthMethod,
		GrantTypes:              metadata.GrantTypes,
		ResponseTypes:           metadata.ResponseTypes,
		ApplicationType:         metadata.ApplicationType,
		CreatedAt:               nowFunc().UTC().Format(time.RFC3339Nano),
	}
	now = nowFunc()
	r.mu.Lock()
	r.pruneCacheLocked(now)
	if len(r.cache) >= maxCacheEntries {
		r.evictOldestLocked()
	}
	r.cache[clientID] = metadataCacheEntry{client: cloneClient(resolved), expiresAt: now.Add(cacheTTL), lastUsed: now}
	r.mu.Unlock()
	return cloneClient(resolved), nil
}

func (r *HTTPClientMetadataResolver) pruneCacheLocked(now time.Time) {
	for key, entry := range r.cache {
		if !now.Before(entry.expiresAt) {
			delete(r.cache, key)
		}
	}
}

func (r *HTTPClientMetadataResolver) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range r.cache {
		if oldestKey == "" || entry.lastUsed.Before(oldest) || (entry.lastUsed.Equal(oldest) && key < oldestKey) {
			oldestKey, oldest = key, entry.lastUsed
		}
	}
	if oldestKey != "" {
		delete(r.cache, oldestKey)
	}
}

func parseMetadataClientID(clientID string) (*url.URL, error) {
	clientID = strings.TrimSpace(clientID)
	if len(clientID) > maxClientMetadataURIBytes {
		return nil, fmt.Errorf("client_id exceeds %d bytes", maxClientMetadataURIBytes)
	}
	parsed, err := url.Parse(clientID)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("client_id is not a valid HTTPS metadata URL")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return nil, fmt.Errorf("client metadata URL requires a path")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port != 443 {
			return nil, fmt.Errorf("client metadata URL must use the default HTTPS port")
		}
	}
	return parsed, nil
}

func isMetadataClientID(clientID string) bool {
	_, err := parseMetadataClientID(clientID)
	return err == nil
}

var blockedMetadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicMetadataIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	for _, prefix := range blockedMetadataPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return address.IsGlobalUnicast()
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return fmt.Errorf("request contains more than one JSON value")
}

// RedirectURIMatches applies exact matching except for native loopback
// callbacks registered without a port, where RFC 8252 requires accepting the
// ephemeral port chosen by the client.
func RedirectURIMatches(registered []string, requested string) bool {
	for _, candidate := range registered {
		if candidate == requested || loopbackRedirectMatches(candidate, requested) {
			return true
		}
	}
	return false
}

func loopbackRedirectMatches(registered, requested string) bool {
	left, leftErr := url.Parse(registered)
	right, rightErr := url.Parse(requested)
	if leftErr != nil || rightErr != nil || left.Scheme != "http" || right.Scheme != "http" {
		return false
	}
	if left.User != nil || right.User != nil || left.Fragment != "" || right.Fragment != "" {
		return false
	}
	if left.Port() != "" || right.Port() == "" {
		return false
	}
	leftHost, rightHost := strings.ToLower(left.Hostname()), strings.ToLower(right.Hostname())
	if !isLoopbackRedirectHost(leftHost) || leftHost != rightHost {
		return false
	}
	return left.EscapedPath() == right.EscapedPath() && left.RawQuery == right.RawQuery
}

func isLoopbackRedirectHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
