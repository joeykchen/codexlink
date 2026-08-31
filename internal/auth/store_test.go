package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePKCEResourceBindingRefreshRotationAndPersistence(t *testing.T) {
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
	code, err := store.CreateAuthorizationCode(client.ID, client.RedirectURIs[0], PKCEChallenge(verifier), []string{"workspace.read", "offline_access"}, "pair", audience)
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
	if _, err := store.Refresh(set.RefreshToken, client.ID, audience); err == nil || err.Error() != "invalid_grant" {
		t.Fatalf("old refresh should fail: %v", err)
	}
	if data, err := os.ReadFile(file); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), set.AccessToken) || strings.Contains(string(data), rotated.RefreshToken) {
		t.Fatal("raw token leaked into persisted state")
	}
	reloaded, err := NewStore("ws-1", StoreOptions{File: file, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.VerifyAccess(rotated.AccessToken, audience); err != nil {
		t.Fatalf("persisted token unavailable: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := store.VerifyAccess(rotated.AccessToken, audience); err == nil || err.Error() != "expired" {
		t.Fatalf("expired token error = %v", err)
	}
	if count := store.TokenCount(); count != 1 {
		// The access token has expired, while the rotated refresh token remains
		// live and is enough for an existing client to reconnect.
		t.Fatalf("live token count = %d, want 1", count)
	}
}

func TestAuthorizationCodeIsSingleUse(t *testing.T) {
	store, err := NewStore("ws", StoreOptions{File: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := store.RegisterClient("client", []string{"http://127.0.0.1/callback"})
	verifier := strings.Repeat("a", 64)
	code, _ := store.CreateAuthorizationCode(client.ID, client.RedirectURIs[0], PKCEChallenge(verifier), []string{"workspace.read"}, "pair", "http://127.0.0.1:1/mcp")
	if _, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExchangeAuthorizationCode(code, client.ID, client.RedirectURIs[0], verifier, ""); err == nil || err.Error() != "invalid_grant" {
		t.Fatalf("second exchange error = %v", err)
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
		code, err := store.CreateAuthorizationCode(client.ID, client.RedirectURIs[0], PKCEChallenge(verifier), []string{"workspace.read", "offline_access"}, "pair", audience)
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
