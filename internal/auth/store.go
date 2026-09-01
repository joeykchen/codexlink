package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/config"
	statefs "github.com/joeykchen/codexlink/internal/state"
)

var (
	ErrInvalidGrant  = errors.New("invalid_grant")
	ErrInvalidClient = errors.New("invalid_client")
	ErrInvalidTarget = errors.New("invalid_target")
	ErrInvalidScope  = errors.New("invalid_scope")
	ErrCapacity      = errors.New("temporarily_unavailable")
)

var SupportedScopes = []string{
	"workspace.read",
	"workspace.search",
	"git.read",
	"execution.read",
	"offline_access",
}

// DefaultScopes deliberately excludes offline_access. A client receives a
// refresh token only when it explicitly requests that scope and declares the
// refresh_token grant.
var DefaultScopes = []string{
	"workspace.read",
	"workspace.search",
	"git.read",
	"execution.read",
}

const (
	defaultMaxClients            = 256
	defaultMaxTokens             = 4096
	defaultMaxAuthorizationCodes = 256
	defaultMaxRefreshTombstones  = 4096
	defaultMaxStateBytes         = int64(8 << 20)
)

type Client struct {
	ID                      string   `json:"clientId"`
	Name                    string   `json:"clientName,omitempty"`
	RedirectURIs            []string `json:"redirectUris"`
	TokenEndpointAuthMethod string   `json:"tokenEndpointAuthMethod,omitempty"`
	GrantTypes              []string `json:"grantTypes,omitempty"`
	ResponseTypes           []string `json:"responseTypes,omitempty"`
	ApplicationType         string   `json:"applicationType,omitempty"`
	CreatedAt               string   `json:"createdAt"`
}

type TokenRecord struct {
	Hash           string   `json:"hash"`
	Kind           string   `json:"kind"`
	ClientID       string   `json:"clientId"`
	WorkspaceID    string   `json:"workspaceId"`
	Audience       string   `json:"audience"`
	Scopes         []string `json:"scopes"`
	FamilyID       string   `json:"familyId,omitempty"`
	RefreshAllowed bool     `json:"refreshAllowed,omitempty"`
	IssuedAt       int64    `json:"issuedAt"`
	ExpiresAt      int64    `json:"expiresAt"`
}

type refreshTombstone struct {
	Hash      string `json:"hash"`
	FamilyID  string `json:"familyId"`
	Audience  string `json:"audience"`
	ExpiresAt int64  `json:"expiresAt"`
}

type authorizationCode struct {
	Hash           string
	ClientID       string
	RedirectURI    string
	CodeChallenge  string
	Scopes         []string
	WorkspaceID    string
	PairingID      string
	Audience       string
	RefreshAllowed bool
	ExpiresAt      time.Time
}

type AuthorizationCodeRequest struct {
	ClientID       string
	RedirectURI    string
	CodeChallenge  string
	Scopes         []string
	PairingID      string
	Audience       string
	RefreshAllowed bool
}

type persistedState struct {
	Clients           []Client           `json:"clients"`
	Tokens            []TokenRecord      `json:"tokens"`
	RefreshTombstones []refreshTombstone `json:"refreshTombstones,omitempty"`
}

type StoreOptions struct {
	File                  string
	Now                   func() time.Time
	AccessTokenTTL        time.Duration
	RefreshTokenTTL       time.Duration
	AuthCodeTTL           time.Duration
	MaxClients            int
	MaxTokens             int
	MaxAuthorizationCodes int
	MaxRefreshTombstones  int
	MaxStateBytes         int64
}

type Store struct {
	mu                    sync.RWMutex
	workspaceID           string
	file                  string
	now                   func() time.Time
	accessTokenTTL        time.Duration
	refreshTokenTTL       time.Duration
	authCodeTTL           time.Duration
	maxClients            int
	maxTokens             int
	maxAuthorizationCodes int
	maxRefreshTombstones  int
	maxStateBytes         int64
	clients               map[string]Client
	tokens                map[string]TokenRecord
	codes                 map[string]authorizationCode
	refreshTombstones     map[string]refreshTombstone
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scopes       []string
}

type Principal struct {
	Token       string
	ClientID    string
	WorkspaceID string
	Audience    string
	Scopes      []string
	ExpiresAt   time.Time
}

