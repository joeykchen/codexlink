package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodexLinkSkillIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEXLINK_SKILLS_DIR", dir)
	first, err := EnsureCodexLinkSkill()
	if err != nil || !first.Installed {
		t.Fatalf("first install = %+v, %v", first, err)
	}
	second, err := EnsureCodexLinkSkill()
	if err != nil || !second.Unchanged {
		t.Fatalf("second install = %+v, %v", second, err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "codexlink", "SKILL.md"))
	if err != nil || !strings.Contains(string(content), "name: codexlink") || strings.Contains(string(content), "c2c") {
		t.Fatalf("unexpected skill content: %v %q", err, content)
	}
}

func TestEnsureCodexLinkSkillUpdatesManagedContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEXLINK_SKILLS_DIR", dir)
	path := filepath.Join(dir, "codexlink", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureCodexLinkSkill()
	if err != nil || !result.Updated {
		t.Fatalf("update = %+v, %v", result, err)
	}
}
