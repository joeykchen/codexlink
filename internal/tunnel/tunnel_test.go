package tunnel

import (
	"context"
	"errors"
	"testing"
)

type fakeAccount struct {
	cert       bool
	loginErr   error
	createErr  error
	routeErr   error
	loginCalls int
}

func (f *fakeAccount) HasCert() bool { return f.cert }
func (f *fakeAccount) Login(context.Context) error {
	f.loginCalls++
	if f.loginErr == nil {
		f.cert = true
	}
	return f.loginErr
}
func (f *fakeAccount) List(context.Context) ([]ListedTunnel, error) { return nil, nil }
func (f *fakeAccount) Create(context.Context, string) (ListedTunnel, error) {
	if f.createErr != nil {
		return ListedTunnel{}, f.createErr
	}
	return ListedTunnel{ID: "12345678-1234-1234-1234-123456789abc", Name: "codexlink-workspace"}, nil
}
func (f *fakeAccount) RouteDNS(context.Context, string, string) error { return f.routeErr }

func TestTunnelStateHostnameAndProvision(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	if got := ParseQuickURL("INF https://Blue-bird.trycloudflare.com ready"); got != "https://blue-bird.trycloudflare.com" {
		t.Fatalf("quick URL = %q", got)
	}
	if host, err := SuggestedHostname("https://example.com/zone", "My 项目", "abcdef123456"); err != nil || host != "codexlink-my.example.com" {
		t.Fatalf("host=%q err=%v", host, err)
	}
	if _, err := NormalizeHostname("bad_host"); err == nil {
		t.Fatal("invalid hostname accepted")
	}
	account := &fakeAccount{}
	result := ProvisionNamed(context.Background(), "workspace", "Demo", "example.com", "", account)
	if !result.OK || result.Fallback || !NamedReady(result.State) || account.loginCalls != 1 {
		t.Fatalf("provision result: %#v account=%#v", result, account)
	}
	loaded, err := ReadState("workspace")
	if err != nil || !NamedReady(loaded) {
		t.Fatalf("loaded state: %#v err=%v", loaded, err)
	}
}

func TestNamedProvisionFallsBackToQuick(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	result := ProvisionNamed(context.Background(), "ws", "Demo", "example.com", "", &fakeAccount{cert: true, createErr: errors.New("boom")})
	if !result.OK || !result.Fallback || result.State.Preference != PreferenceQuick || result.State.FallbackReason != "create_failed" {
		t.Fatalf("fallback result: %#v", result)
	}
}

func TestParseTunnelListFormats(t *testing.T) {
	jsonOutput := `[{"id":"12345678-1234-1234-1234-123456789abc","name":"demo"}]`
	items := ParseTunnelList(jsonOutput)
	if len(items) != 1 || items[0].Name != "demo" {
		t.Fatalf("JSON list: %#v", items)
	}
	table := "ID NAME CREATED\n12345678-1234-1234-1234-123456789abc demo 2026-01-01"
	items = ParseTunnelList(table)
	if len(items) != 1 || items[0].Name != "demo" {
		t.Fatalf("table list: %#v", items)
	}
}
