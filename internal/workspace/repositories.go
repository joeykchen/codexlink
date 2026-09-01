package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type TopologyMode string

const (
	TopologyDirectory TopologyMode = "directory"
	TopologySingle    TopologyMode = "single-repository"
	TopologyGroup     TopologyMode = "repository-group"
)

// Repository is a Git repository contained by the workspace authorization
// boundary. Path is workspace-relative and Root is intentionally omitted from
// JSON responses so remote clients never receive a host filesystem path.
type Repository struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Root string `json:"-"`
}

type RepositorySummary struct {
	Path    string      `json:"path"`
	Name    string      `json:"name"`
	Project ProjectInfo `json:"project"`
	Git     GitInfo     `json:"git"`
}

type Topology struct {
	Mode              TopologyMode         `json:"mode"`
	RepositoryCount   int                  `json:"repositoryCount"`
	DefaultRepository *string              `json:"defaultRepository,omitempty"`
	Repositories      []RepositorySummary  `json:"repositories"`
	Relations         []RepositoryRelation `json:"relations,omitempty"`
}

func (w *Workspace) repositoryMarker(root string) (bool, error) {
	marker := filepath.Join(root, ".git")
	info, err := os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, NewError(ErrOutsideWorkspace, "Git metadata marker is a symbolic link: %s", filepath.ToSlash(marker))
	}

	gitDir := marker
	if info.Mode().IsRegular() {
		data, readErr := readBoundedRegularFile(marker, 8*1024)
		if readErr != nil {
			return false, NewError(ErrInvalidConfig, "cannot read Git metadata marker %s: %v", filepath.ToSlash(marker), readErr)
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
			return false, NewError(ErrInvalidConfig, "invalid Git metadata marker: %s", filepath.ToSlash(marker))
		}
		value := strings.TrimSpace(line[len("gitdir:"):])
		if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n") {
			return false, NewError(ErrInvalidConfig, "invalid Git metadata path in %s", filepath.ToSlash(marker))
		}
		gitDir = filepath.FromSlash(value)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	} else if !info.IsDir() {
		return false, NewError(ErrInvalidConfig, "unsupported Git metadata marker: %s", filepath.ToSlash(marker))
	}

	realGitDir, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return false, NewError(ErrInvalidConfig, "cannot resolve Git metadata directory for %s: %v", filepath.ToSlash(root), err)
	}
	gitInfo, err := os.Stat(realGitDir)
	if err != nil || !gitInfo.IsDir() {
		return false, NewError(ErrInvalidConfig, "Git metadata directory is not a directory for %s", filepath.ToSlash(root))
	}
	if !w.contains(realGitDir) {
		commonDir, linkedErr := validateLinkedWorktreeMetadata(marker, realGitDir)
		if linkedErr != nil {
			return false, NewError(ErrOutsideWorkspace, "Git metadata for %s resolves outside the workspace", filepath.ToSlash(root))
		}
		if err := validateGitAlternates(w, filepath.Join(commonDir, "objects")); err != nil {
			return false, err
		}
		return true, nil
	}

	commonDir, err := resolveGitCommonDir(realGitDir)
	if err != nil {
		return false, err
	}
	if !w.contains(commonDir) {
		return false, NewError(ErrOutsideWorkspace, "Git common directory for %s resolves outside the workspace", filepath.ToSlash(root))
	}
	if err := validateGitAlternates(w, filepath.Join(commonDir, "objects")); err != nil {
		return false, err
	}
	return true, nil
}

func validateLinkedWorktreeMetadata(marker, gitDir string) (string, error) {
	markerInfo, err := os.Lstat(marker)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid worktree marker")
	}
	if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return "", fmt.Errorf("invalid worktree metadata layout")
	}
	expectedCommonDir := filepath.Dir(filepath.Dir(gitDir))
	expectedInfo, err := os.Stat(expectedCommonDir)
	if err != nil || !expectedInfo.IsDir() {
		return "", fmt.Errorf("invalid common directory")
	}
	data, err := readBoundedRegularFile(filepath.Join(gitDir, "gitdir"), 8*1024)
	if err != nil {
		return "", fmt.Errorf("invalid linked-worktree backlink")
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid linked-worktree backlink")
	}
	backlink := filepath.FromSlash(value)
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(gitDir, backlink)
	}
	backlinkInfo, err := os.Stat(backlink)
	if err != nil {
		return "", fmt.Errorf("invalid linked-worktree backlink")
	}
	if !os.SameFile(markerInfo, backlinkInfo) {
		return "", fmt.Errorf("linked-worktree backlink mismatch")
	}
	commonDir, err := resolveGitCommonDir(gitDir)
	if err != nil {
		return "", fmt.Errorf("invalid linked-worktree common directory")
	}
	commonInfo, err := os.Stat(commonDir)
	if err != nil || !os.SameFile(expectedInfo, commonInfo) {
		return "", fmt.Errorf("linked-worktree common directory mismatch")
	}
	objectsInfo, err := os.Lstat(filepath.Join(commonDir, "objects"))
	if err != nil || !objectsInfo.IsDir() || objectsInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid linked-worktree object directory")
	}
	return commonDir, nil
}

