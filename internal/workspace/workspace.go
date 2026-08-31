package workspace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

type ErrorCode string

const (
	ErrInvalidPath       ErrorCode = "INVALID_PATH"
	ErrOutsideWorkspace  ErrorCode = "PATH_OUTSIDE_WORKSPACE"
	ErrSensitiveFile     ErrorCode = "ACCESS_DENIED_SENSITIVE_FILE"
	ErrFileNotFound      ErrorCode = "FILE_NOT_FOUND"
	ErrNotFile           ErrorCode = "NOT_A_FILE"
	ErrNotDirectory      ErrorCode = "NOT_A_DIRECTORY"
	ErrBinaryFile        ErrorCode = "BINARY_FILE"
	ErrFileTooLarge      ErrorCode = "FILE_TOO_LARGE"
	ErrInvalidExpression ErrorCode = "INVALID_EXPRESSION"
	ErrInvalidConfig     ErrorCode = "INVALID_CONFIG"
	ErrRepositoryNeeded  ErrorCode = "REPOSITORY_REQUIRED"
	ErrRepositoryMissing ErrorCode = "REPOSITORY_NOT_FOUND"
	ErrRepositoryLimit   ErrorCode = "REPOSITORY_LIMIT_EXCEEDED"
)

type Error struct {
	Code ErrorCode
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func NewError(code ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

type ProjectConfig struct {
	Name            string   `json:"name,omitempty"`
	MaxIterations   int      `json:"maxIterations,omitempty"`
	ChatGPTProfile  string   `json:"chatgptProfile,omitempty"`
	RepositoryDepth int      `json:"repositoryDepth,omitempty"`
	Repositories    []string `json:"repositories,omitempty"`
}

type Workspace struct {
	Root          string
	ID            string
	Name          string
	Policy        Policy
	ProjectConfig ProjectConfig

	repoOnce     sync.Once
	repositories []Repository
	repoErr      error
}

func New(rootInput string) (*Workspace, error) {
	absolute, err := filepath.Abs(rootInput)
	if err != nil {
		return nil, NewError(ErrInvalidPath, "invalid workspace path: %v", err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, NewError(ErrFileNotFound, "workspace root does not exist: %s", rootInput)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, NewError(ErrFileNotFound, "workspace root does not exist: %s", rootInput)
	}
	if !info.IsDir() {
		return nil, NewError(ErrNotDirectory, "workspace root is not a directory: %s", rootInput)
	}
	real = filepath.Clean(real)
	identity := real
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	digest := sha256.Sum256([]byte(identity))
	id := hex.EncodeToString(digest[:])[:12]
	cfg, err := loadProjectConfig(real)
	if err != nil {
		return nil, err
	}
	name := cfg.Name
	if name == "" {
		name = filepath.Base(real)
	}
	return &Workspace{Root: real, ID: id, Name: name, Policy: NewPolicy(real), ProjectConfig: cfg}, nil
}

const (
	defaultMaxIterations   = 12
	defaultRepositoryDepth = 3
	maxRepositoryDepth     = 6
	maxRepositories        = 128
)

func loadProjectConfig(root string) (ProjectConfig, error) {
	cfg := ProjectConfig{}
	var source string
	for _, name := range []string{".codexlink.json", ".c2c.json"} {
		path := filepath.Join(root, name)
		data, err := readBoundedRegularFile(path, 256*1024)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ProjectConfig{}, NewError(ErrInvalidConfig, "cannot read %s: %v", name, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return ProjectConfig{}, NewError(ErrInvalidConfig, "invalid %s: %v", name, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return ProjectConfig{}, NewError(ErrInvalidConfig, "%s must contain exactly one JSON object", name)
			}
			return ProjectConfig{}, NewError(ErrInvalidConfig, "invalid trailing data in %s: %v", name, err)
		}
		source = name
		break
	}
	if source == "" {
		cfg.MaxIterations = defaultMaxIterations
		cfg.ChatGPTProfile = "current"
		cfg.RepositoryDepth = defaultRepositoryDepth
		return cfg, nil
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	if len([]rune(cfg.Name)) > 160 || strings.ContainsAny(cfg.Name, "\r\n\x00") {
		return ProjectConfig{}, NewError(ErrInvalidConfig, "%s name must be a single line of at most 160 characters", source)
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = defaultMaxIterations
	} else if cfg.MaxIterations < 1 || cfg.MaxIterations > 50 {
		return ProjectConfig{}, NewError(ErrInvalidConfig, "%s maxIterations must be between 1 and 50", source)
	}
	cfg.ChatGPTProfile = strings.TrimSpace(cfg.ChatGPTProfile)
	switch cfg.ChatGPTProfile {
	case "":
		cfg.ChatGPTProfile = "current"
	case "current", "fast", "balanced", "deep", "pro":
	default:
		return ProjectConfig{}, NewError(ErrInvalidConfig, "%s chatgptProfile must be current, fast, balanced, deep, or pro", source)
	}
	if cfg.RepositoryDepth == 0 {
		cfg.RepositoryDepth = defaultRepositoryDepth
	} else if cfg.RepositoryDepth < 1 || cfg.RepositoryDepth > maxRepositoryDepth {
		return ProjectConfig{}, NewError(ErrInvalidConfig, "%s repositoryDepth must be between 1 and %d", source, maxRepositoryDepth)
	}
	if len(cfg.Repositories) > maxRepositories {
		return ProjectConfig{}, NewError(ErrInvalidConfig, "%s repositories may contain at most %d entries", source, maxRepositories)
	}
	seen := make(map[string]struct{}, len(cfg.Repositories))
	for index, repository := range cfg.Repositories {
		repository = strings.TrimSpace(strings.ReplaceAll(repository, `\`, "/"))
		if repository == "" || strings.ContainsRune(repository, '\x00') {
			return ProjectConfig{}, NewError(ErrInvalidConfig, "%s repositories[%d] is invalid", source, index)
		}
		repository = strings.TrimSuffix(repository, "/")
		if repository == "" {
			repository = "."
		}
		if _, exists := seen[repository]; exists {
			return ProjectConfig{}, NewError(ErrInvalidConfig, "%s repositories contains duplicate path %q", source, repository)
		}
		seen[repository] = struct{}{}
		cfg.Repositories[index] = repository
	}
	return cfg, nil
}

func (w *Workspace) contains(candidate string) bool {
	root := filepath.Clean(w.Root)
	candidate = filepath.Clean(candidate)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		candidate = strings.ToLower(candidate)
	}
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func canonicalizeDeepest(abs string) (string, error) {
	current := filepath.Clean(abs)
	suffix := make([]string, 0, 4)
	for {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{real}, suffix...)
			return filepath.Join(parts...), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}

func (w *Workspace) Resolve(requested string, allowSensitive bool) (absolute, relative string, err error) {
	if strings.ContainsRune(requested, '\x00') {
		return "", "", NewError(ErrInvalidPath, "path contains a NUL byte")
	}
	value := strings.TrimSpace(requested)
	value = strings.ReplaceAll(value, `\`, "/")
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "workspace:") {
		value = strings.TrimLeft(value[len("workspace:"):], "/")
	}
	if value == "" || value == "/" {
		value = "."
	}
	joined := filepath.Join(w.Root, filepath.FromSlash(value))
	if filepath.IsAbs(filepath.FromSlash(value)) {
		joined = filepath.Clean(filepath.FromSlash(value))
	}
	canonical, canonicalErr := canonicalizeDeepest(joined)
	if canonicalErr != nil {
		return "", "", NewError(ErrInvalidPath, "cannot canonicalize path: %s", requested)
	}
	if !w.contains(canonical) {
		return "", "", NewError(ErrOutsideWorkspace, "path resolves outside the connected workspace: %s", requested)
	}
	rel, relErr := filepath.Rel(w.Root, canonical)
	if relErr != nil {
		return "", "", NewError(ErrOutsideWorkspace, "path resolves outside the connected workspace: %s", requested)
	}
	rel = filepath.ToSlash(rel)
	if rel == "" {
		rel = "."
	}
	if !allowSensitive && rel != "." && w.Policy.IsSensitive(rel) {
		return "", "", NewError(ErrSensitiveFile, "'%s' is blocked by the sensitive-file policy", rel)
	}
	return canonical, rel, nil
}

type ReadFileOptions struct {
	StartLine int
	EndLine   int
	MaxLines  int
	MaxBytes  int
}

type ReadFileResult struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"sizeBytes"`
	TotalLines     int    `json:"totalLines"`
	StartLine      int    `json:"startLine"`
	EndLine        int    `json:"endLine"`
	Truncated      bool   `json:"truncated"`
	RemainingLines int    `json:"remainingLines"`
	NextStartLine  *int   `json:"nextStartLine"`
	Content        string `json:"content"`
}

const (
	defaultMaxLines = 400
	hardMaxLines    = 2000
	defaultMaxBytes = 256 * 1024
	hardMaxBytes    = 1024 * 1024
)

func (w *Workspace) ReadFile(requested string, options ReadFileOptions) (ReadFileResult, error) {
	absolute, relative, err := w.Resolve(requested, false)
	if err != nil {
		return ReadFileResult{}, err
	}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return ReadFileResult{}, NewError(ErrFileNotFound, "file not found: %s", relative)
	}
	if err != nil {
		return ReadFileResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ReadFileResult{}, NewError(ErrNotFile, "not a regular file: %s", relative)
	}
	binary, err := isBinary(absolute)
	if err != nil {
		return ReadFileResult{}, err
	}
	if binary {
		return ReadFileResult{}, NewError(ErrBinaryFile, "binary file (%d bytes): %s", info.Size(), relative)
	}
	start := options.StartLine
	if start < 1 {
		start = 1
	}
	maxLines := options.MaxLines
	if maxLines < 1 {
		maxLines = defaultMaxLines
	}
	if maxLines > hardMaxLines {
		maxLines = hardMaxLines
	}
	end := options.EndLine
	if end < start {
		end = start + maxLines - 1
	}
	if end > start+hardMaxLines-1 {
		end = start + hardMaxLines - 1
	}
	maxBytes := options.MaxBytes
	if maxBytes < 1024 {
		maxBytes = defaultMaxBytes
	}
	if maxBytes > hardMaxBytes {
		maxBytes = hardMaxBytes
	}

	file, err := os.Open(absolute)
	if err != nil {
		return ReadFileResult{}, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	selected := make([]string, 0, maxLines)
	total := 0
	actualEnd := start - 1
	bytesUsed := 0
	byteTruncated := false
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			total++
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if total >= start && total <= end && !byteTruncated {
				cost := len([]byte(line))
				if len(selected) > 0 {
					cost++
				}
				if bytesUsed+cost <= maxBytes {
					selected = append(selected, line)
					bytesUsed += cost
					actualEnd = total
				} else if len(selected) == 0 {
					prefix := utf8SafePrefix([]byte(line), maxBytes)
					selected = append(selected, string(prefix))
					bytesUsed = len(prefix)
					actualEnd = total
					byteTruncated = true
				} else {
					byteTruncated = true
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return ReadFileResult{}, readErr
			}
			break
		}
	}
	remaining := total - actualEnd
	if remaining < 0 {
		remaining = 0
	}
	var next *int
	if remaining > 0 || byteTruncated {
		value := actualEnd + 1
		if byteTruncated && len(selected) == 1 && actualEnd == start {
			// A single overlong line cannot be resumed by line number. Report no
			// continuation rather than returning the same line forever.
			value = 0
		}
		if value > 0 {
			next = &value
		}
	}
	return ReadFileResult{
		Path:           relative,
		SizeBytes:      info.Size(),
		TotalLines:     total,
		StartLine:      start,
		EndLine:        actualEnd,
		Truncated:      remaining > 0 || byteTruncated,
		RemainingLines: remaining,
		NextStartLine:  next,
		Content:        strings.Join(selected, "\n"),
	}, nil
}

func utf8SafePrefix(data []byte, limit int) []byte {
	if len(data) <= limit {
		return data
	}
	end := limit
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return data[:end]
}

func isBinary(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 8192)
	count, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	for _, value := range buffer[:count] {
		if value == 0 {
			return true, nil
		}
	}
	return false, nil
}

type DirectoryEntry struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	SizeBytes *int64 `json:"sizeBytes,omitempty"`
}

type ListOptions struct {
	Depth  int
	Limit  int
	Offset int
}

type ListResult struct {
	Path            string           `json:"path"`
	Entries         []DirectoryEntry `json:"entries"`
	Total           int              `json:"total"`
	Offset          int              `json:"offset"`
	Limit           int              `json:"limit"`
	HasMore         bool             `json:"hasMore"`
	SkippedSymlinks int              `json:"skippedSymlinks,omitempty"`
}

func (w *Workspace) ListDirectory(requested string, options ListOptions) (ListResult, error) {
	absolute, relative, err := w.Resolve(requested, false)
	if err != nil {
		return ListResult{}, err
	}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return ListResult{}, NewError(ErrFileNotFound, "directory not found: %s", relative)
	}
	if err != nil {
		return ListResult{}, err
	}
	if !info.IsDir() {
		return ListResult{}, NewError(ErrNotDirectory, "not a directory: %s", relative)
	}
	depth := options.Depth
	if depth < 1 {
		depth = 1
	}
	if depth > 4 {
		depth = 4
	}
	limit := options.Limit
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	all := make([]DirectoryEntry, 0, limit)
	skippedSymlinks := 0
	hardCap := offset + limit + 2000
	var walk func(string, string, int) error
	walk = func(dirAbsolute, dirRelative string, level int) error {
		entries, readErr := os.ReadDir(dirAbsolute)
		if readErr != nil {
			return nil
		}
		sort.Slice(entries, func(i, j int) bool {
			iDir, jDir := entries[i].IsDir(), entries[j].IsDir()
			if iDir != jDir {
				return iDir
			}
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		for _, entry := range entries {
			if len(all) >= hardCap {
				return nil
			}
			childRel := entry.Name()
			if dirRelative != "" && dirRelative != "." {
				childRel = strings.TrimSuffix(dirRelative, "/") + "/" + entry.Name()
			}
			if w.Policy.IsHidden(childRel) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				skippedSymlinks++
				continue
			}
			childAbs := filepath.Join(dirAbsolute, entry.Name())
			if entry.IsDir() {
				all = append(all, DirectoryEntry{Path: childRel + "/", Type: "dir"})
				if level < depth {
					_ = walk(childAbs, childRel, level+1)
				}
				continue
			}
			entryInfo, statErr := entry.Info()
			if statErr == nil && entryInfo.Mode().IsRegular() {
				size := entryInfo.Size()
				all = append(all, DirectoryEntry{Path: childRel, Type: "file", SizeBytes: &size})
			}
		}
		return nil
	}
	walkRel := relative
	if walkRel == "." {
		walkRel = ""
	}
	if err := walk(absolute, walkRel, 1); err != nil {
		return ListResult{}, err
	}
	start := offset
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := append([]DirectoryEntry(nil), all[start:end]...)
	return ListResult{
		Path:            relative,
		Entries:         page,
		Total:           len(all),
		Offset:          offset,
		Limit:           limit,
		HasMore:         end < len(all),
		SkippedSymlinks: skippedSymlinks,
	}, nil
}

type ProjectInfo struct {
	ProjectType    string            `json:"projectType"`
	Languages      []string          `json:"languages"`
	Frameworks     []string          `json:"frameworks"`
	PackageManager *string           `json:"packageManager"`
	Scripts        map[string]string `json:"scripts"`
}

func (w *Workspace) DetectProject() ProjectInfo {
	return detectProjectAt(w.Root)
}

func (w *Workspace) DetectProjectAt(root string) ProjectInfo {
	return detectProjectAt(root)
}

func detectProjectAt(root string) ProjectInfo {
	has := func(name string) bool {
		info, err := os.Lstat(filepath.Join(root, name))
		return err == nil && info.Mode().IsRegular()
	}
	languages := map[string]bool{}
	frameworks := map[string]bool{}
	projectType := "unknown"
	scripts := map[string]string{}
	var packageManager *string
	if has("package.json") {
		projectType = "node"
		languages["JavaScript"] = true
		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if data, err := readBoundedRegularFile(filepath.Join(root, "package.json"), 1024*1024); err == nil {
			_ = json.Unmarshal(data, &pkg)
		}
		if pkg.Scripts != nil {
			scripts = pkg.Scripts
		}
		known := map[string]string{
			"next": "Next.js", "react": "React", "vue": "Vue", "svelte": "Svelte",
			"express": "Express", "fastify": "Fastify", "@nestjs/core": "NestJS",
			"electron": "Electron", "vitest": "Vitest", "jest": "Jest",
		}
		for dependency, label := range known {
			if _, ok := pkg.Dependencies[dependency]; ok {
				frameworks[label] = true
			}
			if _, ok := pkg.DevDependencies[dependency]; ok {
				frameworks[label] = true
			}
		}
		manager := "npm"
		switch {
		case has("pnpm-lock.yaml"):
			manager = "pnpm"
		case has("yarn.lock"):
			manager = "yarn"
		case has("bun.lock"), has("bun.lockb"):
			manager = "bun"
		}
		packageManager = &manager
	}
	if has("tsconfig.json") {
		languages["TypeScript"] = true
	}
	if has("pyproject.toml") || has("requirements.txt") || has("setup.py") {
		languages["Python"] = true
		if projectType == "unknown" {
			projectType = "python"
		}
	}
	if has("Cargo.toml") {
		languages["Rust"] = true
		if projectType == "unknown" {
			projectType = "rust"
		}
	}
	if has("go.mod") {
		languages["Go"] = true
		if projectType == "unknown" {
			projectType = "go"
		}
	}
	if has("Package.swift") {
		languages["Swift"] = true
		if projectType == "unknown" {
			projectType = "swift"
		}
	}
	if has("pom.xml") {
		languages["Java"] = true
		frameworks["Maven"] = true
		if projectType == "unknown" {
			projectType = "jvm"
		}
	}
	if has("build.gradle") || has("build.gradle.kts") {
		languages["Java/Kotlin"] = true
		frameworks["Gradle"] = true
		if projectType == "unknown" {
			projectType = "jvm"
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "*.csproj")); hasRegularMatch(matches) {
		languages["C#"] = true
		frameworks[".NET"] = true
		if projectType == "unknown" {
			projectType = "dotnet"
		}
	}
	languageList := make([]string, 0, len(languages))
	for language := range languages {
		languageList = append(languageList, language)
	}
	frameworkList := make([]string, 0, len(frameworks))
	for framework := range frameworks {
		frameworkList = append(frameworkList, framework)
	}
	sort.Strings(languageList)
	sort.Strings(frameworkList)
	return ProjectInfo{ProjectType: projectType, Languages: languageList, Frameworks: frameworkList, PackageManager: packageManager, Scripts: scripts}
}

func hasRegularMatch(paths []string) bool {
	for _, candidate := range paths {
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
