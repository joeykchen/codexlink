package config

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/SKILL.md
var codexLinkSkill []byte

type SkillInstallResult struct {
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Updated   bool   `json:"updated"`
	Unchanged bool   `json:"unchanged"`
}

func CodexSkillsDir() string {
	if override := strings.TrimSpace(os.Getenv("CODEXLINK_SKILLS_DIR")); override != "" {
		absolute, _ := filepath.Abs(override)
		return absolute
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agents", "skills")
}

func CodexLinkSkillPath() string {
	return filepath.Join(CodexSkillsDir(), "codexlink", "SKILL.md")
}

func CodexLinkSkillContent() []byte { return append([]byte(nil), codexLinkSkill...) }

func EnsureCodexLinkSkill() (SkillInstallResult, error) {
	path := CodexLinkSkillPath()
	result := SkillInstallResult{Path: path}
	previous, err := os.ReadFile(path)
	if err == nil && bytes.Equal(previous, CodexLinkSkillContent()) {
		result.Unchanged = true
		return result, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return SkillInstallResult{}, err
	}
	if err := WriteSecureFile(path, CodexLinkSkillContent()); err != nil {
		return SkillInstallResult{}, err
	}
	if os.IsNotExist(err) {
		result.Installed = true
	} else {
		result.Updated = true
	}
	return result, nil
}
