package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func gitRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func createLinkedWorktree(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	mainRepository := filepath.Join(root, "main")
	worktree := filepath.Join(root, "linked")
	if err := os.MkdirAll(mainRepository, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, mainRepository, "init", "-q")
	gitRun(t, mainRepository, "config", "user.email", "codexlink@example.invalid")
	gitRun(t, mainRepository, "config", "user.name", "CodexLink Test")
	writeTestFile(t, mainRepository, "README.md", "before\n")
	gitRun(t, mainRepository, "add", "README.md")
	gitRun(t, mainRepository, "commit", "-q", "-m", "initial")
	gitRun(t, mainRepository, "worktree", "add", "-q", "-b", "linked-test", worktree)
	return mainRepository, worktree
}

func TestGitLinkedWorktreeReadOnlyOperations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	_, worktree := createLinkedWorktree(t)
	writeTestFile(t, worktree, "README.md", "after\n")
	ws, err := New(worktree)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ws.GitInfoFor(context.Background(), "")
	if err != nil || !info.IsRepo {
		t.Fatalf("info=%#v err=%v", info, err)
	}
	status, err := ws.GitStatus(context.Background())
	if err != nil || len(status.Unstaged) != 1 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	diff, err := ws.GitDiff(context.Background(), GitDiffOptions{Mode: DiffUnstaged})
	if err != nil || !strings.Contains(diff.Diff, "after") {
		t.Fatalf("diff=%#v err=%v", diff, err)
	}
	log, err := ws.GitLog(context.Background(), GitLogOptions{Limit: 1})
	if err != nil || len(log.Commits) != 1 {
		t.Fatalf("log=%#v err=%v", log, err)
	}
}

func TestGitLinkedWorktreeRevalidatesCachedMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	mainRepository, worktree := createLinkedWorktree(t)
	ws, err := New(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Repositories(); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	alternates := filepath.Join(mainRepository, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.ToSlash(outside)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.GitStatus(context.Background()); workspaceErrorCode(err) != ErrOutsideWorkspace {
		t.Fatalf("cached metadata bypass error = %v", err)
	}
}

func TestGitStatusAndDiffRedactSensitivePaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	writeTestFile(t, root, "safe.txt", "before\n")
	writeTestFile(t, root, ".env", "TOKEN=before\n")
	gitRun(t, root, "add", "safe.txt", ".env")
	gitRun(t, root, "commit", "-qm", "initial")
	writeTestFile(t, root, "safe.txt", "after\n")
	writeTestFile(t, root, ".env", "TOKEN=after\n")
	writeTestFile(t, root, "new.txt", "new\n")

	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	status, err := ws.GitStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsRepo || status.Redacted != 1 || len(status.Unstaged) != 1 || status.Unstaged[0].Path != "safe.txt" || len(status.Untracked) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	diff, err := ws.GitDiff(context.Background(), GitDiffOptions{Mode: DiffUnstaged, MaxBytes: 64 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !diff.IsRepo || diff.RedactedFiles != 1 || !strings.Contains(diff.Diff, "safe.txt") || strings.Contains(diff.Diff, ".env") || strings.Contains(diff.Diff, "TOKEN") {
		t.Fatalf("unsafe or incomplete diff: %#v", diff)
	}
	gitRun(t, root, "add", "safe.txt")
	staged, err := ws.GitDiff(context.Background(), GitDiffOptions{Mode: DiffStaged, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged.Diff, "safe.txt") {
		t.Fatalf("staged diff missing: %#v", staged)
	}
}

func TestGitToolsRequireRepositoryForWorkspaceGroup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "init", "-q")
		gitRun(t, repo, "config", "user.email", "test@example.com")
		gitRun(t, repo, "config", "user.name", "Test")
		writeTestFile(t, repo, "main.txt", "before\n")
		gitRun(t, repo, "add", "main.txt")
		gitRun(t, repo, "commit", "-qm", "initial")
	}
	writeTestFile(t, filepath.Join(root, "web"), "main.txt", "after\n")
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.GitStatus(context.Background()); workspaceErrorCode(err) != ErrRepositoryNeeded {
		t.Fatalf("status without selector error = %v", err)
	}
	status, err := ws.GitStatusFor(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if status.Repository != "web" || len(status.Unstaged) != 1 || status.Unstaged[0].Path != "main.txt" {
		t.Fatalf("status = %#v", status)
	}
	diff, err := ws.GitDiff(context.Background(), GitDiffOptions{Repository: "web", Mode: DiffHead})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Repository != "web" || !strings.Contains(diff.Diff, "main.txt") {
		t.Fatalf("diff = %#v", diff)
	}
	if _, err := ws.GitDiff(context.Background(), GitDiffOptions{Repository: "missing"}); workspaceErrorCode(err) != ErrRepositoryMissing {
		t.Fatalf("unknown repository error = %v", err)
	}
}

func TestGitInspectionDisablesRepositoryFsmonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fsmonitor test is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	writeTestFile(t, root, "safe.txt", "before\n")
	gitRun(t, root, "add", "safe.txt")
	gitRun(t, root, "commit", "-qm", "initial")
	marker := filepath.Join(root, "fsmonitor-ran")
	hook := filepath.Join(root, "fsmonitor.sh")
	script := "#!/bin/sh\nprintf ran > " + strconv.Quote(marker) + "\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "config", "core.fsmonitor", hook)

	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.GitStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository-controlled fsmonitor executed; stat error = %v", err)
	}
}

