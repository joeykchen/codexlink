package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	codexSandboxTable = "sandbox_workspace_write"
	codexRootsKey     = "writable_roots"
)

type SandboxAllowResult struct {
	Added          bool   `json:"added"`
	AlreadyAllowed bool   `json:"alreadyAllowed"`
	StateDir       string `json:"stateDir"`
	ConfigPath     string `json:"configPath"`
}

func CodexHome() string {
	if override := strings.TrimSpace(os.Getenv("CODEX_HOME")); override != "" {
		absolute, _ := filepath.Abs(override)
		return absolute
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func CodexConfigPath() string { return filepath.Join(CodexHome(), "config.toml") }

func tomlPath(value string) string {
	absolute, err := filepath.Abs(value)
	if err == nil {
		value = absolute
	}
	return filepath.ToSlash(value)
}

func equivalentPath(left, right string) bool {
	left = strings.TrimRight(filepath.ToSlash(left), "/")
	right = strings.TrimRight(filepath.ToSlash(right), "/")
	if runtime.GOOS == "windows" || windowsStyle(left) || windowsStyle(right) {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func windowsStyle(value string) bool {
	return len(value) >= 2 && value[1] == ':' || strings.Contains(value, `\`)
}

func EnsureCodexSandboxAllowlist(configPath, stateDir string) (SandboxAllowResult, error) {
	if configPath == "" {
		configPath = CodexConfigPath()
	}
	if stateDir == "" {
		stateDir = StateDir()
	}
	stateDir, _ = filepath.Abs(stateDir)
	if _, err := EnsureDir(stateDir); err != nil {
		return SandboxAllowResult{}, err
	}
	if _, err := EnsureDir(filepath.Dir(configPath)); err != nil {
		return SandboxAllowResult{}, err
	}
	content := ""
	if data, err := os.ReadFile(configPath); err == nil {
		content = string(data)
	} else if !os.IsNotExist(err) {
		return SandboxAllowResult{}, err
	}
	result := SandboxAllowResult{StateDir: stateDir, ConfigPath: configPath}
	for _, root := range writableRoots(content) {
		if equivalentPath(root, stateDir) {
			result.AlreadyAllowed = true
			return result, nil
		}
	}
	next, err := addWritableRoot(content, stateDir)
	if err != nil {
		return SandboxAllowResult{}, err
	}
	if err := WriteSecureFile(configPath, []byte(next)); err != nil {
		return SandboxAllowResult{}, err
	}
	result.Added = true
	return result, nil
}

func IsCodexStateAllowed(content, stateDir string) bool {
	for _, root := range writableRoots(content) {
		if equivalentPath(root, stateDir) {
			return true
		}
	}
	return false
}

func writableRoots(content string) []string {
	body, _, _, ok := tableBody(content, codexSandboxTable)
	if !ok {
		return nil
	}
	assignment := regexp.MustCompile(`(?ms)^\s*` + regexp.QuoteMeta(codexRootsKey) + `\s*=\s*(\[.*?\])`).FindStringSubmatch(body)
	if len(assignment) != 2 {
		return nil
	}
	stringsRE := regexp.MustCompile(`"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'`)
	matches := stringsRE.FindAllStringSubmatch(assignment[1], -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		value := match[1]
		if value == "" {
			value = match[2]
		}
		value = strings.ReplaceAll(value, `\"`, `"`)
		value = strings.ReplaceAll(value, `\\`, `\`)
		values = append(values, value)
	}
	return values
}

func addWritableRoot(content, stateDir string) (string, error) {
	escaped := strings.ReplaceAll(strings.ReplaceAll(tomlPath(stateDir), `\`, `\\`), `"`, `\"`)
	body, start, end, ok := tableBody(content, codexSandboxTable)
	if !ok {
		separator := ""
		if content != "" && !strings.HasSuffix(content, "\n\n") {
			if strings.HasSuffix(content, "\n") {
				separator = "\n"
			} else {
				separator = "\n\n"
			}
		}
		return content + separator + "[" + codexSandboxTable + "]\n" + codexRootsKey + " = [\"" + escaped + "\"]\n", nil
	}
	assignmentRE := regexp.MustCompile(`(?ms)^\s*` + regexp.QuoteMeta(codexRootsKey) + `\s*=\s*(\[.*?\])`)
	location := assignmentRE.FindStringSubmatchIndex(body)
	if location == nil {
		headerEnd := strings.IndexByte(body, '\n')
		if headerEnd < 0 {
			headerEnd = len(body)
		} else {
			headerEnd++
		}
		insert := codexRootsKey + " = [\"" + escaped + "\"]\n"
		absolute := start + headerEnd
		return content[:absolute] + insert + content[absolute:], nil
	}
	rawArray := body[location[2]:location[3]]
	var replacement string
	if strings.Contains(rawArray, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rawArray), "]"))
		if !strings.HasSuffix(trimmed, ",") && trimmed != "[" {
			trimmed += ","
		}
		replacement = trimmed + "\n  \"" + escaped + "\",\n]"
	} else {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(rawArray), "["), "]"))
		if trimmed == "" {
			replacement = "[\"" + escaped + "\"]"
		} else {
			replacement = "[" + trimmed + ", \"" + escaped + "\"]"
		}
	}
	absoluteStart := start + location[2]
	absoluteEnd := start + location[3]
	if absoluteEnd > end {
		return "", fmt.Errorf("invalid writable_roots assignment")
	}
	return content[:absoluteStart] + replacement + content[absoluteEnd:], nil
}

func tableBody(content, name string) (body string, start, end int, ok bool) {
	headerRE := regexp.MustCompile(`(?m)^\s*\[` + regexp.QuoteMeta(name) + `\]\s*$`)
	location := headerRE.FindStringIndex(content)
	if location == nil {
		return "", 0, 0, false
	}
	start = location[0]
	rest := content[location[1]:]
	nextRE := regexp.MustCompile(`(?m)^\s*\[[^\]]+\]\s*$`)
	next := nextRE.FindStringIndex(rest)
	end = len(content)
	if next != nil {
		end = location[1] + next[0]
	}
	return content[start:end], start, end, true
}
