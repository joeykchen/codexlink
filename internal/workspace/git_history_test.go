package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitLogCurrentHeadPaginationAndPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	writeTestFile(t, root, "a.txt", "one\n")
	gitRun(t, root, "add", "a.txt")
	gitRun(t, root, "commit", "-qm", "first")
	writeTestFile(t, root, "b.txt", "two\n")
	gitRun(t, root, "add", "b.txt")
	gitRun(t, root, "commit", "-qm", "second\tcontrol")
	writeTestFile(t, root, "a.txt", "three\n")
	gitRun(t, root, "add", "a.txt")
	gitRun(t, root, "commit", "-qm", "third")
	ws, _ := New(root)
	page, err := ws.GitLog(context.Background(), GitLogOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Returned != 2 || !page.HasMore || page.NextOffset == nil || page.Commits[0].Subject != "third" || strings.ContainsAny(page.Commits[1].Subject, "\t\n\r") {
		t.Fatalf("page = %#v", page)
	}
	for _, commit := range page.Commits {
		if len(commit.Hash) != 40 || strings.ContainsAny(commit.Hash, " \t\n\r") {
			t.Fatalf("invalid commit hash %q", commit.Hash)
		}
	}
	pathLog, err := ws.GitLog(context.Background(), GitLogOptions{Path: "a.txt", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if pathLog.Returned != 2 || pathLog.Path != "a.txt" {
		t.Fatalf("path log = %#v", pathLog)
	}
}

func TestGitLogRejectsOversizedOutputPromptly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	messagePath := filepath.Join(root, "message.txt")
	if err := os.WriteFile(messagePath, []byte(strings.Repeat("x", int(gitLogOutputLimit)+4096)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "commit", "--allow-empty", "-qF", messagePath)
	ws, _ := New(root)
	started := time.Now()
	_, err := ws.GitLog(context.Background(), GitLogOptions{})
	if workspaceErrorCode(err) != ErrFileTooLarge {
		t.Fatalf("oversized log error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("oversized log took %s", elapsed)
	}
}

func TestGitLogPaginationWindowIsExplicitlyTruncated(t *testing.T) {
	result := GitLogResult{Commits: make([]GitCommit, 3)}
	finishGitLogPage(&result, 2, GitLogMaxOffset)
	if result.HasMore || result.NextOffset != nil || !result.PageTruncated || result.Returned != 2 {
		t.Fatalf("boundary page = %#v", result)
	}
}

func TestGitLogHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ws, _ := New(t.TempDir())
	if _, err := ws.GitLog(ctx, GitLogOptions{}); err == nil {
		t.Fatal("cancelled git log should fail")
	}
}

func TestGitLogRequiresRepositoryForGroupAndBlocksSensitivePath(t *testing.T) {
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
	}
	ws, _ := New(root)
	if _, err := ws.GitLog(context.Background(), GitLogOptions{}); workspaceErrorCode(err) != ErrRepositoryNeeded {
		t.Fatalf("missing repository error = %v", err)
	}
	if _, err := ws.GitLog(context.Background(), GitLogOptions{Repository: "web", Path: ".env"}); workspaceErrorCode(err) != ErrSensitiveFile {
		t.Fatalf("sensitive path error = %v", err)
	}
}

func TestGitLogReturnsEmptyHistoryForUnbornHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	ws, _ := New(root)
	result, err := ws.GitLog(context.Background(), GitLogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsRepo || result.Returned != 0 || len(result.Commits) != 0 || result.HasMore {
		t.Fatalf("unborn HEAD log = %#v", result)
	}
}
