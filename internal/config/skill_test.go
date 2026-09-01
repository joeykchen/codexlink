package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexLinkSkillCopiesAreByteIdentical(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, CodexLinkSkillContent()) {
		t.Fatal("skill/SKILL.md and embedded skill must be byte-identical")
	}
	dir := t.TempDir()
	t.Setenv("CODEXLINK_SKILLS_DIR", dir)
	if result, err := EnsureCodexLinkSkill(); err != nil || !result.Installed {
		t.Fatalf("install = %+v, %v", result, err)
	}
	managed, err := os.ReadFile(filepath.Join(dir, "codexlink", "SKILL.md"))
	if err != nil || !bytes.Equal(published, managed) {
		t.Fatalf("managed skill mismatch: %v", err)
	}
	if result, err := EnsureCodexLinkSkill(); err != nil || !result.Unchanged {
		t.Fatalf("second install = %+v, %v", result, err)
	}
}

func TestCodexLinkSkillResponseExtractionPolicy(t *testing.T) {
	content := string(CodexLinkSkillContent())
	ordered := []string{
		"repository-data plane",
		"browser-control task",
		"structured browser text",
		"message's own `Copy` action",
		"smallest region containing its unread text",
		"Ask the user to paste only",
	}
	previous := -1
	for _, phrase := range ordered {
		index := strings.Index(content, phrase)
		if index <= previous {
			t.Fatalf("response extraction policy missing or out of order: %q", phrase)
		}
		previous = index
	}
	for _, required := range []string{
		"full-page or full-conversation screenshot",
		"Do not save screenshots in the repository",
		"Verify the task ID, state, iteration, and obvious truncation",
		"independently obtain repository evidence through CodexLink",
		"do not reduce autonomous browser operation",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("response extraction policy missing %q", required)
		}
	}
	if strings.Contains(content, "solely when the user explicitly requests or approves visual extraction") {
		t.Fatal("visual fallback must not require unnecessary user intervention")
	}
}

func TestCodexLinkSkillPrefersStructuredControlAPI(t *testing.T) {
	content := string(CodexLinkSkillContent())
	api := strings.Index(content, "Structured control-response API (preferred)")
	fallback := strings.Index(content, "Use the narrowest available response-extraction method")
	if api < 0 || fallback < 0 || api > fallback {
		t.Fatalf("structured API must be defined before the compatibility extraction policy")
	}
	for _, phrase := range []string{"control prepare", "CONTROL_REQUEST_ID", "submit_control_response", "control wait", "control get", "control cancel", "Compatibility-only"} {
		if !strings.Contains(content, phrase) {
			t.Fatalf("structured control policy missing %q", phrase)
		}
	}
	for _, phrase := range []string{"--ttl 2h", "--timeout 90m", "do not cancel merely because the first wait ended"} {
		if !strings.Contains(content, phrase) {
			t.Fatalf("long-running control policy missing %q", phrase)
		}
	}
}

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
