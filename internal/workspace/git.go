package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type GitInfo struct {
	Repository string  `json:"repository,omitempty"`
	IsRepo     bool    `json:"isRepo"`
	Branch     *string `json:"branch"`
	Commit     *string `json:"commit"`
	Dirty      bool    `json:"dirty"`
}

type GitChange struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

type GitStatus struct {
	Repository string      `json:"repository,omitempty"`
	IsRepo     bool        `json:"isRepo"`
	Branch     *string     `json:"branch"`
	Upstream   *string     `json:"upstream"`
	Ahead      int         `json:"ahead"`
	Behind     int         `json:"behind"`
	Staged     []GitChange `json:"staged"`
	Unstaged   []GitChange `json:"unstaged"`
	Untracked  []string    `json:"untracked"`
	Conflicted []string    `json:"conflicted"`
	Redacted   int         `json:"redacted,omitempty"`
}

type DiffMode string

const (
	DiffUnstaged DiffMode = "unstaged"
	DiffStaged   DiffMode = "staged"
	DiffHead     DiffMode = "head"
)

type GitDiffOptions struct {
	Repository string
	Mode       DiffMode
	Path       string
	Offset     int
	MaxBytes   int
}

type GitDiff struct {
	Repository    string   `json:"repository,omitempty"`
	IsRepo        bool     `json:"isRepo"`
	Mode          DiffMode `json:"mode"`
	TotalBytes    int      `json:"totalBytes"`
	Offset        int      `json:"offset"`
	ReturnedBytes int      `json:"returnedBytes"`
	HasMore       bool     `json:"hasMore"`
	NextOffset    *int     `json:"nextOffset"`
	Diff          string   `json:"diff"`
	RedactedFiles int      `json:"redactedFiles,omitempty"`
}

var gitConfigOverrides = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=" + os.DevNull,
	"-c", "core.attributesFile=" + os.DevNull,
	"-c", "core.excludesFile=" + os.DevNull,
	"-c", "maintenance.auto=false",
	"-c", "gc.auto=0",
	"-c", "submodule.recurse=false",
}

func runGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	overrides := append([]string(nil), gitConfigOverrides...)
	if len(args) > 0 && (args[0] == "status" || args[0] == "diff") {
		filterOverrides, err := discoverFilterOverrides(ctx, root)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, filterOverrides...)
	}
	return runGitWithOverrides(ctx, root, overrides, args...)
}

func runGitWithOverrides(ctx context.Context, root string, overrides []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	commandArgs := append(append([]string(nil), overrides...), args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Dir = root
	command.Env = safeGitEnvironment()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("git command timed out")
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s failed: %s", strings.Join(args, " "), message)
	}
	return output, nil
}

func discoverFilterOverrides(ctx context.Context, root string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := append(append([]string(nil), gitConfigOverrides...),
		"config", "--local", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	command.Env = safeGitEnvironment()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil // no matching filter configuration
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("Git filter inspection timed out")
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("cannot inspect Git filter configuration: %s", message)
	}
	drivers := map[string]struct{}{}
	for _, raw := range bytes.Split(output, []byte{0}) {
		key := strings.TrimSpace(string(raw))
		if key == "" {
			continue
		}
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "filter.") {
			continue
		}
		lastDot := strings.LastIndex(lower, ".")
		if lastDot <= len("filter.") {
			return nil, fmt.Errorf("unsafe Git filter configuration key %q", key)
		}
		suffix := lower[lastDot+1:]
		if suffix != "clean" && suffix != "smudge" && suffix != "process" && suffix != "required" {
			continue
		}
		driver := key[len("filter."):lastDot]
		if !safeGitConfigSubsection(driver) {
			return nil, fmt.Errorf("unsafe Git filter driver name %q", driver)
		}
		drivers[driver] = struct{}{}
	}
	names := make([]string, 0, len(drivers))
	for driver := range drivers {
		names = append(names, driver)
	}
	sort.Strings(names)
	overrides := make([]string, 0, len(names)*8)
	for _, driver := range names {
		prefix := "filter." + driver + "."
		overrides = append(overrides,
			"-c", prefix+"clean=",
			"-c", prefix+"smudge=",
			"-c", prefix+"process=",
			"-c", prefix+"required=false",
		)
	}
	return overrides, nil
}

func safeGitConfigSubsection(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func safeGitEnvironment() []string {
	result := make([]string, 0, len(os.Environ())+9)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if !found || strings.HasPrefix(upper, "GIT_") || upper == "LC_ALL" || upper == "PAGER" {
			continue
		}
		result = append(result, entry)
	}
	result = append(result,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_LAZY_FETCH=1",
		"PAGER=cat",
		"LC_ALL=C",
	)
	return result
}

