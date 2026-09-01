package auth

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRedirectURIMatchesLoopbackEphemeralPort(t *testing.T) {
	registered := []string{"http://127.0.0.1/callback/codexlink"}
	if !RedirectURIMatches(registered, "http://127.0.0.1:54321/callback/codexlink") {
		t.Fatal("ephemeral loopback port should match")
	}
	if RedirectURIMatches(registered, "http://localhost:54321/callback/codexlink") {
		t.Fatal("host substitution should not match")
	}
	if RedirectURIMatches(registered, "http://127.0.0.1:54321/other") {
		t.Fatal("path substitution should not match")
	}
}

func TestMetadataClientIDValidation(t *testing.T) {
	valid := "https://chatgpt.com/oauth/client.json"
	if _, err := parseMetadataClientID(valid); err != nil {
		t.Fatalf("valid metadata id: %v", err)
	}
	for _, value := range []string{
		"http://chatgpt.com/oauth/client.json",
		"https://127.0.0.1/client.json",
		"https://user@chatgpt.com/client.json",
		"https://chatgpt.com/",
		"https://chatgpt.com:444/client.json",
	} {
		if _, err := parseMetadataClientID(value); err == nil && value != "https://127.0.0.1/client.json" {
			t.Fatalf("invalid metadata id accepted: %s", value)
		}
	}
}

func TestPublicMetadataIP(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.168.1.1", "::1", "fc00::1"} {
		if publicMetadataIP(net.ParseIP(value)) {
			t.Fatalf("private address accepted: %s", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicMetadataIP(net.ParseIP(value)) {
			t.Fatalf("public address rejected: %s", value)
		}
	}
}

func TestClientMetadataNormalization(t *testing.T) {
	metadata, err := (ClientMetadata{
		ClientName:   "ChatGPT",
		RedirectURIs: []string{"https://chatgpt.com/callback", "https://chatgpt.com/callback"},
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.RedirectURIs) != 1 || metadata.TokenEndpointAuthMethod != "none" || len(metadata.GrantTypes) != 2 {
		t.Fatalf("normalized metadata: %#v", metadata)
	}
}

func TestClientMetadataSelectsPreferredPrivateKeyJWT(t *testing.T) {
	metadata, err := (ClientMetadata{
		ClientID:                          "https://chatgpt.com/oauth/client.json",
		ClientName:                        "ChatGPT",
		RedirectURIs:                      []string{"https://chatgpt.com/oauth/callback"},
		TokenEndpointAuthMethod:           "private_key_jwt",
		TokenEndpointAuthMethodsSupported: []string{"none", "private_key_jwt"},
		TokenEndpointAuthSigningAlg:       "RS256",
		JWKSURI:                           "https://chatgpt.com/oauth/jwks.json",
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TokenEndpointAuthMethod != "private_key_jwt" {
		t.Fatalf("selected auth method = %q", metadata.TokenEndpointAuthMethod)
	}
}

func TestClientMetadataRejectsNoSupportedAuthIntersection(t *testing.T) {
	_, err := (ClientMetadata{
		ClientID:                          "https://example.com/client.json",
		ClientName:                        "Example",
		RedirectURIs:                      []string{"https://example.com/callback"},
		TokenEndpointAuthMethodsSupported: []string{"private_key_jwt"},
	}).normalized()
	if err == nil {
		t.Fatal("metadata without none support should be rejected")
	}
}

func TestClientMetadataCacheIsBoundedAndLRU(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	resolver := NewHTTPClientMetadataResolver()
	resolver.MaxCacheEntries = 2
	resolver.cache = map[string]metadataCacheEntry{
		"old": {client: Client{ID: "old"}, expiresAt: now.Add(time.Minute), lastUsed: now},
		"new": {client: Client{ID: "new"}, expiresAt: now.Add(time.Minute), lastUsed: now.Add(time.Second)},
	}
	resolver.evictOldestLocked()
	if _, ok := resolver.cache["old"]; ok {
		t.Fatal("least recently used entry was not evicted")
	}
	resolver.cache["expired"] = metadataCacheEntry{client: Client{ID: "expired"}, expiresAt: now, lastUsed: now}
	resolver.pruneCacheLocked(now)
	if _, ok := resolver.cache["expired"]; ok {
		t.Fatal("expired cache entry was not pruned")
	}
}

func TestClientMetadataBounds(t *testing.T) {
	valid := ClientMetadata{RedirectURIs: []string{"https://example.com/callback"}}
	cases := []struct {
		name     string
		metadata ClientMetadata
	}{
		{"too many redirects", func() ClientMetadata {
			value := valid
			value.RedirectURIs = make([]string, maxClientMetadataRedirectURIs+1)
			for i := range value.RedirectURIs {
				value.RedirectURIs[i] = fmt.Sprintf("https://example.com/callback/%d", i)
			}
			return value
		}()},
		{"long redirect", ClientMetadata{RedirectURIs: []string{"https://example.com/" + strings.Repeat("x", maxClientMetadataURIBytes)}}},
		{"long client id", ClientMetadata{ClientID: "https://example.com/" + strings.Repeat("x", maxClientMetadataURIBytes), RedirectURIs: valid.RedirectURIs}},
		{"too many grants", ClientMetadata{RedirectURIs: valid.RedirectURIs, GrantTypes: make([]string, maxClientMetadataValues+1)}},
		{"long scope", ClientMetadata{RedirectURIs: valid.RedirectURIs, Scope: strings.Repeat("x", maxClientMetadataScopeBytes+1)}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.metadata.normalized(); err == nil {
				t.Fatal("oversized metadata was accepted")
			}
		})
	}
	if _, err := valid.normalized(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
}

func TestMetadataClientIDLengthIsBounded(t *testing.T) {
	value := "https://example.com/" + strings.Repeat("x", maxClientMetadataURIBytes)
	if _, err := parseMetadataClientID(value); err == nil {
		t.Fatal("oversized metadata client_id was accepted")
	}
}

func TestClientMetadataCacheReturnsDefensiveCopies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clientID := "https://example.com/client.json"
	resolver := NewHTTPClientMetadataResolver()
	resolver.Now = func() time.Time { return now }
	resolver.cache[clientID] = metadataCacheEntry{
		client:    Client{ID: clientID, RedirectURIs: []string{"https://example.com/callback"}, GrantTypes: []string{"authorization_code"}},
		expiresAt: now.Add(time.Minute), lastUsed: now,
	}
	first, err := resolver.Resolve(context.Background(), clientID)
	if err != nil {
		t.Fatal(err)
	}
	first.RedirectURIs[0] = "https://attacker.example/callback"
	first.GrantTypes[0] = "refresh_token"
	second, err := resolver.Resolve(context.Background(), clientID)
	if err != nil {
		t.Fatal(err)
	}
	if second.RedirectURIs[0] != "https://example.com/callback" || second.GrantTypes[0] != "authorization_code" {
		t.Fatalf("cached client was mutated through a returned value: %+v", second)
	}
}
