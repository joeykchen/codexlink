package auth

import (
	"net"
	"testing"
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

func TestClientMetadataSelectsNoneFromPluralCapabilityList(t *testing.T) {
	metadata, err := (ClientMetadata{
		ClientID:                          "https://chatgpt.com/oauth/client.json",
		ClientName:                        "ChatGPT",
		RedirectURIs:                      []string{"https://chatgpt.com/oauth/callback"},
		TokenEndpointAuthMethod:           "private_key_jwt",
		TokenEndpointAuthMethodsSupported: []string{"none", "private_key_jwt"},
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TokenEndpointAuthMethod != "none" {
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
