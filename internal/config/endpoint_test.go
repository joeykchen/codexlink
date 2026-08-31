package config

import "testing"

func strptr(value string) *string { return &value }

func TestEndpointHelpers(t *testing.T) {
	if got := MCPURL("HTTPS://Example.COM/base/"); got != "https://example.com/base/mcp" {
		t.Fatalf("MCPURL = %q", got)
	}
	if action := ConnectorAction(nil, strptr("https://x/mcp")); action != "create" {
		t.Fatalf("action = %s", action)
	}
	if action := ConnectorAction(strptr("HTTPS://X/mcp/"), strptr("https://x/mcp")); action != "none" {
		t.Fatalf("action = %s", action)
	}
	if action := ConnectorAction(strptr("https://old/mcp"), strptr("https://new/mcp")); action != "update" {
		t.Fatalf("action = %s", action)
	}
	name := ConnectorName("项目 / demo !!!", "abcdef123456", nil)
	if name != "CodexLink · 项目 demo" {
		t.Fatalf("name = %q", name)
	}
	previous := &Endpoint{ConnectorName: "Pinned name"}
	if got := ConnectorName("ignored", "abcdef", previous); got != "Pinned name" {
		t.Fatalf("pinned name = %q", got)
	}
}
