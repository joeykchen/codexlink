package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/config"
	statefs "github.com/joeykchen/codexlink/internal/state"
)

var SupportedScopes = []string{
	"workspace.read",
	"workspace.search",
	"git.read",
	"execution.read",
	"offline_access",
}

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
	Hash        string   `json:"hash"`
	Kind        string   `json:"kind"`
	ClientID    string   `json:"clientId"`
	WorkspaceID string   `json:"workspaceId"`
	Audience    string   `json:"audience"`
	Scopes      []string `json:"scopes"`
	FamilyID    string   `json:"familyId,omitempty"`
	IssuedAt    int64    `json:"issuedAt"`
	ExpiresAt   int64    `json:"expiresAt"`
}

type authorizationCode struct {
	Hash          string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Scopes        []string
	WorkspaceID   string
	PairingID     string
	Audience      string
	ExpiresAt     time.Time
}

type persistedState struct {
	Clients []Client      `json:"clients"`
	Tokens  []TokenRecord `json:"tokens"`
}

type StoreOptions struct {
	File            string
	Now             func() time.Time
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	AuthCodeTTL     time.Duration
}

type Store struct {
	mu              sync.RWMutex
	workspaceID     string
	file            string
	now             func() time.Time
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
	authCodeTTL     time.Duration
	clients         map[string]Client
	tokens          map[string]TokenRecord
	codes           map[string]authorizationCode
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
	if options.File == "" {
		options.File = filepath.Join(config.StateDir(), "auth", workspaceID+".json")
	}
	store := &Store{
		workspaceID: workspaceID, file: options.File, now: options.Now,
		accessTokenTTL: options.AccessTokenTTL, refreshTokenTTL: options.RefreshTokenTTL, authCodeTTL: options.AuthCodeTTL,
		clients: make(map[string]Client), tokens: make(map[string]TokenRecord), codes: make(map[string]authorizationCode),
	}
	var state persistedState
	found, err := statefs.ReadJSONFile(options.File, &state)
	if err != nil {
		return nil, err
	}
	if found {
		now := options.Now().UnixMilli()
		for _, client := range state.Clients {
			store.clients[client.ID] = client
		}
		for _, token := range state.Tokens {
			if token.ExpiresAt > now {
				store.tokens[token.Hash] = token
			}
		}
	}
	return store, nil
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

func (s *Store) saveLocked() error {
	now := s.now().UnixMilli()
	state := persistedState{}
	for _, client := range s.clients {
		state.Clients = append(state.Clients, client)
	}
	for hash, token := range s.tokens {
		if token.ExpiresAt > now {
			state.Tokens = append(state.Tokens, token)
		} else {
			delete(s.tokens, hash)
		}
	}
	sort.Slice(state.Clients, func(i, j int) bool { return state.Clients[i].ID < state.Clients[j].ID })
	sort.Slice(state.Tokens, func(i, j int) bool { return state.Tokens[i].Hash < state.Tokens[j].Hash })
	return statefs.WriteJSONFile(s.file, state)
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
		ID: id, Name: metadata.ClientName, RedirectURIs: metadata.RedirectURIs,
		TokenEndpointAuthMethod: metadata.TokenEndpointAuthMethod,
		GrantTypes:              metadata.GrantTypes, ResponseTypes: metadata.ResponseTypes,
		ApplicationType: metadata.ApplicationType,
		CreatedAt:       s.now().UTC().Format(time.RFC3339Nano),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = client
	if err := s.saveLocked(); err != nil {
		delete(s.clients, id)
		return Client{}, err
	}
	return client, nil
}

func (s *Store) Client(id string) (Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[id]
	return client, ok
}

func (s *Store) CreateAuthorizationCode(clientID, redirectURI, challenge string, scopes []string, pairingID, audience string) (string, error) {
	code, err := randomToken("cl_ac", 32)
	if err != nil {
		return "", err
	}
	record := authorizationCode{
		Hash: hashValue(code), ClientID: clientID, RedirectURI: redirectURI, CodeChallenge: challenge,
		Scopes: append([]string(nil), scopes...), WorkspaceID: s.workspaceID, PairingID: pairingID,
		Audience: audience, ExpiresAt: s.now().Add(s.authCodeTTL),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[record.Hash] = record
	return code, nil
}

func (s *Store) ExchangeAuthorizationCode(code, clientID, redirectURI, verifier, audience string) (TokenSet, error) {
	hash := hashValue(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.codes[hash]
	delete(s.codes, hash)
	if !ok || s.now().After(record.ExpiresAt) {
		return TokenSet{}, errors.New("invalid_grant")
	}
	if record.ClientID != clientID || (redirectURI != "" && record.RedirectURI != redirectURI) {
		return TokenSet{}, errors.New("invalid_grant")
	}
	if audience != "" && !sameResource(audience, record.Audience) {
		return TokenSet{}, errors.New("invalid_target")
	}
	if !validVerifier(verifier) || !safeEqual(PKCEChallenge(verifier), record.CodeChallenge) {
		return TokenSet{}, errors.New("invalid_grant")
	}
	return s.issueTokensLocked(clientID, record.Scopes, record.Audience, "")
}

func (s *Store) issueTokensLocked(clientID string, scopes []string, audience, familyID string) (TokenSet, error) {
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
		Hash: hashValue(access), Kind: "access", ClientID: clientID, WorkspaceID: s.workspaceID,
		Audience: audience, Scopes: append([]string(nil), scopes...), FamilyID: familyID,
		IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(s.accessTokenTTL).UnixMilli(),
	}
	s.tokens[accessRecord.Hash] = accessRecord
	refresh := ""
	if containsScope(scopes, "offline_access") {
		refresh, err = randomToken("cl_rt", 32)
		if err != nil {
			delete(s.tokens, accessRecord.Hash)
			return TokenSet{}, err
		}
		refreshRecord := TokenRecord{
			Hash: hashValue(refresh), Kind: "refresh", ClientID: clientID, WorkspaceID: s.workspaceID,
			Audience: audience, Scopes: append([]string(nil), scopes...), FamilyID: familyID,
			IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(s.refreshTokenTTL).UnixMilli(),
		}
		s.tokens[refreshRecord.Hash] = refreshRecord
	}
	if err := s.saveLocked(); err != nil {
		delete(s.tokens, accessRecord.Hash)
		if refresh != "" {
			delete(s.tokens, hashValue(refresh))
		}
		return TokenSet{}, err
	}
	return TokenSet{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(s.accessTokenTTL.Seconds()), Scopes: append([]string(nil), scopes...)}, nil
}

func (s *Store) Refresh(refreshToken, clientID, audience string) (TokenSet, error) {
	hash := hashValue(refreshToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.tokens[hash]
	if !ok || record.Kind != "refresh" || record.ExpiresAt <= s.now().UnixMilli() {
		return TokenSet{}, errors.New("invalid_grant")
	}
	if record.ClientID != clientID {
		return TokenSet{}, errors.New("invalid_client")
	}
	if audience != "" && !sameResource(audience, record.Audience) {
		return TokenSet{}, errors.New("invalid_target")
	}
	delete(s.tokens, hash)
	set, err := s.issueTokensLocked(clientID, record.Scopes, record.Audience, record.FamilyID)
	if err != nil {
		s.tokens[hash] = record
		return TokenSet{}, err
	}
	return set, nil
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
	return Principal{Token: token, ClientID: record.ClientID, WorkspaceID: record.WorkspaceID, Audience: record.Audience, Scopes: append([]string(nil), record.Scopes...), ExpiresAt: time.UnixMilli(record.ExpiresAt)}, nil
}

func (s *Store) Revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, hashValue(token))
	return s.saveLocked()
}

func (s *Store) RevokeAll() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.tokens)
	s.tokens = make(map[string]TokenRecord)
	s.codes = make(map[string]authorizationCode)
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
	return count, s.saveLocked()
}

func FilterScopes(requested string) []string {
	if strings.TrimSpace(requested) == "" {
		return append([]string(nil), SupportedScopes...)
	}
	allowed := make(map[string]bool, len(SupportedScopes))
	for _, scope := range SupportedScopes {
		allowed[scope] = true
	}
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, scope := range strings.Fields(strings.ReplaceAll(requested, "+", " ")) {
		if allowed[scope] && !seen[scope] {
			seen[scope] = true
			result = append(result, scope)
		}
	}
	if len(result) == 0 {
		return append([]string(nil), SupportedScopes...)
	}
	return result
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
