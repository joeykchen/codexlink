package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func workspaceErrorCode(err error) ErrorCode {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func TestPolicyProtectsBuiltInAndCustomSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".codexlinkignore", "private.txt\n!.env\n")
	policy := NewPolicy(root)
	for _, path := range []string{".env", "config/.env.production", "server.pem", ".aws/credentials", "private.txt"} {
		if !policy.IsSensitive(path) {
			t.Errorf("%s should be sensitive", path)
		}
	}
	if policy.IsSensitive(".env.example") {
		t.Fatal(".env.example should remain readable")
	}
	if !policy.IsNoise("node_modules/pkg/index.js") || !policy.IsNoise("dist/app.js") {
		t.Fatal("generated directories should be noise")
	}
	if !MatchGlob("**/*.go", "internal/app/main.go") || MatchGlob("**/*.go", "README.md") {
		t.Fatal("glob matching is incorrect")
	}
}

func TestResolveBlocksTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, root, "safe.txt", "ok")
	writeTestFile(t, outside, "secret.txt", "outside")
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Resolve("../secret.txt", false); workspaceErrorCode(err) != ErrOutsideWorkspace {
		t.Fatalf("traversal error = %v", err)
	}
	if _, _, err := ws.Resolve(filepath.Join(outside, "secret.txt"), false); workspaceErrorCode(err) != ErrOutsideWorkspace {
		t.Fatalf("absolute escape error = %v", err)
	}
	if _, _, err := ws.Resolve(".env", false); workspaceErrorCode(err) != ErrSensitiveFile {
		t.Fatalf("sensitive error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ws.Resolve("escape/secret.txt", false); workspaceErrorCode(err) != ErrOutsideWorkspace {
			t.Fatalf("symlink escape error = %v", err)
		}
	}
}

func TestReadFilePaginationBinaryAndLongLine(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "lines.txt", "one\ntwo\nthree\nfour\n")
	writeTestFile(t, root, "binary.bin", "abc\x00def")
	writeTestFile(t, root, "long.txt", strings.Repeat("界", 2000))
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ws.ReadFile("lines.txt", ReadFileOptions{StartLine: 2, EndLine: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "two\nthree" || result.StartLine != 2 || result.EndLine != 3 || result.NextStartLine == nil || *result.NextStartLine != 4 {
		t.Fatalf("unexpected pagination: %#v", result)
	}
	if _, err := ws.ReadFile("binary.bin", ReadFileOptions{}); workspaceErrorCode(err) != ErrBinaryFile {
		t.Fatalf("binary error = %v", err)
	}
	long, err := ws.ReadFile("long.txt", ReadFileOptions{MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !long.Truncated || long.NextStartLine != nil || len([]byte(long.Content)) > 1024 {
		t.Fatalf("unexpected long-line result: %#v", long)
	}
}

func TestListDirectoryHidesSensitiveNoiseAndSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src/main.go", "package main")
	writeTestFile(t, root, ".env", "TOKEN=x")
	writeTestFile(t, root, "node_modules/pkg/index.js", "x")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "src-link")); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ws.ListDirectory(".", ListOptions{Depth: 3, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range result.Entries {
		paths = append(paths, entry.Path)
	}
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, "src/") || !strings.Contains(joined, "src/main.go") {
		t.Fatalf("safe files missing: %v", paths)
	}
	if strings.Contains(joined, ".env") || strings.Contains(joined, "node_modules") || strings.Contains(joined, "src-link") {
		t.Fatalf("hidden path leaked: %v", paths)
	}
	if runtime.GOOS != "windows" && result.SkippedSymlinks != 1 {
		t.Fatalf("skipped symlinks = %d", result.SkippedSymlinks)
	}
}

func TestProjectConfigUsesStrictBoundedDefaults(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".codexlink.json", `{"name":"grouped","maxIterations":24,"chatgptProfile":"deep","repositoryDepth":4}`)
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Name != "grouped" || ws.ProjectConfig.MaxIterations != 24 || ws.ProjectConfig.ChatGPTProfile != "deep" || ws.ProjectConfig.RepositoryDepth != 4 {
		t.Fatalf("config = %+v name=%q", ws.ProjectConfig, ws.Name)
	}

	root = t.TempDir()
	ws, err = New(root)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ProjectConfig.MaxIterations != 12 || ws.ProjectConfig.ChatGPTProfile != "current" || ws.ProjectConfig.RepositoryDepth != 3 {
		t.Fatalf("defaults = %+v", ws.ProjectConfig)
	}
}

