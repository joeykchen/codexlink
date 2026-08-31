package workspace

import (
	"bufio"
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
)

var sensitivePatterns = []string{
	".env",
	".env.*",
	"!.env.example",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"*.jks",
	"*.keystore",
	"id_rsa",
	"id_rsa.*",
	"id_ed25519",
	"id_ed25519.*",
	"id_ecdsa",
	"id_ecdsa.*",
	"id_dsa",
	"id_dsa.*",
	".ssh/",
	".aws/",
	".azure/",
	".gnupg/",
	".kube/",
	".docker/config.json",
	".npmrc",
	".pypirc",
	".netrc",
	"_netrc",
	".git-credentials",
	"*.keychain",
	"*.keychain-db",
	".cloudflared/",
	"credentials.json",
	"service-account*.json",
	"secrets.json",
	"cookies.sqlite",
	"Cookies",
	".codexlink-secrets*",
	".c2c-secrets*",
}

var noisePatterns = []string{
	".git/",
	"node_modules/",
	"dist/",
	"build/",
	"out/",
	".next/",
	".nuxt/",
	".svelte-kit/",
	"coverage/",
	".cache/",
	".turbo/",
	".venv/",
	"venv/",
	"__pycache__/",
	".pytest_cache/",
	".mypy_cache/",
	"target/",
	".gradle/",
	".idea/",
	".tooling/",
	".pnpm-store/",
	"vendor/",
	".DS_Store",
	"*.lock",
	"pnpm-lock.yaml",
	"package-lock.json",
	"yarn.lock",
}

type ignoreRule struct {
	negated bool
	re      *regexp.Regexp
}

type ruleSet struct {
	rules []ignoreRule
}

func newRuleSet(patterns []string) ruleSet {
	set := ruleSet{}
	for _, pattern := range patterns {
		if rule, ok := compileIgnoreRule(pattern); ok {
			set.rules = append(set.rules, rule)
		}
	}
	return set
}

func compileIgnoreRule(raw string) (ignoreRule, bool) {
	pattern := strings.TrimSpace(raw)
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		return ignoreRule{}, false
	}
	negated := false
	if strings.HasPrefix(pattern, `\!`) {
		pattern = strings.TrimPrefix(pattern, `\`)
	} else if strings.HasPrefix(pattern, "!") {
		negated = true
		pattern = strings.TrimPrefix(pattern, "!")
	}
	pattern = strings.ReplaceAll(pattern, `\`, "/")
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	directoryOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return ignoreRule{}, false
	}
	hasSlash := strings.Contains(pattern, "/")

	var body strings.Builder
	for i := 0; i < len(pattern); {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i += 2
				if i < len(pattern) && pattern[i] == '/' {
					body.WriteString(`(?:.*/)?`)
					i++
				} else {
					body.WriteString(`.*`)
				}
				continue
			}
			body.WriteString(`[^/]*`)
		case '?':
			body.WriteString(`[^/]`)
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end >= 0 {
				class := pattern[i+1 : i+1+end]
				if strings.HasPrefix(class, "!") {
					class = "^" + regexp.QuoteMeta(strings.TrimPrefix(class, "!"))
				} else {
					class = regexp.QuoteMeta(class)
				}
				body.WriteByte('[')
				body.WriteString(class)
				body.WriteByte(']')
				i += end + 1
			} else {
				body.WriteString(`\[`)
			}
		default:
			body.WriteString(regexp.QuoteMeta(string(ch)))
		}
		i++
	}

	prefix := `^(?:.*/)?`
	if anchored || hasSlash {
		prefix = `^`
	}
	suffix := `$`
	if directoryOnly {
		suffix = `(?:/.*)?$`
	}
	re, err := regexp.Compile(prefix + body.String() + suffix)
	if err != nil {
		return ignoreRule{}, false
	}
	return ignoreRule{negated: negated, re: re}, true
}

func (r ruleSet) matches(path string) bool {
	path = normalizeRelative(path)
	ignored := false
	for _, rule := range r.rules {
		if rule.re.MatchString(path) {
			ignored = !rule.negated
		}
	}
	return ignored
}

// Policy keeps immutable secret rules separate from user additions: a custom
// negation can never make a built-in credential pattern readable.
type Policy struct {
	sensitive ruleSet
	noise     ruleSet
	custom    ruleSet
}

func NewPolicy(root string) Policy {
	customPatterns := make([]string, 0)
	for _, ignoreName := range []string{".codexlinkignore", ".c2cignore"} {
		data, err := readBoundedRegularFile(filepath.Join(root, ignoreName), 256*1024)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			customPatterns = append(customPatterns, scanner.Text())
		}
		break
	}
	return Policy{
		sensitive: newRuleSet(sensitivePatterns),
		noise:     newRuleSet(noisePatterns),
		custom:    newRuleSet(customPatterns),
	}
}

func normalizeRelative(path string) string {
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	return path
}

func (p Policy) IsSensitive(path string) bool {
	path = normalizeRelative(path)
	if path == "" || path == "." {
		return false
	}
	return p.sensitive.matches(path) || p.custom.matches(path)
}

func (p Policy) IsNoise(path string) bool {
	path = normalizeRelative(path)
	if path == "" || path == "." {
		return false
	}
	return p.noise.matches(path)
}

func (p Policy) IsHidden(path string) bool {
	return p.IsSensitive(path) || p.IsNoise(path)
}

func MatchGlob(pattern, path string) bool {
	rule, ok := compileIgnoreRule(pattern)
	if !ok {
		return false
	}
	return rule.re.MatchString(normalizeRelative(path))
}