func NewStore(workspaceID string, options StoreOptions) (*Store, error) {
	applyStoreDefaults(workspaceID, &options)
	if info, err := os.Stat(options.File); err == nil && info.Size() > options.MaxStateBytes {
		return nil, fmt.Errorf("auth state exceeds %d bytes", options.MaxStateBytes)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	store := &Store{
		workspaceID:           workspaceID,
		file:                  options.File,
		now:                   options.Now,
		accessTokenTTL:        options.AccessTokenTTL,
		refreshTokenTTL:       options.RefreshTokenTTL,
		authCodeTTL:           options.AuthCodeTTL,
		maxClients:            options.MaxClients,
		maxTokens:             options.MaxTokens,
		maxAuthorizationCodes: options.MaxAuthorizationCodes,
		maxRefreshTombstones:  options.MaxRefreshTombstones,
		maxStateBytes:         options.MaxStateBytes,
		clients:               make(map[string]Client),
		tokens:                make(map[string]TokenRecord),
		codes:                 make(map[string]authorizationCode),
		refreshTombstones:     make(map[string]refreshTombstone),
	}
	var state persistedState
	found, err := statefs.ReadJSONFile(options.File, &state)
	if err != nil {
		return nil, err
	}
	if !found {
		return store, nil
	}
	if len(state.Clients) > store.maxClients || len(state.Tokens) > store.maxTokens || len(state.RefreshTombstones) > store.maxRefreshTombstones {
		return nil, fmt.Errorf("auth state exceeds configured capacity")
	}
	now := options.Now().UnixMilli()
	for _, client := range state.Clients {
		store.clients[client.ID] = client
	}
	for _, token := range state.Tokens {
		if token.ExpiresAt <= now {
			continue
		}
		// Version 1.0 did not persist RefreshAllowed. Preserve existing
		// refresh sessions while applying the stricter rule to new tokens.
		if token.Kind == "refresh" && !token.RefreshAllowed && containsScope(token.Scopes, "offline_access") {
			token.RefreshAllowed = true
		}
		store.tokens[token.Hash] = token
	}
	for _, tombstone := range state.RefreshTombstones {
		if tombstone.ExpiresAt > now {
			store.refreshTombstones[tombstone.Hash] = tombstone
		}
	}
	return store, nil
}

func applyStoreDefaults(workspaceID string, options *StoreOptions) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.AccessTokenTTL <= 0 {
		options.AccessTokenTTL = time.Hour
	}
	if options.RefreshTokenTTL <= 0 {
		options.RefreshTokenTTL = 30 * 24 * time.Hour
	}
	if options.AuthCodeTTL <= 0 {
		options.AuthCodeTTL = 5 * time.Minute
	}
	if options.MaxClients <= 0 {
		options.MaxClients = defaultMaxClients
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = defaultMaxTokens
	}
	if options.MaxAuthorizationCodes <= 0 {
		options.MaxAuthorizationCodes = defaultMaxAuthorizationCodes
	}
	if options.MaxRefreshTombstones <= 0 {
		options.MaxRefreshTombstones = defaultMaxRefreshTombstones
	}
	if options.MaxStateBytes <= 0 {
		options.MaxStateBytes = defaultMaxStateBytes
	}
	if options.File == "" {
		options.File = filepath.Join(config.StateDir(), "auth", workspaceID+".json")
	}
}