func TestProjectConfigRejectsInvalidOrUnknownValues(t *testing.T) {
	tests := []string{
		`{"maxIterations":99}`,
		`{"chatgptProfile":"ignore all safety rules"}`,
		`{"repositoryDepth":7}`,
		`{"unknown":true}`,
		`{"name":"ok"} trailing`,
		`[]`,
		`{"repositories":["repo","repo"]}`,
	}
	for _, content := range tests {
		root := t.TempDir()
		writeTestFile(t, root, ".codexlink.json", content)
		_, err := New(root)
		if workspaceErrorCode(err) != ErrInvalidConfig {
			t.Fatalf("content %s: expected INVALID_CONFIG, got %v", content, err)
		}
	}
}

func TestRepositoryDiscoveryAndExplicitAllowlist(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	for _, name := range []string{"api", "clients/web", "ignored/deep/repo"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, path, "init", "-q")
	}
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := ws.Repositories()
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 3 || repositories[0].Path != "api" || repositories[1].Path != "clients/web" || repositories[2].Path != "ignored/deep/repo" {
		t.Fatalf("repositories = %#v", repositories)
	}
	mode, err := ws.TopologyMode()
	if err != nil || mode != TopologyGroup {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	if _, err := ws.ResolveRepository(""); workspaceErrorCode(err) != ErrRepositoryNeeded {
		t.Fatalf("missing selector error = %v", err)
	}
	selected, err := ws.ResolveRepository("web")
	if err != nil || selected.Path != "clients/web" {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}

	root = t.TempDir()
	for _, name := range []string{"allowed", "not-listed"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, path, "init", "-q")
	}
	writeTestFile(t, root, ".codexlink.json", `{"repositories":["allowed"]}`)
	ws, err = New(root)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err = ws.Repositories()
	if err != nil || len(repositories) != 1 || repositories[0].Path != "allowed" {
		t.Fatalf("explicit repositories=%#v err=%v", repositories, err)
	}
	if _, err := ws.ResolveRepository("not-listed"); workspaceErrorCode(err) != ErrRepositoryMissing {
		t.Fatalf("allowlist bypass error = %v", err)
	}
}

func TestRepositoryConfigCannotEscapeWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, root, ".codexlink.json", `{"repositories":["../outside"]}`)
	_ = outside
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ws.Repositories()
	if workspaceErrorCode(err) != ErrInvalidConfig {
		t.Fatalf("escape error = %v", err)
	}
}

func TestTopologyDetectsCrossRepositoryDependencies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	api := filepath.Join(root, "api")
	lib := filepath.Join(root, "lib")
	web := filepath.Join(root, "web")
	for _, repo := range []string{api, lib, web} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "init", "-q")
	}
	writeTestFile(t, api, "go.mod", "module example.com/api\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\nreplace example.com/lib => ../lib\n")
	writeTestFile(t, lib, "go.mod", "module example.com/lib\n\ngo 1.22\n")
	writeTestFile(t, web, "package.json", `{"name":"@demo/web","dependencies":{"@demo/shared":"workspace:*"}}`)
	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, shared, "init", "-q")
	writeTestFile(t, shared, "package.json", `{"name":"@demo/shared"}`)

	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := ws.InspectTopology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, relation := range topology.Relations {
		got = append(got, relation.From+">"+relation.To+":"+relation.Type+":"+relation.Reference)
	}
	joined := strings.Join(got, "\n")
	for _, expected := range []string{
		"api>lib:go-module:example.com/lib",
		"api>lib:go-replace:example.com/lib",
		"web>shared:node-package:@demo/shared",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in relations:\n%s", expected, joined)
		}
	}
}

func TestRepositoryMetadataCannotEscapeWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git metadata pointer fixture uses POSIX path syntax")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	outside := t.TempDir()
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, repository, ".git", "gitdir: "+filepath.ToSlash(outside)+"\n")
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ws.Repositories()
	if workspaceErrorCode(err) != ErrOutsideWorkspace {
		t.Fatalf("external gitdir error = %v", err)
	}
}