func resolveGitCommonDir(gitDir string) (string, error) {
	path := filepath.Join(gitDir, "commondir")
	data, err := readBoundedRegularFile(path, 8*1024)
	if errors.Is(err, os.ErrNotExist) {
		return gitDir, nil
	}
	if err != nil {
		return "", NewError(ErrInvalidConfig, "cannot read Git commondir: %v", err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n") {
		return "", NewError(ErrInvalidConfig, "invalid Git commondir")
	}
	commonDir := filepath.FromSlash(value)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	real, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", NewError(ErrInvalidConfig, "cannot resolve Git commondir: %v", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", NewError(ErrInvalidConfig, "Git commondir is not a directory")
	}
	return filepath.Clean(real), nil
}

func validateGitAlternates(w *Workspace, objectsDir string) error {
	path := filepath.Join(objectsDir, "info", "alternates")
	data, err := readBoundedRegularFile(path, 256*1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return NewError(ErrInvalidConfig, "cannot read Git object alternates: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		value := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if value == "" {
			continue
		}
		candidate := filepath.FromSlash(value)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(objectsDir, candidate)
		}
		real, evalErr := filepath.EvalSymlinks(candidate)
		if evalErr != nil {
			return NewError(ErrInvalidConfig, "cannot resolve Git object alternate: %v", evalErr)
		}
		if !w.contains(real) {
			return NewError(ErrOutsideWorkspace, "Git object alternate resolves outside the workspace")
		}
	}
	return nil
}

