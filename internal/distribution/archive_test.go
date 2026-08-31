package distribution

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteArchiveIsDeterministicAndSafe(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, _ := ParseTarget("linux", "amd64")
	first := filepath.Join(dir, "one.tar.gz")
	second := filepath.Join(dir, "two.tar.gz")
	entries := []Entry{{Source: b, Name: "README"}, {Source: a, Name: "codexlink", Mode: 0o755}}
	if err := WriteArchive(target, first, entries); err != nil {
		t.Fatal(err)
	}
	if err := WriteArchive(target, second, entries); err != nil {
		t.Fatal(err)
	}
	one, _ := SHA256(first)
	two, _ := SHA256(second)
	if one != two {
		t.Fatalf("archives differ: %s != %s", one, two)
	}

	file, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	if strings.Join(names, ",") != "README,codexlink" {
		t.Fatalf("unexpected archive order: %v", names)
	}

	if err := WriteArchive(target, filepath.Join(dir, "bad.tar.gz"), []Entry{{Source: a, Name: "../escape"}}); err == nil {
		t.Fatal("unsafe path was accepted")
	}
}