func TestGitInspectionIgnoresInheritedRepositoryOverrides(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	other := t.TempDir()
	for _, repo := range []string{root, other} {
		gitRun(t, repo, "init", "-q")
		gitRun(t, repo, "config", "user.email", "test@example.com")
		gitRun(t, repo, "config", "user.name", "Test")
		writeTestFile(t, repo, "tracked.txt", "before\n")
		gitRun(t, repo, "add", "tracked.txt")
		gitRun(t, repo, "commit", "-qm", "initial")
	}
	writeTestFile(t, root, "tracked.txt", "root-change\n")
	writeTestFile(t, other, "tracked.txt", "other-change\n")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	status, err := ws.GitStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Unstaged) != 1 || status.Unstaged[0].Path != "tracked.txt" {
		t.Fatalf("status was redirected by inherited Git environment: %#v", status)
	}
	diff, err := ws.GitDiff(context.Background(), GitDiffOptions{Mode: DiffHead})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "root-change") || strings.Contains(diff.Diff, "other-change") {
		t.Fatalf("diff was redirected by inherited Git environment: %s", diff.Diff)
	}
}

func TestGitInspectionDisablesRepositoryCleanFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script filter test is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	marker := filepath.Join(root, "filter-ran")
	filter := filepath.Join(root, "filter.sh")
	script := "#!/bin/sh\nprintf ran > " + strconv.Quote(marker) + "\ncat\n"
	if err := os.WriteFile(filter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "config", "filter.evil.clean", filter)
	gitRun(t, root, "config", "filter.evil.smudge", "cat")
	writeTestFile(t, root, ".gitattributes", "*.txt filter=evil\n")
	writeTestFile(t, root, "safe.txt", "before\n")
	// Use -c to avoid executing the repository filter while constructing the
	// fixture. CodexLink itself must remain safe without this test-only override.
	gitRun(t, root, "-c", "filter.evil.clean=", "add", ".gitattributes", "safe.txt")
	gitRun(t, root, "commit", "-qm", "initial")
	_ = os.Remove(marker)
	writeTestFile(t, root, "safe.txt", "after\n")

	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.GitStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	diff, err := ws.GitDiff(context.Background(), GitDiffOptions{Mode: DiffHead})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "after") {
		t.Fatalf("safe diff missing: %s", diff.Diff)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository-controlled clean filter executed; stat error = %v", err)
	}
}

func TestGitDiffPreservesCaseSensitiveRepositoryPaths(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("case-sensitive path behavior is exercised on case-sensitive platforms")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	writeTestFile(t, root, "Source/Main.go", "package source\n")
	gitRun(t, root, "add", "Source/Main.go")
	gitRun(t, root, "commit", "-qm", "initial")
	writeTestFile(t, root, "Source/Main.go", "package changed\n")

	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := ws.GitDiff(context.Background(), GitDiffOptions{Path: "workspace:/Source/Main.go", Mode: DiffHead})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "Source/Main.go") {
		t.Fatalf("case-sensitive path was lost: %s", diff.Diff)
	}
}
