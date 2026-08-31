package openurl

import (
	"context"
	"errors"
	"testing"
)

func TestCommandForPlatforms(t *testing.T) {
	look := func(name string) (string, error) { return "/bin/" + name, nil }
	tests := []struct {
		goos string
		name string
		args []string
	}{
		{goos: "darwin", name: "/bin/open", args: []string{"https://example.com/a"}},
		{goos: "windows", name: "/bin/rundll32", args: []string{"url.dll,FileProtocolHandler", "https://example.com/a"}},
		{goos: "linux", name: "/bin/xdg-open", args: []string{"https://example.com/a"}},
	}
	for _, test := range tests {
		name, args, err := commandFor(test.goos, "https://example.com/a", look)
		if err != nil || name != test.name || len(args) != len(test.args) {
			t.Fatalf("commandFor(%s) = %q %v, %v", test.goos, name, args, err)
		}
		for i := range args {
			if args[i] != test.args[i] {
				t.Fatalf("commandFor(%s) arg %d = %q, want %q", test.goos, i, args[i], test.args[i])
			}
		}
	}
}

func TestCommandForLinuxFallsBackToGIO(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "gio" {
			return "/usr/bin/gio", nil
		}
		return "", errors.New("missing")
	}
	name, args, err := commandFor("linux", "https://example.com", look)
	if err != nil || name != "/usr/bin/gio" || len(args) != 2 || args[0] != "open" {
		t.Fatalf("unexpected fallback: %q %v %v", name, args, err)
	}
}

func TestOpenRejectsUnsafeURL(t *testing.T) {
	for _, target := range []string{"file:///tmp/a", "javascript:alert(1)", "https://user@example.com"} {
		if err := Open(context.Background(), target); err == nil {
			t.Fatalf("Open(%q) unexpectedly succeeded", target)
		}
	}
}
