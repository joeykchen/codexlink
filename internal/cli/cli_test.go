package cli

import (
	"bytes"
	"context"
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
