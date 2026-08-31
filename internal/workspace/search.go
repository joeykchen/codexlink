package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type SearchOptions struct {
	Query string
	Path  string
	Glob  string
	Limit int
	Regex bool
}

type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type SearchResult struct {
	Matches    []SearchMatch `json:"matches"`
	MatchCount int           `json:"matchCount"`
	Truncated  bool          `json:"truncated"`
	Engine     string        `json:"engine"`
}

var (
	ripgrepOnce sync.Once
	ripgrepPath string
)

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func findRipgrep() string {
	if firstEnv("CODEXLINK_DISABLE_RG", "C2C_DISABLE_RG") == "1" {
		return ""
	}
	if override := strings.TrimSpace(firstEnv("CODEXLINK_RG_PATH", "C2C_RG_PATH")); override != "" {
		return override
	}
	ripgrepOnce.Do(func() {
		if found, err := exec.LookPath("rg"); err == nil {
			ripgrepPath = found
			return
		}
		candidates := []string{
			"/opt/homebrew/bin/rg",
			"/usr/local/bin/rg",
			"/usr/bin/rg",
			"/Applications/Cursor.app/Contents/Resources/app/node_modules/@vscode/ripgrep/bin/rg",
			"/Applications/Visual Studio Code.app/Contents/Resources/app/node_modules/@vscode/ripgrep/bin/rg",
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code", "resources", "app", "node_modules", "@vscode", "ripgrep", "bin", "rg.exe"),
			)
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				ripgrepPath = candidate
				return
			}
		}
	})
	return ripgrepPath
}

func (w *Workspace) Search(ctx context.Context, options SearchOptions) (SearchResult, error) {
	query := options.Query
	if len(strings.TrimSpace(query)) < 2 {
		return SearchResult{Matches: []SearchMatch{}, Engine: "go"}, nil
	}
	limit := options.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	searchPath := options.Path
	if strings.TrimSpace(searchPath) == "" {
		searchPath = "."
	}
	absolute, relative, err := w.Resolve(searchPath, false)
	if err != nil {
		return SearchResult{}, err
	}
	if options.Regex {
		if _, err := regexp.Compile(query); err != nil {
			return SearchResult{}, NewError(ErrInvalidExpression, "invalid regular expression: %v", err)
		}
	}
	if rg := findRipgrep(); rg != "" {
		result, rgErr := w.searchRipgrep(ctx, rg, relative, options, limit)
		if rgErr == nil {
			return result, nil
		}
	}
	return w.searchGo(ctx, absolute, relative, options, limit)
}

func (w *Workspace) searchRipgrep(ctx context.Context, binary, relative string, options SearchOptions, limit int) (SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := []string{"--json", "--max-filesize", "2M", "--max-count", "20", "--smart-case"}
	if !options.Regex {
		args = append(args, "-F")
	}
	if options.Glob != "" {
		args = append(args, "-g", options.Glob)
	}
	target := relative
	if target == "" || target == "." {
		target = "."
	}
	args = append(args, "--", options.Query, target)
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = w.Root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return SearchResult{}, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return SearchResult{}, err
	}
	matches := make([]SearchMatch, 0, limit)
	truncated := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				LineNumber int `json:"line_number"`
				Lines      struct {
					Text string `json:"text"`
				} `json:"lines"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "match" || event.Data.Path.Text == "" {
			continue
		}
		path := event.Data.Path.Text
		if filepath.IsAbs(path) {
			path, _ = filepath.Rel(w.Root, path)
		}
		path = filepath.ToSlash(path)
		if strings.HasPrefix(path, "../") || w.Policy.IsHidden(path) {
			continue
		}
		text := strings.TrimRight(event.Data.Lines.Text, "\r\n")
		if len(text) > 500 {
			text = string(utf8SafePrefix([]byte(text), 500))
		}
		matches = append(matches, SearchMatch{Path: path, Line: event.Data.LineNumber, Text: text})
		if len(matches) >= limit {
			truncated = true
			cancel()
			break
		}
	}
	waitErr := command.Wait()
	if scanErr := scanner.Err(); scanErr != nil && !errors.Is(scanErr, context.Canceled) {
		return SearchResult{}, scanErr
	}
	if waitErr != nil && !truncated {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) || exitError.ExitCode() != 1 {
			return SearchResult{}, fmt.Errorf("ripgrep failed: %s", strings.TrimSpace(stderr.String()))
		}
	}
	return SearchResult{Matches: matches, MatchCount: len(matches), Truncated: truncated, Engine: "ripgrep"}, nil
}

func (w *Workspace) searchGo(ctx context.Context, absolute, relative string, options SearchOptions, limit int) (SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	caseSensitive := containsUpper(options.Query)
	needle := options.Query
	if !caseSensitive && !options.Regex {
		needle = strings.ToLower(needle)
	}
	var expression *regexp.Regexp
	if options.Regex {
		pattern := options.Query
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return SearchResult{}, NewError(ErrInvalidExpression, "invalid regular expression: %v", err)
		}
		expression = compiled
	}
	matches := make([]SearchMatch, 0, limit)
	truncated := false
	inspect := func(path, rel string, info fs.FileInfo) error {
		if info.Size() > 2*1024*1024 || w.Policy.IsHidden(rel) {
			return nil
		}
		if options.Glob != "" && !MatchGlob(options.Glob, rel) && !MatchGlob(options.Glob, filepath.Base(rel)) {
			return nil
		}
		binary, err := isBinary(path)
		if err != nil || binary {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			lineNumber++
			line := scanner.Text()
			hit := false
			if expression != nil {
				hit = expression.MatchString(line)
			} else if caseSensitive {
				hit = strings.Contains(line, needle)
			} else {
				hit = strings.Contains(strings.ToLower(line), needle)
			}
			if !hit {
				continue
			}
			if len(line) > 500 {
				line = string(utf8SafePrefix([]byte(line), 500))
			}
			matches = append(matches, SearchMatch{Path: filepath.ToSlash(rel), Line: lineNumber, Text: line})
			if len(matches) >= limit {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	}

	info, err := os.Stat(absolute)
	if err != nil {
		return SearchResult{}, err
	}
	if info.Mode().IsRegular() {
		if relative == "." {
			relative = filepath.Base(absolute)
		}
		_ = inspect(absolute, relative, info)
	} else {
		err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			rel, relErr := filepath.Rel(w.Root, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if w.Policy.IsHidden(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			return inspect(path, rel, info)
		})
		if err != nil && !errors.Is(err, fs.SkipAll) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return SearchResult{}, err
		}
	}
	return SearchResult{Matches: matches, MatchCount: len(matches), Truncated: truncated, Engine: "go"}, nil
}

func containsUpper(value string) bool {
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}