func hashValue(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func randomToken(prefix string, bytesCount int) (string, error) {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}

func PKCEChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func safeEqual(left, right string) bool {
	leftBytes, rightBytes := []byte(left), []byte(right)
	if len(leftBytes) != len(rightBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}

func (s *Store) pruneLocked() {
	now := s.now()
	nowMillis := now.UnixMilli()
	for hash, token := range s.tokens {
		if token.ExpiresAt <= nowMillis {
			delete(s.tokens, hash)
		}
	}
	for hash, tombstone := range s.refreshTombstones {
		if tombstone.ExpiresAt <= nowMillis {
			delete(s.refreshTombstones, hash)
		}
	}
	for hash, code := range s.codes {
		if !now.Before(code.ExpiresAt) {
			delete(s.codes, hash)
		}
	}
}

func (s *Store) saveLocked() error {
	s.pruneLocked()
	state := persistedState{}
	for _, client := range s.clients {
		state.Clients = append(state.Clients, client)
	}
	for _, token := range s.tokens {
		state.Tokens = append(state.Tokens, token)
	}
	for _, tombstone := range s.refreshTombstones {
		state.RefreshTombstones = append(state.RefreshTombstones, tombstone)
	}
	sort.Slice(state.Clients, func(i, j int) bool { return state.Clients[i].ID < state.Clients[j].ID })
	sort.Slice(state.Tokens, func(i, j int) bool { return state.Tokens[i].Hash < state.Tokens[j].Hash })
	sort.Slice(state.RefreshTombstones, func(i, j int) bool { return state.RefreshTombstones[i].Hash < state.RefreshTombstones[j].Hash })
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if int64(len(data)) > s.maxStateBytes {
		return fmt.Errorf("%w: auth state exceeds %d bytes", ErrCapacity, s.maxStateBytes)
	}
	return statefs.WriteFileAtomic(s.file, data)
}

func (s *Store) RegisterClient(name string, redirects []string) (Client, error) {
	return s.RegisterClientMetadata(ClientMetadata{ClientName: name, RedirectURIs: redirects})
}

func (s *Store) RegisterClientMetadata(metadata ClientMetadata) (Client, error) {
	metadata, err := metadata.normalized()
	if err != nil {
		return Client{}, err
	}
	id, err := randomToken("cl_client", 18)
	if err != nil {
		return Client{}, err
	}
	client := Client{
		ID:                      id,
		Name:                    metadata.ClientName,
		RedirectURIs:            metadata.RedirectURIs,
		TokenEndpointAuthMethod: metadata.TokenEndpointAuthMethod,
		GrantTypes:              metadata.GrantTypes,
		ResponseTypes:           metadata.ResponseTypes,
		ApplicationType:         metadata.ApplicationType,
		CreatedAt:               s.now().UTC().Format(time.RFC3339Nano),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	previous := cloneClients(s.clients)
	s.makeClientRoomLocked()
	if len(s.clients) >= s.maxClients {
		return Client{}, fmt.Errorf("%w: OAuth client capacity reached", ErrCapacity)
	}
	s.clients[id] = client
	if err := s.saveLocked(); err != nil {
		s.clients = previous
		return Client{}, err
	}
	return client, nil
}

func cloneClients(source map[string]Client) map[string]Client {
	clone := make(map[string]Client, len(source))
	for id, client := range source {
		clone[id] = cloneClient(client)
	}
	return clone
}

func (s *Store) makeClientRoomLocked() {
	if len(s.clients) < s.maxClients {
		return
	}
	active := make(map[string]struct{})
	for _, token := range s.tokens {
		active[token.ClientID] = struct{}{}
	}
	for _, code := range s.codes {
		active[code.ClientID] = struct{}{}
	}
	candidates := make([]Client, 0, len(s.clients))
	for _, client := range s.clients {
		if _, ok := active[client.ID]; !ok {
			candidates = append(candidates, client)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt == candidates[j].CreatedAt {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].CreatedAt < candidates[j].CreatedAt
	})
	for _, client := range candidates {
		if len(s.clients) < s.maxClients {
			break
		}
		delete(s.clients, client.ID)
	}
}

func (s *Store) Client(id string) (Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[id]
	return cloneClient(client), ok
}

func cloneClient(client Client) Client {
	client.RedirectURIs = append([]string(nil), client.RedirectURIs...)
	client.GrantTypes = append([]string(nil), client.GrantTypes...)
	client.ResponseTypes = append([]string(nil), client.ResponseTypes...)
	return client
}

func (s *Store) CreateAuthorizationCode(request AuthorizationCodeRequest) (string, error) {
	code, err := randomToken("cl_ac", 32)
	if err != nil {
		return "", err
	}
	record := authorizationCode{
		Hash:           hashValue(code),
		ClientID:       request.ClientID,
		RedirectURI:    request.RedirectURI,
		CodeChallenge:  request.CodeChallenge,
		Scopes:         append([]string(nil), request.Scopes...),
		WorkspaceID:    s.workspaceID,
		PairingID:      request.PairingID,
		Audience:       request.Audience,
		RefreshAllowed: request.RefreshAllowed,
		ExpiresAt:      s.now().Add(s.authCodeTTL),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.codes) >= s.maxAuthorizationCodes {
		return "", fmt.Errorf("%w: authorization code capacity reached", ErrCapacity)
	}
	s.codes[record.Hash] = record
	return code, nil
}

func (s *Store) ExchangeAuthorizationCode(code, clientID, redirectURI, verifier, audience string) (TokenSet, error) {
	hash := hashValue(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	record, ok := s.codes[hash]
	delete(s.codes, hash)
	if !ok || !s.now().Before(record.ExpiresAt) {
		return TokenSet{}, ErrInvalidGrant
	}
	if redirectURI == "" || record.ClientID != clientID || record.RedirectURI != redirectURI {
		return TokenSet{}, ErrInvalidGrant
	}
	if audience != "" && !sameResource(audience, record.Audience) {
		return TokenSet{}, ErrInvalidTarget
	}
	if !validVerifier(verifier) || !safeEqual(PKCEChallenge(verifier), record.CodeChallenge) {
		return TokenSet{}, ErrInvalidGrant
	}
	return s.issueTokensLocked(clientID, record.Scopes, record.Audience, "", record.RefreshAllowed)
}

func (s *Store) issueTokensLocked(clientID string, scopes []string, audience, familyID string, refreshAllowed bool) (TokenSet, error) {
	s.pruneLocked()
	issueRefresh := refreshAllowed && containsScope(scopes, "offline_access")
	needed := 1
	if issueRefresh {
		needed++
	}
	if len(s.tokens)+needed > s.maxTokens {
		return TokenSet{}, fmt.Errorf("%w: token capacity reached", ErrCapacity)
	}
	now := s.now()
	access, err := randomToken("cl_at", 32)
	if err != nil {
		return TokenSet{}, err
	}
	if familyID == "" {
		familyID, err = randomToken("family", 12)
		if err != nil {
			return TokenSet{}, err
		}
	}
	accessRecord := TokenRecord{
		Hash:           hashValue(access),
		Kind:           "access",
		ClientID:       clientID,
		WorkspaceID:    s.workspaceID,
		Audience:       audience,
		Scopes:         append([]string(nil), scopes...),
		FamilyID:       familyID,
		RefreshAllowed: refreshAllowed,
		IssuedAt:       now.UnixMilli(),
		ExpiresAt:      now.Add(s.accessTokenTTL).UnixMilli(),
	}
	s.tokens[accessRecord.Hash] = accessRecord
	refresh := ""
	refreshHash := ""
	if issueRefresh {
		refresh, err = randomToken("cl_rt", 32)
		if err != nil {
			delete(s.tokens, accessRecord.Hash)
			return TokenSet{}, err
		}
		refreshHash = hashValue(refresh)
		s.tokens[refreshHash] = TokenRecord{
			Hash:           refreshHash,
			Kind:           "refresh",
			ClientID:       clientID,
			WorkspaceID:    s.workspaceID,
			Audience:       audience,
			Scopes:         append([]string(nil), scopes...),
			FamilyID:       familyID,
			RefreshAllowed: true,
			IssuedAt:       now.UnixMilli(),
			ExpiresAt:      now.Add(s.refreshTokenTTL).UnixMilli(),
		}
	}
	if err := s.saveLocked(); err != nil {
		delete(s.tokens, accessRecord.Hash)
		if refreshHash != "" {
			delete(s.tokens, refreshHash)
		}
		return TokenSet{}, err
	}
	return TokenSet{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(s.accessTokenTTL.Seconds()),
		Scopes:       append([]string(nil), scopes...),
	}, nil
}

func (s *Store) Refresh(refreshToken, clientID, audience string) (TokenSet, error) {
	hash := hashValue(refreshToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	record, ok := s.tokens[hash]
	if !ok {
		if tombstone, replay := s.refreshTombstones[hash]; replay {
			s.revokeFamilyLocked(tombstone.FamilyID)
			if err := s.saveLocked(); err != nil {
				return TokenSet{}, err
			}
		}
		return TokenSet{}, ErrInvalidGrant
	}
	if record.Kind != "refresh" || !record.RefreshAllowed || record.ExpiresAt <= s.now().UnixMilli() {
		return TokenSet{}, ErrInvalidGrant
	}
	if record.ClientID != clientID {
		return TokenSet{}, ErrInvalidClient
	}
	if client, registered := s.clients[clientID]; registered && !containsString(client.GrantTypes, "refresh_token") {
		return TokenSet{}, ErrInvalidGrant
	}
	if audience != "" && !sameResource(audience, record.Audience) {
		return TokenSet{}, ErrInvalidTarget
	}
	if len(s.refreshTombstones) >= s.maxRefreshTombstones {
		return TokenSet{}, fmt.Errorf("%w: refresh replay history capacity reached", ErrCapacity)
	}
	delete(s.tokens, hash)
	s.refreshTombstones[hash] = refreshTombstone{
		Hash:      hash,
		FamilyID:  record.FamilyID,
		Audience:  record.Audience,
		ExpiresAt: record.ExpiresAt,
	}
	set, err := s.issueTokensLocked(clientID, record.Scopes, record.Audience, record.FamilyID, true)
	if err != nil {
		delete(s.refreshTombstones, hash)
		s.tokens[hash] = record
		return TokenSet{}, err
	}
	return set, nil
}

func (s *Store) revokeFamilyLocked(familyID string) {
	if familyID == "" {
		return
	}
	for hash, token := range s.tokens {
		if token.FamilyID == familyID {
			delete(s.tokens, hash)
		}
	}
}

func (s *Store) VerifyAccess(token, audience string) (Principal, error) {
	s.mu.RLock()
	record, ok := s.tokens[hashValue(token)]
	s.mu.RUnlock()
	if !ok || record.Kind != "access" {
		return Principal{}, errors.New("unknown")
	}
	if record.ExpiresAt <= s.now().UnixMilli() {
		return Principal{}, errors.New("expired")
	}
	if record.WorkspaceID != s.workspaceID {
		return Principal{}, errors.New("wrong_workspace")
	}
	if audience != "" && !sameResource(audience, record.Audience) {
		return Principal{}, errors.New("wrong_audience")
	}
	return Principal{
		Token:       token,
		ClientID:    record.ClientID,
		WorkspaceID: record.WorkspaceID,
		Audience:    record.Audience,
		Scopes:      append([]string(nil), record.Scopes...),
		ExpiresAt:   time.UnixMilli(record.ExpiresAt),
	}, nil
}

func (s *Store) Revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := hashValue(token)
	if record, ok := s.tokens[hash]; ok {
		if record.FamilyID == "" {
			delete(s.tokens, hash)
		} else {
			s.revokeFamilyLocked(record.FamilyID)
		}
	} else if tombstone, ok := s.refreshTombstones[hash]; ok {
		s.revokeFamilyLocked(tombstone.FamilyID)
	}
	return s.saveLocked()
}

func (s *Store) RevokeAll() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.tokens)
	s.tokens = make(map[string]TokenRecord)
	s.codes = make(map[string]authorizationCode)
	s.refreshTombstones = make(map[string]refreshTombstone)
	return count, s.saveLocked()
}

func (s *Store) TokenCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now().UnixMilli()
	count := 0
	for _, token := range s.tokens {
		if token.ExpiresAt > now {
			count++
		}
	}
	return count
}

func (s *Store) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// TokenCountForAudience returns active access and refresh records bound to one
// canonical MCP resource. It is used by idempotent setup so tokens issued for
// an expired tunnel URL cannot make a new endpoint appear connected.
func (s *Store) TokenCountForAudience(audience string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now().UnixMilli()
	count := 0
	for _, token := range s.tokens {
		if token.ExpiresAt > now && sameResource(token.Audience, audience) {
			count++
		}
	}
	return count
}

func (s *Store) RevokeAudience(audience string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for hash, token := range s.tokens {
		if sameResource(token.Audience, audience) {
			delete(s.tokens, hash)
			count++
		}
	}
	for hash, tombstone := range s.refreshTombstones {
		if sameResource(tombstone.Audience, audience) {
			delete(s.refreshTombstones, hash)
		}
	}
	return count, s.saveLocked()
}

func ParseScopes(requested string) ([]string, error) {
	if strings.TrimSpace(requested) == "" {
		return append([]string(nil), DefaultScopes...), nil
	}
	allowed := make(map[string]struct{}, len(SupportedScopes))
	for _, scope := range SupportedScopes {
		allowed[scope] = struct{}{}
	}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, scope := range strings.Fields(strings.ReplaceAll(requested, "+", " ")) {
		if _, ok := allowed[scope]; !ok {
			return nil, fmt.Errorf("%w: unsupported scope %q", ErrInvalidScope, scope)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: no supported scope requested", ErrInvalidScope)
	}
	return result, nil
}

func containsScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func HasScope(principal Principal, scope string) bool { return containsScope(principal.Scopes, scope) }

func AllowedRedirectURI(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Fragment != "" || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func CanonicalResource(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("invalid resource URI")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	parsed.RawQuery = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func sameResource(left, right string) bool {
	l, lErr := CanonicalResource(left)
	r, rErr := CanonicalResource(right)
	return lErr == nil && rErr == nil && l == r
}

func validVerifier(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("-._~", r) {
			return false
		}
	}
	return true
}

func ValidChallenge(value string) bool {
	if len(value) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
