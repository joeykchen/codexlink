// Package openurl opens a trusted HTTP(S) URL in the user's default browser.
package openurl

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

type lookPathFunc func(string) (string, error)

// Open opens target in the platform's default browser without invoking a shell.
func Open(ctx context.Context, target string) error {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("browser URL must be an absolute HTTP(S) URL")
	}
	name, args, err := commandFor(runtime.GOOS, parsed.String(), exec.LookPath)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, name, args...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}

func commandFor(goos, target string, look lookPathFunc) (string, []string, error) {
	switch goos {
	case "darwin":
		name, err := look("open")
		if err != nil {
			return "", nil, fmt.Errorf("open command not found")
		}
		return name, []string{target}, nil
	case "windows":
		name, err := look("rundll32")
		if err != nil {
			return "", nil, fmt.Errorf("rundll32 command not found")
		}
		return name, []string{"url.dll,FileProtocolHandler", target}, nil
	default:
		if name, err := look("xdg-open"); err == nil {
			return name, []string{target}, nil
		}
		if name, err := look("gio"); err == nil {
			return name, []string{"open", target}, nil
		}
		return "", nil, fmt.Errorf("no supported browser opener found (tried xdg-open and gio)")
	}
}
