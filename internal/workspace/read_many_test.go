package workspace

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestReadFilesMatchesReadFileResult(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "lines.txt", "one\ntwo\nthree\nfour\n")
	ws, _ := New(root)
	direct, err := ws.ReadFile("lines.txt", ReadFileOptions{StartLine: 2, EndLine: 3, MaxLines: ReadFilesMaxLines, MaxBytes: ReadFilesDefaultBytes})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{{Path: "lines.txt", StartLine: 2, EndLine: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].Result == nil || !reflect.DeepEqual(*batch.Items[0].Result, direct) {
		t.Fatalf("batch result = %#v, direct = %#v", batch, direct)
	}
}

func TestReadFilesReturnsBoundedPartialErrors(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "one\ntwo\nthree\n")
	writeTestFile(t, root, "binary.bin", "x\x00y")
	writeTestFile(t, root, ".env", "TOKEN=x")
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{
		{Path: "a.txt", StartLine: 2, EndLine: 3},
		{Path: "missing.txt"},
		{Path: "binary.bin"},
		{Path: ".env"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProcessedCount != 4 || result.SuccessCount != 1 || result.ErrorCount != 3 || result.Items[0].Result.Content != "two\nthree" {
		t.Fatalf("result = %#v", result)
	}
	if result.Items[1].Error.Code != ErrFileNotFound || result.Items[2].Error.Code != ErrBinaryFile || result.Items[3].Error.Code != ErrSensitiveFile {
		t.Fatalf("errors = %#v", result.Items)
	}
}

func TestReadFilesStopsAtAggregateContentBudget(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeTestFile(t, root, name, strings.Repeat("x", 3500)+"\n")
	}
	ws, _ := New(root)
	result, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{{Path: "a.txt"}, {Path: "b.txt"}, {Path: "c.txt"}}, MaxBytes: ReadFilesMinBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.ProcessedCount != 1 || result.RemainingRequests != 2 || result.ReturnedContentBytes > ReadFilesMinBytes {
		t.Fatalf("budget result = %#v", result)
	}
}

func TestReadFilesCaseDistinctIdentityMatchesPlatform(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "Foo.txt", "upper\n")
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		writeTestFile(t, root, "foo.txt", "lower\n")
	}
	ws, _ := New(root)
	_, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{{Path: "Foo.txt"}, {Path: "foo.txt"}}})
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if workspaceErrorCode(err) != ErrInvalidArgument {
			t.Fatalf("case-insensitive duplicate error = %v", err)
		}
	} else if err != nil {
		t.Fatalf("case-distinct paths rejected: %v", err)
	}
}

func TestReadFilesRejectsDuplicatesAndInvalidRanges(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "one\n")
	writeTestFile(t, root, ".env", "TOKEN=x\n")
	ws, _ := New(root)
	if _, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{{Path: "a.txt"}, {Path: "./a.txt"}}}); workspaceErrorCode(err) != ErrInvalidArgument {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{{Path: "a.txt", StartLine: 3, EndLine: 2}}}); workspaceErrorCode(err) != ErrInvalidArgument {
		t.Fatalf("range error = %v", err)
	}
	if _, err := ws.ReadFiles(ReadFilesOptions{Files: []ReadFileRequest{{Path: ".env"}, {Path: "./.env"}}}); workspaceErrorCode(err) != ErrInvalidArgument {
		t.Fatalf("sensitive duplicate error = %v", err)
	}
}
