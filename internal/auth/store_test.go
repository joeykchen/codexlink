package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePKCEResourceBindingRefreshRotationAndReplayRevocation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	file := filepath.Join(t.TempDir(), "auth.json")
	store, err := NewStore("ws-1", StoreOptions{File: file, Now: func() time.Time { return now }, AccessTokenTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterClient("bad", []string{"http://example.com/callback"}); err == nil {
		t.Fatal("insecure non-loopback redirect should fail")
	}
	if _, err := store.RegisterClient("userinfo", []string{"https://user@example.com/callback"}); err == nil {
		t.Fatal("redirect URI with userinfo should fail")
	}
	client, err := store.RegisterClient("Chat client", []string{"https://client.example/callback", "https://client.example/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.RedirectURIs) != 1 {
		t.Fatalf("redirects not deduplicated: %#v", client.RedirectURIs)
	}
	verifier := strings.Repeat("v", 64)
	audience := "https://bridge.example/mcp"
	code, err := store.CreateAuthorizationCode(AuthorizationCodeRequest{
		ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier),
		Scopes: []string{"workspace.read", "offline_access"}, PairingID: "pair", Audience: audience, RefreshAllowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience)
	if err != nil {
		t.Fatal(err)
	}
	if set.AccessToken == "" || set.RefreshToken == "" || set.ExpiresIn != 60 {
		t.Fatalf("unexpected token set: %#v", set)
	}
	principal, err := store.VerifyAccess(set.AccessToken, "https://BRIDGE.example/mcp/")
	if err != nil || principal.ClientID != client.ID || !HasScope(principal, "workspace.read") {
		t.Fatalf("verify failed: %#v %v", principal, err)
	}
	if _, err := store.VerifyAccess(set.AccessToken, "https://other.example/mcp"); err == nil || err.Error() != "wrong_audience" {
		t.Fatalf("wrong audience error = %v", err)
	}
	rotated, err := store.Refresh(set.RefreshToken, client.ID, audience)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == "" || rotated.RefreshToken == set.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// Rotation is persisted before replay handling.
	reloaded, err := NewStore("ws-1", StoreOptions{File: file, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.VerifyAccess(rotated.AccessToken, audience); err != nil {
		t.Fatalf("persisted token unavailable: %v", err)
	}
	if data, err := os.ReadFile(file); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), set.AccessToken) || strings.Contains(string(data), rotated.RefreshToken) {
		t.Fatal("raw token leaked into persisted state")
	}

	// Replaying the consumed refresh token revokes the entire token family.
	if _, err := store.Refresh(set.RefreshToken, client.ID, audience); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("old refresh should fail: %v", err)
	}
	if _, err := store.VerifyAccess(rotated.AccessToken, audience); err == nil {
		t.Fatal("rotated access token survived refresh replay")
	}
	if _, err := store.Refresh(rotated.RefreshToken, client.ID, audience); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("rotated refresh token survived family revocation: %v", err)
	}
	reloaded, err = NewStore("ws-1", StoreOptions{File: file, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.VerifyAccess(rotated.AccessToken, audience); err == nil {
		t.Fatal("family revocation was not persisted")
	}
	if count := store.TokenCount(); count != 0 {
		t.Fatalf("live token count = %d, want 0", count)
	}
}

func TestControlRespondScopeIsExplicitAndDoesNotUpgradeOldGrant(t *testing.T) {
	defaults, err := ParseScopes("")
	if err != nil || !containsScope(defaults, ScopeControlRespond) {
		t.Fatalf("defaults=%v err=%v", defaults, err)
	}
	old, err := ParseScopes("workspace.read offline_access")
	if err != nil || containsScope(old, ScopeControlRespond) {
		t.Fatalf("old scopes=%v err=%v", old, err)
	}
	store, err := NewStore("w", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json"), AccessTokenTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := store.RegisterClient("old", []string{"http://127.0.0.1/callback"})
	verifier := strings.Repeat("c", 64)
	audience := "https://example.test/mcp"
	code, _ := store.CreateAuthorizationCode(AuthorizationCodeRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier), Scopes: old, PairingID: "p", Audience: audience, RefreshAllowed: true})
	tokens, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience)
	if err != nil {
		t.Fatal(err)
	}
	if store.HasActiveScopeForAudience(audience, ScopeControlRespond) {
		t.Fatal("old grant gained control scope")
	}
	rotated, err := store.Refresh(tokens.RefreshToken, client.ID, audience)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.VerifyAccess(rotated.AccessToken, audience)
	if err != nil || containsScope(principal.Scopes, ScopeControlRespond) {
		t.Fatalf("refresh upgraded scopes: %+v err=%v", principal, err)
	}
}