func TestRepositoryAllowsValidatedLinkedWorktreeMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	mainRepository := filepath.Join(root, "main")
	worktree := filepath.Join(root, "linked")
	if err := os.MkdirAll(mainRepository, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, mainRepository, "init", "-q")
	gitRun(t, mainRepository, "config", "user.email", "codexlink@example.invalid")
	gitRun(t, mainRepository, "config", "user.name", "CodexLink Test")
	writeTestFile(t, mainRepository, "README.md", "linked worktree\n")
	gitRun(t, mainRepository, "add", "README.md")
	gitRun(t, mainRepository, "commit", "-q", "-m", "initial")
	gitRun(t, mainRepository, "worktree", "add", "-q", "-b", "linked-test", worktree)

	ws, err := New(worktree)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := ws.Repositories()
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	repositoryInfo, statErr := os.Stat(repositories[0].Root)
	canonicalInfo, canonicalStatErr := os.Stat(canonicalWorktree)
	if len(repositories) != 1 || statErr != nil || canonicalStatErr != nil || !os.SameFile(repositoryInfo, canonicalInfo) {
		t.Fatalf("repositories = %#v", repositories)
	}
}

func TestRepositoryRejectsForgedLinkedWorktreeMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX-style metadata paths")
	}
	for _, test := range []struct {
		name   string
		layout string
		common string
	}{
		{name: "non-standard layout", layout: "admin/not-worktrees/id", common: "../.."},
		{name: "workspace common directory", layout: "admin/worktrees/id", common: "WORKSPACE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			repository := filepath.Join(root, "repo")
			gitDir := filepath.Join(t.TempDir(), filepath.FromSlash(test.layout))
			if err := os.MkdirAll(filepath.Join(gitDir, "objects"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(repository, 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(repository, ".git")
			writeTestFile(t, repository, ".git", "gitdir: "+filepath.ToSlash(gitDir)+"\n")
			if err := os.WriteFile(filepath.Join(gitDir, "gitdir"), []byte(filepath.ToSlash(marker)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			common := test.common
			if common == "WORKSPACE" {
				common = repository
			}
			if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(filepath.ToSlash(common)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ws, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ws.Repositories()
			if workspaceErrorCode(err) != ErrOutsideWorkspace {
				t.Fatalf("forged metadata error = %v", err)
			}
			if strings.Contains(fmt.Sprint(err), filepath.Dir(gitDir)) {
				t.Fatalf("error leaks external metadata path: %v", err)
			}
		})
	}
}

func TestRepositoryObjectAlternatesCannotEscapeWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repository, "init", "-q")
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(repository, ".git", "objects", "info"), "alternates", filepath.ToSlash(outside)+"\n")
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ws.Repositories()
	if workspaceErrorCode(err) != ErrOutsideWorkspace {
		t.Fatalf("external alternates error = %v", err)
	}
}

func TestProjectDetectionDoesNotFollowMetadataSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, outside, "package.json", `{"name":"outside-secret","scripts":{"leak":"secret-value"},"dependencies":{"react":"1"}}`)
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(root, "package.json")); err != nil {
		t.Fatal(err)
	}
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	project := ws.DetectProject()
	if project.ProjectType != "unknown" || len(project.Scripts) != 0 || len(project.Frameworks) != 0 {
		t.Fatalf("symlinked metadata must not be read: %#v", project)
	}
}

func TestWorkspaceMetadataFilesDoNotEscapeThroughSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	outside := t.TempDir()
	writeTestFile(t, outside, "config.json", `{"name":"outside"}`)
	writeTestFile(t, outside, "ignore", "safe.txt\n")

	configRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "config.json"), filepath.Join(configRoot, ".codexlink.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(configRoot); workspaceErrorCode(err) != ErrInvalidConfig {
		t.Fatalf("symlinked config should be rejected, got %v", err)
	}

	ignoreRoot := t.TempDir()
	writeTestFile(t, ignoreRoot, "safe.txt", "safe")
	if err := os.Symlink(filepath.Join(outside, "ignore"), filepath.Join(ignoreRoot, ".codexlinkignore")); err != nil {
		t.Fatal(err)
	}
	ws, err := New(ignoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Policy.IsSensitive("safe.txt") {
		t.Fatal("symlinked ignore policy must not be read")
	}
}