func (w *Workspace) selectGitRepository(selector string) (Repository, bool, error) {
	repositories, err := w.Repositories()
	if err != nil {
		return Repository{}, false, err
	}
	if len(repositories) == 0 {
		if strings.TrimSpace(selector) != "" {
			return Repository{}, false, NewError(ErrRepositoryMissing, "repository %q is not part of this workspace", selector)
		}
		return Repository{}, false, nil
	}
	repository, err := w.ResolveRepository(selector)
	if err != nil {
		return Repository{}, false, err
	}
	return repository, true, nil
}

// GitInfo preserves the original single-repository convenience API. For a
// repository group callers should use GitInfoFor with an explicit selector.
func (w *Workspace) GitInfo(ctx context.Context) GitInfo {
	info, err := w.GitInfoFor(ctx, "")
	if err != nil {
		return GitInfo{IsRepo: false}
	}
	return info
}

func (w *Workspace) GitInfoFor(ctx context.Context, selector string) (GitInfo, error) {
	repository, found, err := w.selectGitRepository(selector)
	if err != nil {
		return GitInfo{}, err
	}
	if !found {
		return GitInfo{IsRepo: false}, nil
	}
	return w.gitInfoForRepository(ctx, repository), nil
}

func (w *Workspace) gitInfoForRepository(ctx context.Context, repository Repository) GitInfo {
	result := GitInfo{Repository: repository.Path}
	inside, err := runGit(ctx, repository.Root, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return result
	}
	result.IsRepo = true
	if output, err := runGit(ctx, repository.Root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		value := strings.TrimSpace(string(output))
		if value != "" {
			result.Branch = &value
		}
	}
	if output, err := runGit(ctx, repository.Root, "rev-parse", "--short", "HEAD"); err == nil {
		value := strings.TrimSpace(string(output))
		if value != "" {
			result.Commit = &value
		}
	}
	status, statusErr := runGit(ctx, repository.Root, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=all", "--", ".")
	result.Dirty = statusErr == nil && len(bytes.TrimSpace(status)) > 0
	return result
}

func (w *Workspace) GitStatus(ctx context.Context) (GitStatus, error) {
	return w.GitStatusFor(ctx, "")
}

func (w *Workspace) GitStatusFor(ctx context.Context, selector string) (GitStatus, error) {
	repository, found, err := w.selectGitRepository(selector)
	result := GitStatus{Staged: []GitChange{}, Unstaged: []GitChange{}, Untracked: []string{}, Conflicted: []string{}}
	if err != nil {
		return result, err
	}
	if !found {
		return result, nil
	}
	result.Repository = repository.Path
	info := w.gitInfoForRepository(ctx, repository)
	result.IsRepo, result.Branch = info.IsRepo, info.Branch
	if !info.IsRepo {
		return result, nil
	}
	if output, err := runGit(ctx, repository.Root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		value := strings.TrimSpace(string(output))
		if value != "" {
			result.Upstream = &value
			if counts, countErr := runGit(ctx, repository.Root, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); countErr == nil {
				parts := strings.Fields(string(counts))
				if len(parts) == 2 {
					result.Ahead, _ = strconv.Atoi(parts[0])
					result.Behind, _ = strconv.Atoi(parts[1])
				}
			}
		}
	}
	output, err := runGit(ctx, repository.Root, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=all", "--", ".")
	if err != nil {
		return result, wrapRepositoryError(repository, err)
	}
	records := splitNUL(output)
	conflicts := map[string]bool{"DD": true, "AU": true, "UD": true, "UA": true, "DU": true, "AA": true, "UU": true}
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 3 {
			continue
		}
		xy := record[:2]
		path := record[3:]
		displayPath := path
		sensitive := w.Policy.IsSensitive(workspacePathForRepository(repository, path))
		if (xy[0] == 'R' || xy[0] == 'C') && i+1 < len(records) {
			from := records[i+1]
			i++
			displayPath = from + " -> " + path
			sensitive = sensitive || w.Policy.IsSensitive(workspacePathForRepository(repository, from))
		}
		if sensitive {
			result.Redacted++
			continue
		}
		switch xy {
		case "??":
			result.Untracked = append(result.Untracked, path)
			continue
		case "!!":
			continue
		}
		if conflicts[xy] {
			result.Conflicted = append(result.Conflicted, displayPath)
			continue
		}
		if xy[0] != ' ' {
			result.Staged = append(result.Staged, GitChange{Path: displayPath, Change: string(xy[0])})
		}
		if xy[1] != ' ' {
			result.Unstaged = append(result.Unstaged, GitChange{Path: displayPath, Change: string(xy[1])})
		}
	}
	return result, nil
}

func splitNUL(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func diffModeArgs(mode DiffMode) ([]string, error) {
	switch mode {
	case "", DiffUnstaged:
		return nil, nil
	case DiffStaged:
		return []string{"--cached"}, nil
	case DiffHead:
		return []string{"HEAD"}, nil
	default:
		return nil, fmt.Errorf("invalid diff mode: %s", mode)
	}
}

func inScope(path, scope string) bool {
	if scope == "" || scope == "." {
		return true
	}
	return path == scope || strings.HasPrefix(path, strings.TrimSuffix(scope, "/")+"/")
}

func (w *Workspace) GitDiff(ctx context.Context, options GitDiffOptions) (GitDiff, error) {
	mode := options.Mode
	if mode == "" {
		mode = DiffUnstaged
	}
	modeArgs, err := diffModeArgs(mode)
	if err != nil {
		return GitDiff{}, err
	}
	result := GitDiff{Mode: mode}
	repository, found, err := w.selectGitRepository(options.Repository)
	if err != nil {
		return result, err
	}
	if !found {
		return result, nil
	}
	result.Repository = repository.Path
	if !w.gitInfoForRepository(ctx, repository).IsRepo {
		return result, nil
	}
	result.IsRepo = true
	scope := "."
	if strings.TrimSpace(options.Path) != "" {
		scope, err = w.repositoryRelativePath(repository, options.Path)
		if err != nil {
			return GitDiff{}, err
		}
	}
	listArgs := []string{"diff", "--name-status", "-z", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--find-renames=1%"}
	listArgs = append(listArgs, modeArgs...)
	listArgs = append(listArgs, "--", ".")
	inventory, err := runGit(ctx, repository.Root, listArgs...)
	if err != nil {
		return GitDiff{}, wrapRepositoryError(repository, err)
	}
	tokens := splitNUL(inventory)
	paths := make([]string, 0, len(tokens))
	seen := map[string]bool{}
	for i := 0; i < len(tokens); {
		status := tokens[i]
		i++
		if status == "" {
			continue
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+1 >= len(tokens) {
				break
			}
			oldPath, newPath := tokens[i], tokens[i+1]
			i += 2
			if w.Policy.IsSensitive(workspacePathForRepository(repository, oldPath)) || w.Policy.IsSensitive(workspacePathForRepository(repository, newPath)) {
				result.RedactedFiles++
				continue
			}
			if !inScope(oldPath, scope) && !inScope(newPath, scope) {
				continue
			}
			for _, path := range []string{oldPath, newPath} {
				if !seen[path] {
					paths = append(paths, path)
					seen[path] = true
				}
			}
			continue
		}
		if i >= len(tokens) {
			break
		}
		path := tokens[i]
		i++
		if w.Policy.IsSensitive(workspacePathForRepository(repository, path)) {
			result.RedactedFiles++
			continue
		}
		if inScope(path, scope) && !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	if len(paths) == 0 {
		return result, nil
	}
	sort.Strings(paths)
	batches := batchPaths(paths, 50, 32*1024)
	var combined bytes.Buffer
	const aggregateLimit = 64 * 1024 * 1024
	for _, batch := range batches {
		args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--find-renames=1%"}
		args = append(args, modeArgs...)
		args = append(args, "--")
		for _, path := range batch {
			args = append(args, ":(literal)"+path)
		}
		chunk, chunkErr := runGit(ctx, repository.Root, args...)
		if chunkErr != nil {
			return GitDiff{}, wrapRepositoryError(repository, chunkErr)
		}
		if combined.Len()+len(chunk) > aggregateLimit {
			return GitDiff{}, NewError(ErrFileTooLarge, "git diff exceeds the 64 MiB safety limit")
		}
		_, _ = combined.Write(chunk)
	}
	full := combined.Bytes()
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(full) {
		offset = len(full)
	}
	maxBytes := options.MaxBytes
	if maxBytes < 1024 {
		maxBytes = 64 * 1024
	}
	if maxBytes > 256*1024 {
		maxBytes = 256 * 1024
	}
	end := offset + maxBytes
	if end > len(full) {
		end = len(full)
	}
	chunk := full[offset:end]
	if end < len(full) {
		if newline := bytes.LastIndexByte(chunk, '\n'); newline > 0 {
			chunk = chunk[:newline+1]
			end = offset + len(chunk)
		}
	}
	for len(chunk) > 0 && !utf8.Valid(chunk) {
		chunk = chunk[:len(chunk)-1]
		end--
	}
	result.TotalBytes = len(full)
	result.Offset = offset
	result.ReturnedBytes = len(chunk)
	result.HasMore = end < len(full)
	if result.HasMore {
		next := end
		result.NextOffset = &next
	}
	result.Diff = string(chunk)
	return result, nil
}

func batchPaths(paths []string, maxCount, maxBytes int) [][]string {
	batches := make([][]string, 0)
	current := make([]string, 0, maxCount)
	bytesUsed := 0
	for _, path := range paths {
		cost := len(path) + len(":(literal)") + 1
		if len(current) > 0 && (len(current) >= maxCount || bytesUsed+cost > maxBytes) {
			batches = append(batches, current)
			current = make([]string, 0, maxCount)
			bytesUsed = 0
		}
		current = append(current, path)
		bytesUsed += cost
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}