func TestAuthorizationCodeIsSingleUseAndRedirectBound(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := store.RegisterClient("client", []string{"http://127.0.0.1/callback"})
	verifier := strings.Repeat("a", 64)
	request := AuthorizationCodeRequest{
		ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier),
		Scopes: []string{"workspace.read"}, PairingID: "pair", Audience: "http://127.0.0.1:1/mcp", RefreshAllowed: true,
	}
	code, _ := store.CreateAuthorizationCode(request)
	if _, err := store.ExchangeAuthorizationCode(code, client.ID, "", verifier, ""); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("missing redirect URI error = %v", err)
	}

	code, _ = store.CreateAuthorizationCode(request)
	if _, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, ""); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("second exchange error = %v", err)
	}
}

func TestClientWithoutRefreshGrantDoesNotReceiveRefreshToken(t *testing.T) {
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
	verifier := strings.Repeat("g", 64)
	code, err := store.CreateAuthorizationCode(AuthorizationCodeRequest{
		ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier),
		Scopes: []string{"workspace.read", "offline_access"}, Audience: "https://bridge.example/mcp", RefreshAllowed: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, "")
	if err != nil {
		t.Fatal(err)
	}
	if set.RefreshToken != "" {
		t.Fatal("refresh token issued to code-only client")
	}
}

