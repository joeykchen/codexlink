package workspace

import (
	"strings"
	"testing"
)

func TestFileOutlineGoAndMarkdown(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "sample.go", `package sample
import "context"
const answer = 42
type Service struct{}
func New() *Service { return &Service{} }
func (s *Service) Run(ctx context.Context) error { return nil }
`)
	writeTestFile(t, root, "README.md", "# Title\ntext\n```go\n# Not a heading\n```\n## Details\n")
	ws, _ := New(root)
	outline, err := ws.OutlineFile("sample.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	if outline.Language != "go" || outline.Package != "sample" || len(outline.Imports) != 1 || outline.TotalItems != 4 || !outline.Complete {
		t.Fatalf("go outline = %#v", outline)
	}
	joined := ""
	for _, item := range outline.Items {
		joined += item.Kind + ":" + item.Name + ":" + item.Detail + "\n"
	}
	if !strings.Contains(joined, "type:Service") || !strings.Contains(joined, "function:New") || !strings.Contains(joined, "method:Run") || strings.Contains(joined, "return") || strings.Contains(joined, "42") {
		t.Fatalf("unsafe/incomplete outline:\n%s", joined)
	}
	markdown, err := ws.OutlineFile("README.md", 1)
	if err != nil {
		t.Fatal(err)
	}
	if markdown.TotalItems != 2 || markdown.ReturnedItems != 1 || !markdown.Truncated || markdown.Items[0].Name != "Title" {
		t.Fatalf("markdown outline = %#v", markdown)
	}
}

func TestFileOutlinePartialAndUnsupported(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "broken.go", "package broken\nfunc Good() {}\nfunc Bad(")
	writeTestFile(t, root, "data.json", `{}`)
	ws, _ := New(root)
	partial, err := ws.OutlineFile("broken.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Complete || partial.ParseError == "" || partial.TotalItems == 0 {
		t.Fatalf("partial outline = %#v", partial)
	}
	if _, err := ws.OutlineFile("data.json", 20); workspaceErrorCode(err) != ErrUnsupportedType {
		t.Fatalf("unsupported error = %v", err)
	}
}

func TestFileOutlineAcceptsBoundedLongMarkdownHeading(t *testing.T) {
	root := t.TempDir()
	name := strings.Repeat("x", 128*1024)
	writeTestFile(t, root, "LONG.md", "# "+name+"\n")
	ws, _ := New(root)
	outline, err := ws.OutlineFile("LONG.md", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !outline.Complete || outline.TotalItems != 1 || len(outline.Items[0].Name) != 500 {
		t.Fatalf("long markdown outline = %#v", outline)
	}
}

func TestFileOutlineRedactsSignatureLiteralsAndStructTags(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "secret.go", "package secret\nfunc Inspect(a [len(\"SECRET_LITERAL\")]byte, b struct { X string `secret:\"TAG_VALUE\"` }) [42]byte { return [42]byte{} }\n")
	ws, _ := New(root)
	outline, err := ws.OutlineFile("secret.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	encoded := ""
	for _, item := range outline.Items {
		encoded += item.Name + " " + item.Detail
	}
	if strings.Contains(encoded, "SECRET_LITERAL") || strings.Contains(encoded, "TAG_VALUE") || strings.Contains(encoded, "42") || strings.Contains(encoded, "secret:") {
		t.Fatalf("signature leaked source literals or tags: %s", encoded)
	}
	if !strings.Contains(encoded, `len("")`) || !strings.Contains(encoded, "[0]byte") {
		t.Fatalf("signature placeholders missing: %s", encoded)
	}
}
