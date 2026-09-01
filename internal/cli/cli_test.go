package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpPathsExitSuccessfully(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "top level", args: []string{"--help"}, want: "Core commands:"},
		{name: "direct command", args: []string{"start", "--help"}, want: "Usage: codexlink start [options]"},
		{name: "session group", args: []string{"session", "--help"}, want: "codexlink session <command>"},
		{name: "session child", args: []string{"session", "set", "--help"}, want: "Usage: codexlink session set [options]"},
		{name: "tunnel group", args: []string{"tunnel", "--help"}, want: "codexlink tunnel <command>"},
		{name: "tunnel positional child", args: []string{"tunnel", "choose", "--help"}, want: "codexlink tunnel choose <quick|named>"},
		{name: "tunnel selected child", args: []string{"tunnel", "choose", "quick", "--help"}, want: "Usage: codexlink tunnel choose [options]"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := &App{Stdout: &stdout, Stderr: &stderr}
			if code := app.Run(context.Background(), test.args); code != 0 {
				t.Fatalf("exit code = %d, stderr=%q", code, stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, test.want) {
				t.Fatalf("help output %q does not contain %q", combined, test.want)
			}
		})
	}
}

func TestVersionUnknownAndFlagErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}
	if code := app.Run(context.Background(), []string{"version"}); code != 0 || !strings.Contains(stdout.String(), "CodexLink ") {
		t.Fatalf("version: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"does-not-exist"}); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unknown command: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"status", "--not-a-flag"}); code != 2 {
		t.Fatalf("flag error code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestLocalStateCommandsRoundTrip(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	codexHome := filepath.Join(t.TempDir(), "codex")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Dir(stateDir))
	t.Setenv("CODEXLINK_STATE_DIR", stateDir)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CODEXLINK_SKILLS_DIR", filepath.Join(t.TempDir(), "skills"))

	run := func(args ...string) (string, string, int) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		app := &App{Stdout: &stdout, Stderr: &stderr}
		code := app.Run(context.Background(), args)
		return stdout.String(), stderr.String(), code
	}
	mustJSON := func(args ...string) map[string]any {
		t.Helper()
		stdout, stderr, code := run(args...)
		if code != 0 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(stdout), &value); err != nil {
			t.Fatalf("%v: invalid JSON %q: %v", args, stdout, err)
		}
		return value
	}
	workspaceArgs := []string{"--workspace", root, "--json"}

	status := mustJSON(append([]string{"status"}, workspaceArgs...)...)
	if running, _ := status["running"].(bool); running {
		t.Fatalf("unexpected live bridge: %#v", status)
	}
	workspaceInfo := mustJSON(append([]string{"workspace"}, workspaceArgs...)...)
	if workspaceInfo["root"] != realRoot {
		t.Fatalf("workspace root = %#v", workspaceInfo["root"])
	}
	mustJSON("tunnel", "status", "--workspace", root, "--json")
	mustJSON("tunnel", "choose", "quick", "--workspace", root, "--json")
	mustJSON("unpair", "--workspace", root, "--json")
	stopped := mustJSON("stop", "--workspace", root, "--json")
	if stopped["stopped"] != false {
		t.Fatalf("stop payload = %#v", stopped)
	}

	recorded := mustJSON(
		"record", "--workspace", root, "--task-id", "task-1", "--iteration", "2",
		"--changed-files", "a.go,b.go", "--tests", "go test ./... passed",
		"--timestamp", "2026-08-31T12:00:00Z", "--json",
	)
	if recorded["recorded"] != true {
		t.Fatalf("record payload = %#v", recorded)
	}
	if _, stderr, code := run("record", "--workspace", root); code != 1 || !strings.Contains(stderr, "--task-id is required") {
		t.Fatalf("missing task id: code=%d stderr=%q", code, stderr)
	}

	saved := mustJSON(
		"session", "set", "--workspace", root, "--mode", "long-chat",
		"--url", "https://chatgpt.com/c/example", "--connector-name", "CodexLink · demo", "--json",
	)
	if saved["saved"] == nil {
		t.Fatalf("session set payload = %#v", saved)
	}
	got := mustJSON("session", "get", "--workspace", root, "--json")
	if got["saved"] == nil {
		t.Fatalf("session get payload = %#v", got)
	}
	cleared := mustJSON("session", "clear", "--workspace", root, "--json")
	if cleared["cleared"] != true {
		t.Fatalf("session clear payload = %#v", cleared)
	}

	configPath := filepath.Join(codexHome, "config.toml")
	installed := mustJSON("install", "--config", configPath, "--json")
	if installed["ok"] != true {
		t.Fatalf("install payload = %#v", installed)
	}
	allowed := mustJSON("sandbox-allow", "--config", configPath, "--json")
	if allowed["alreadyAllowed"] != true {
		t.Fatalf("sandbox payload = %#v", allowed)
	}

	stdout, stderr, code := run("logs")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "No bridge logs found") {
		t.Fatalf("logs: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	doctor := mustJSON("doctor", "--workspace", root, "--no-fix", "--json")
	if doctor["checks"] == nil {
		t.Fatalf("doctor payload = %#v", doctor)
	}
}
