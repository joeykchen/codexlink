package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/config"
)

type Logger struct {
	mu      sync.Mutex
	name    string
	file    *os.File
	console bool
}

var redactors = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:access_token|refresh_token|client_secret|code)=)[^&\s]+`),
	regexp.MustCompile(`(?i)("(?:access_token|refresh_token|client_secret|code)"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)\b[A-Z2-9]{4}-[A-Z2-9]{4}\b`),
	regexp.MustCompile(`\b(?:cl|c2cg|c2c)_[a-z]{2,8}_[A-Za-z0-9_-]{12,}\b`),
}

func New(name string, console bool) (*Logger, error) {
	dir, err := config.StateSubdir("logs")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "bridge.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Logger{name: name, file: file, console: console}, nil
}

func Null() *Logger { return &Logger{name: "null"} }

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

func redact(message string) string {
	out := message
	for _, re := range redactors {
		if re.NumSubexp() >= 1 {
			out = re.ReplaceAllString(out, "${1}[REDACTED]")
		} else {
			out = re.ReplaceAllString(out, "[REDACTED]")
		}
	}
	return out
}

func (l *Logger) log(level, format string, args ...any) {
	if l == nil {
		return
	}
	message := redact(fmt.Sprintf(format, args...))
	message = strings.NewReplacer("\r", " ", "\n", " ").Replace(message)
	line := fmt.Sprintf("%s %-5s %-12s %s\n", time.Now().UTC().Format(time.RFC3339Nano), level, l.name, message)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
	if l.console {
		_, _ = os.Stderr.WriteString(line)
	}
}

func (l *Logger) Debug(format string, args ...any) { l.log("DEBUG", format, args...) }
func (l *Logger) Info(format string, args ...any)  { l.log("INFO", format, args...) }
func (l *Logger) Warn(format string, args ...any)  { l.log("WARN", format, args...) }
func (l *Logger) Error(format string, args ...any) { l.log("ERROR", format, args...) }
