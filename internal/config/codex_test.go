package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodexSandboxAllowlistPreservesConfigAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "codex", "config.toml")
	stateDir := filepath.Join(root, "state")
	original := "model = \"gpt-5\"\n\n[sandbox_workspace_write]\nwritable_roots = [\"/tmp/existing\"]\n\n[other]\nvalue = 1\n"
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := EnsureCodexSandboxAllowlist(configPath, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Added || first.AlreadyAllowed {
		t.Fatalf("unexpected first result: %#v", first)
	}
	data, _ := os.ReadFile(configPath)
	content := string(data)
	if !strings.Contains(content, `model = "gpt-5"`) || !strings.Contains(content, "[other]") || !IsCodexStateAllowed(content, stateDir) {
		t.Fatalf("config was not safely patched:\n%s", content)
	}
	second, err := EnsureCodexSandboxAllowlist(configPath, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Added || !second.AlreadyAllowed {
		t.Fatalf("unexpected second result: %#v", second)
	}
	data2, _ := os.ReadFile(configPath)
	if string(data2) != content {
		t.Fatal("idempotent call changed the file")
	}
}

func TestAddWritableRootCreatesMissingTable(t *testing.T) {
	next, err := addWritableRoot("model = \"x\"\n", "/tmp/codexlink state")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "[sandbox_workspace_write]") || !strings.Contains(next, "writable_roots") || !strings.Contains(next, "/tmp/codexlink state") {
		t.Fatalf("unexpected TOML:\n%s", next)
	}
}