func TestTokenCountsAndRevocationAreAudienceScoped(t *testing.T) {
	store, err := NewStore("workspace", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.RegisterClient("test", []string{"https://client.example/callback"})
	if err != nil {
		t.Fatal(err)
	}
	issue := func(audience string) {
		verifier := strings.Repeat("a", 43)
		code, err := store.CreateAuthorizationCode(AuthorizationCodeRequest{
			ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier),
			Scopes: []string{"workspace.read", "offline_access"}, PairingID: "pair", Audience: audience, RefreshAllowed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience); err != nil {
			t.Fatal(err)
		}
	}
	issue("https://one.example/mcp")
	issue("https://two.example/mcp")
	if got := store.TokenCountForAudience("https://one.example/mcp"); got != 2 {
		t.Fatalf("audience one count = %d", got)
	}
	if got := store.TokenCount(); got != 4 {
		t.Fatalf("total count = %d", got)
	}
	revoked, err := store.RevokeAudience("https://one.example/mcp")
	if err != nil || revoked != 2 {
		t.Fatalf("revoke = %d, %v", revoked, err)
	}
	if store.TokenCountForAudience("https://one.example/mcp") != 0 || store.TokenCountForAudience("https://two.example/mcp") != 2 {
		t.Fatal("audience revocation affected the wrong token family")
	}
}

func TestParseScopesRejectsPrivilegeEscalation(t *testing.T) {
	defaults, err := ParseScopes("")
	if err != nil {
		t.Fatal(err)
	}
	if containsScope(defaults, "offline_access") {
		t.Fatal("offline_access must be explicit")
	}
	if _, err := ParseScopes("unknown"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("unknown scope error = %v", err)
	}
	parsed, err := ParseScopes("workspace.read workspace.read git.read")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(parsed, " "); got != "workspace.read git.read" {
		t.Fatalf("deduplicated scopes = %q", got)
	}
}

func TestStoreBoundsClientsCodesAndStateFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "auth.json")
	store, err := NewStore("ws", StoreOptions{File: file, MaxClients: 2, MaxAuthorizationCodes: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.RegisterClient("one", []string{"https://one.example/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterClient("two", []string{"https://two.example/callback"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterClient("three", []string{"https://three.example/callback"}); err != nil {
		t.Fatal(err)
	}
	if store.ClientCount() != 2 {
		t.Fatalf("client count = %d", store.ClientCount())
	}
	if _, ok := store.Client(first.ID); ok {
		t.Fatal("oldest unused client was not evicted")
	}
	client, _ := store.RegisterClient("four", []string{"https://four.example/callback"})
	request := AuthorizationCodeRequest{ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(strings.Repeat("x", 64)), Scopes: []string{"workspace.read"}, Audience: "https://bridge.example/mcp"}
	if _, err := store.CreateAuthorizationCode(request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAuthorizationCode(request); !errors.Is(err, ErrCapacity) {
		t.Fatalf("authorization code capacity error = %v", err)
	}

	oversized := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, 1025), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore("ws", StoreOptions{File: oversized, MaxStateBytes: 1024}); err == nil {
		t.Fatal("oversized state was accepted")
	}
}

func TestLegacyRefreshTokenStateRemainsUsable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	file := filepath.Join(t.TempDir(), "auth.json")
	refresh := "legacy-refresh"
	state := persistedState{
		Clients: []Client{{
			ID: "legacy-client", RedirectURIs: []string{"https://client.example/callback"},
			GrantTypes: []string{"authorization_code", "refresh_token"}, CreatedAt: now.UTC().Format(time.RFC3339),
		}},
		Tokens: []TokenRecord{{
			Hash: hashValue(refresh), Kind: "refresh", ClientID: "legacy-client", WorkspaceID: "ws",
			Audience: "https://bridge.example/mcp", Scopes: []string{"workspace.read", "offline_access"},
			FamilyID: "legacy-family", IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(),
		}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore("ws", StoreOptions{File: file, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.Refresh(refresh, "legacy-client", "https://bridge.example/mcp")
	if err != nil {
		t.Fatalf("legacy refresh token was not migrated: %v", err)
	}
	if set.RefreshToken == "" {
		t.Fatal("migrated refresh session did not rotate")
	}
}

func TestRevokeInvalidatesTheWholeTokenFamily(t *testing.T) {
	file := filepath.Join(t.TempDir(), "auth.json")
	store, err := NewStore("ws", StoreOptions{File: file})
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.RegisterClient("client", []string{"https://client.example/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("r", 64)
	audience := "https://bridge.example/mcp"
	code, err := store.CreateAuthorizationCode(AuthorizationCodeRequest{
		ClientID: client.ID, RedirectURI: client.RedirectURIs[0], CodeChallenge: PKCEChallenge(verifier),
		Scopes: []string{"workspace.read", "offline_access"}, Audience: audience, RefreshAllowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, audience)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(set.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyAccess(set.AccessToken, audience); err == nil {
		t.Fatal("revoked access token remained valid")
	}
	if _, err := store.Refresh(set.RefreshToken, client.ID, audience); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("related refresh token remained valid: %v", err)
	}
	if count := store.TokenCount(); count != 0 {
		t.Fatalf("token family still has %d records", count)
	}
	reloaded, err := NewStore("ws", StoreOptions{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if count := reloaded.TokenCount(); count != 0 {
		t.Fatalf("family revocation was not persisted: %d records", count)
	}
}

func TestClientLookupReturnsDefensiveCopy(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := store.RegisterClientMetadata(ClientMetadata{
		ClientName: "test", RedirectURIs: []string{"https://example.com/callback"},
		GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, ok := store.Client(registered.ID)
	if !ok {
		t.Fatal("registered client not found")
	}
	first.RedirectURIs[0] = "https://attacker.example/callback"
	first.GrantTypes[0] = "attacker"
	second, ok := store.Client(registered.ID)
	if !ok || second.RedirectURIs[0] != "https://example.com/callback" || second.GrantTypes[0] != "authorization_code" {
		t.Fatalf("stored client was mutated through a returned value: %+v", second)
	}
}
