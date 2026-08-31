package workspace

import (
	"context"
	"testing"
)

func TestSearchGoFallbackRespectsPolicyGlobRegexAndLimit(t *testing.T) {
	t.Setenv("CODEXLINK_DISABLE_RG", "1")
	root := t.TempDir()
	writeTestFile(t, root, "a.go", "package a\n// Needle value\nneedle again\n")
	writeTestFile(t, root, "b.txt", "needle text\n")
	writeTestFile(t, root, ".env", "needle secret\n")
	writeTestFile(t, root, "node_modules/x.js", "needle dependency\n")
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ws.Search(context.Background(), SearchOptions{Query: "needle", Glob: "*.go", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Engine != "go" || len(result.Matches) != 1 || !result.Truncated || result.Matches[0].Path != "a.go" {
		t.Fatalf("unexpected search result: %#v", result)
	}
	regex, err := ws.Search(context.Background(), SearchOptions{Query: `Need(le|ing)`, Regex: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(regex.Matches) != 1 || regex.Matches[0].Path != "a.go" {
		t.Fatalf("unexpected regex result: %#v", regex)
	}
	if _, err := ws.Search(context.Background(), SearchOptions{Query: "[a", Regex: true}); workspaceErrorCode(err) != ErrInvalidExpression {
		t.Fatalf("invalid regex error = %v", err)
	}
}