func readBoundedRegularFile(path string, limit int64) ([]byte, error) {
	metadata, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if metadata.Mode()&os.ModeSymlink != 0 || !metadata.Mode().IsRegular() || metadata.Size() > limit {
		return nil, fmt.Errorf("file is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || !os.SameFile(metadata, info) {
		return nil, fmt.Errorf("file is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds safety limit")
	}
	return data, nil
}

// Repositories discovers the Git repositories governed by this workspace. An
// explicit allowlist in .codexlink.json wins over automatic bounded discovery.
func (w *Workspace) Repositories() ([]Repository, error) {
	w.repoOnce.Do(func() {
		w.repositories, w.repoErr = w.discoverRepositories()
	})
	if w.repoErr != nil {
		return nil, w.repoErr
	}
	return append([]Repository(nil), w.repositories...), nil
}

func (w *Workspace) discoverRepositories() ([]Repository, error) {
	if len(w.ProjectConfig.Repositories) > 0 {
		result := make([]Repository, 0, len(w.ProjectConfig.Repositories))
		seenRoots := make(map[string]struct{}, len(w.ProjectConfig.Repositories))
		for _, configured := range w.ProjectConfig.Repositories {
			absolute, relative, err := w.Resolve(configured, false)
			if err != nil {
				return nil, NewError(ErrInvalidConfig, "configured repository %q is invalid: %v", configured, err)
			}
			info, err := os.Stat(absolute)
			if err != nil || !info.IsDir() {
				return nil, NewError(ErrRepositoryMissing, "configured repository %q is not a directory", configured)
			}
			marked, markerErr := w.repositoryMarker(absolute)
			if markerErr != nil {
				return nil, markerErr
			}
			if !marked {
				return nil, NewError(ErrRepositoryMissing, "configured repository %q has no .git marker", configured)
			}
			identity := normalizeIdentityPath(absolute)
			if _, exists := seenRoots[identity]; exists {
				return nil, NewError(ErrInvalidConfig, "configured repositories resolve to the same directory: %q", configured)
			}
			seenRoots[identity] = struct{}{}
			result = append(result, newRepository(absolute, relative))
		}
		sortRepositories(result)
		return result, nil
	}

	rootMarked, err := w.repositoryMarker(w.Root)
	if err != nil {
		return nil, err
	}
	if rootMarked {
		return []Repository{newRepository(w.Root, ".")}, nil
	}

	depthLimit := w.ProjectConfig.RepositoryDepth
	if depthLimit <= 0 {
		depthLimit = defaultRepositoryDepth
	}
	result := make([]Repository, 0)
	var walk func(string, string, int) error
	walk = func(dirAbsolute, dirRelative string, depth int) error {
		entries, err := os.ReadDir(dirAbsolute)
		if err != nil {
			return nil
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			childRelative := entry.Name()
			if dirRelative != "" && dirRelative != "." {
				childRelative = strings.TrimSuffix(dirRelative, "/") + "/" + entry.Name()
			}
			if w.Policy.IsHidden(childRelative) || w.Policy.IsHidden(childRelative+"/") {
				continue
			}
			childAbsolute := filepath.Join(dirAbsolute, entry.Name())
			marked, markerErr := w.repositoryMarker(childAbsolute)
			if markerErr != nil {
				return markerErr
			}
			if marked {
				result = append(result, newRepository(childAbsolute, childRelative))
				if len(result) > maxRepositories {
					return NewError(ErrRepositoryLimit, "workspace contains more than %d repositories; configure an explicit repositories allowlist", maxRepositories)
				}
				continue
			}
			if depth < depthLimit {
				if err := walk(childAbsolute, childRelative, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(w.Root, "", 1); err != nil {
		return nil, err
	}
	sortRepositories(result)
	return result, nil
}

func newRepository(root, relative string) Repository {
	relative = filepath.ToSlash(filepath.Clean(relative))
	if relative == "" {
		relative = "."
	}
	name := filepath.Base(root)
	if relative == "." {
		name = filepath.Base(root)
	}
	return Repository{Path: relative, Name: name, Root: filepath.Clean(root)}
}

func sortRepositories(repositories []Repository) {
	sort.Slice(repositories, func(i, j int) bool {
		return strings.ToLower(repositories[i].Path) < strings.ToLower(repositories[j].Path)
	})
}

func normalizeIdentityPath(value string) string {
	value = filepath.Clean(value)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func (w *Workspace) TopologyMode() (TopologyMode, error) {
	repositories, err := w.Repositories()
	if err != nil {
		return "", err
	}
	switch len(repositories) {
	case 0:
		return TopologyDirectory, nil
	case 1:
		return TopologySingle, nil
	default:
		return TopologyGroup, nil
	}
}

// ResolveRepository selects a repository by workspace-relative path or by a
// unique basename. A selector is mandatory for a group so Git operations can
// never silently inspect the wrong repository.
func (w *Workspace) ResolveRepository(selector string) (Repository, error) {
	repositories, err := w.Repositories()
	if err != nil {
		return Repository{}, err
	}
	selector = normalizeRepositorySelector(selector)
	if selector == "" {
		switch len(repositories) {
		case 0:
			return Repository{}, NewError(ErrRepositoryMissing, "workspace does not contain a Git repository")
		case 1:
			return repositories[0], nil
		default:
			paths := make([]string, 0, len(repositories))
			for _, repository := range repositories {
				paths = append(paths, repository.Path)
			}
			return Repository{}, NewError(ErrRepositoryNeeded, "repository is required for this workspace group; choose one of: %s", strings.Join(paths, ", "))
		}
	}

	var nameMatches []Repository
	for _, repository := range repositories {
		if equalRepositorySelector(selector, repository.Path) {
			return repository, nil
		}
		if strings.EqualFold(selector, repository.Name) {
			nameMatches = append(nameMatches, repository)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], nil
	}
	if len(nameMatches) > 1 {
		paths := make([]string, 0, len(nameMatches))
		for _, repository := range nameMatches {
			paths = append(paths, repository.Path)
		}
		return Repository{}, NewError(ErrRepositoryNeeded, "repository name %q is ambiguous; use one of: %s", selector, strings.Join(paths, ", "))
	}
	return Repository{}, NewError(ErrRepositoryMissing, "repository %q is not part of this workspace", selector)
}

func normalizeRepositorySelector(selector string) string {
	selector = strings.TrimSpace(strings.ReplaceAll(selector, `\`, "/"))
	if strings.HasPrefix(strings.ToLower(selector), "workspace:") {
		selector = selector[len("workspace:"):]
	}
	selector = strings.TrimLeft(selector, "/")
	selector = strings.TrimSuffix(selector, "/")
	if selector == "." {
		return "."
	}
	return selector
}

func equalRepositorySelector(selector, path string) bool {
	return strings.EqualFold(strings.TrimSuffix(selector, "/"), strings.TrimSuffix(path, "/"))
}

func (w *Workspace) InspectTopology(ctx context.Context) (Topology, error) {
	repositories, err := w.Repositories()
	if err != nil {
		return Topology{}, err
	}
	mode, err := w.TopologyMode()
	if err != nil {
		return Topology{}, err
	}
	topology := Topology{
		Mode: mode, RepositoryCount: len(repositories),
		Repositories: make([]RepositorySummary, 0, len(repositories)),
	}
	if len(repositories) == 1 {
		value := repositories[0].Path
		topology.DefaultRepository = &value
	}
	for _, repository := range repositories {
		marked, markerErr := w.repositoryMarker(repository.Root)
		if markerErr != nil {
			return Topology{}, markerErr
		}
		if !marked {
			return Topology{}, NewError(ErrRepositoryMissing, "repository %q has no .git marker", repository.Path)
		}
		gitInfo := w.gitInfoForRepository(ctx, repository)
		topology.Repositories = append(topology.Repositories, RepositorySummary{
			Path: repository.Path, Name: repository.Name,
			Project: detectProjectAt(repository.Root), Git: gitInfo,
		})
	}
	topology.Relations = detectRepositoryRelations(w.Root, repositories)
	return topology, nil
}

func (w *Workspace) repositoryRelativePath(repository Repository, requested string) (string, error) {
	requested = strings.TrimSpace(strings.ReplaceAll(requested, `\`, "/"))
	if requested == "" || requested == "." {
		return ".", nil
	}
	if strings.HasPrefix(strings.ToLower(requested), "workspace:") {
		requested = requested[len("workspace:"):]
	}
	requested = strings.TrimLeft(requested, "/")
	if repository.Path != "." {
		prefix := strings.TrimSuffix(repository.Path, "/") + "/"
		if strings.EqualFold(requested, repository.Path) {
			return ".", nil
		}
		if len(requested) >= len(prefix) && strings.EqualFold(requested[:len(prefix)], prefix) {
			requested = requested[len(prefix):]
		}
	}
	candidate := filepath.Join(repository.Root, filepath.FromSlash(requested))
	canonical, err := canonicalizeDeepest(candidate)
	if err != nil {
		return "", NewError(ErrInvalidPath, "cannot canonicalize repository path: %s", requested)
	}
	if !w.contains(canonical) {
		return "", NewError(ErrOutsideWorkspace, "path resolves outside the connected workspace: %s", requested)
	}
	rel, err := filepath.Rel(repository.Root, canonical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", NewError(ErrOutsideWorkspace, "path resolves outside repository %s: %s", repository.Path, requested)
	}
	rel = filepath.ToSlash(rel)
	if rel == "" {
		rel = "."
	}
	workspacePath := rel
	if repository.Path != "." && rel != "." {
		workspacePath = strings.TrimSuffix(repository.Path, "/") + "/" + rel
	} else if repository.Path != "." {
		workspacePath = repository.Path
	}
	if workspacePath != "." && w.Policy.IsSensitive(workspacePath) {
		return "", NewError(ErrSensitiveFile, "'%s' is blocked by the sensitive-file policy", workspacePath)
	}
	return rel, nil
}

func workspacePathForRepository(repository Repository, repositoryPath string) string {
	repositoryPath = strings.TrimPrefix(filepath.ToSlash(repositoryPath), "./")
	if repository.Path == "." {
		if repositoryPath == "" {
			return "."
		}
		return repositoryPath
	}
	if repositoryPath == "" || repositoryPath == "." {
		return repository.Path
	}
	return strings.TrimSuffix(repository.Path, "/") + "/" + repositoryPath
}

func wrapRepositoryError(repository Repository, err error) error {
	if err == nil {
		return nil
	}
	var workspaceErr *Error
	if errors.As(err, &workspaceErr) {
		return err
	}
	return fmt.Errorf("repository %s: %w", repository.Path, err)
}
