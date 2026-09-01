package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindFilesFiltersAndPaginates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.go", "package a")
	writeTestFile(t, root, "src/b.go", "package b")
	writeTestFile(t, root, "src/c.txt", "text")
	writeTestFile(t, root, ".env.go", "secret")
	writeTestFile(t, root, "dist/generated.go", "package generated")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ws.FindFiles(context.Background(), FindFilesOptions{Glob: "**/*.go", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Returned != 1 || first.Matches[0].Path != "a.go" || !first.HasMore || first.NextOffset == nil || *first.NextOffset != 1 {
		t.Fatalf("first page = %#v", first)
	}
	second, err := ws.FindFiles(context.Background(), FindFilesOptions{Glob: "**/*.go", Limit: 10, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Returned != 1 || second.Matches[0].Path != "src/b.go" || second.HasMore {
		t.Fatalf("second page = %#v", second)
	}
	if runtime.GOOS != "windows" && first.SkippedSymlinks != 1 {
		t.Fatalf("skipped symlinks = %d", first.SkippedSymlinks)
	}
}

func TestFindFilesRejectsInvalidAndCancelledQueries(t *testing.T) {
	ws, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.FindFiles(context.Background(), FindFilesOptions{}); workspaceErrorCode(err) != ErrInvalidArgument {
		t.Fatalf("empty glob error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ws.FindFiles(ctx, FindFilesOptions{Glob: "**/*"}); err == nil {
		t.Fatal("cancelled scan should fail")
	}
}

func TestFindFilesPaginationWindowIsExplicitlyTruncated(t *testing.T) {
	matches := make([]FileMatch, FindFilesMaxOffset+3)
	for index := range matches {
		matches[index].Path = string(rune(index + 1))
	}
	result := FindFilesResult{Limit: 2, Offset: FindFilesMaxOffset, Matches: []FileMatch{}}
	finishFindFilesPage(matches, FindFilesMaxOffset, 2, &result)
	if result.HasMore || result.NextOffset != nil || !result.PageTruncated || result.Returned != 2 {
		t.Fatalf("boundary page = %#v", result)
	}
}

func TestFindFilesHonorsCustomIgnoreAndMaxDepth(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".codexlinkignore", "ignored.go\n")
	writeTestFile(t, root, "ignored.go", "package ignored\n")
	writeTestFile(t, root, "one/visible.go", "package visible\n")
	writeTestFile(t, root, "one/two/deep.go", "package deep\n")
	ws, _ := New(root)
	result, err := ws.FindFiles(context.Background(), FindFilesOptions{Glob: "**/*.go", MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Returned != 1 || result.Matches[0].Path != "one/visible.go" {
		t.Fatalf("filtered result = %#v", result)
	}
}
