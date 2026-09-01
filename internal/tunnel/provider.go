package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/logging"
)

type Status struct {
	Running  bool   `json:"running"`
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider"`
	Detail   string `json:"detail,omitempty"`
}

type DoctorReport struct {
	Provider    string   `json:"provider"`
	BinaryFound bool     `json:"binaryFound"`
	BinaryPath  string   `json:"binaryPath,omitempty"`
	Running     bool     `json:"running"`
	URL         string   `json:"url,omitempty"`
	Problems    []string `json:"problems"`
}

type Provider interface {
	Name() string
	Start(context.Context, int) (string, error)
	Stop() error
	Status() Status
	Doctor(context.Context) DoctorReport
}

type processProvider struct {
	mu         sync.Mutex
	name       string
	binary     string
	args       func(int) []string
	ready      func(string) (string, bool)
	fixedURL   string
	logger     *logging.Logger
	timeout    time.Duration
	cmd        *exec.Cmd
	url        string
	lastError  string
	generation uint64
}

var quickURLRE = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com`)
var namedReadyRE = regexp.MustCompile(`(?i)registered tunnel connection`)
var cloudflaredErrorRE = regexp.MustCompile(`(?i)\b(error|failed|fatal)\b`)

func ParseQuickURL(line string) string {
	return quickURLRE.FindString(strings.ToLower(line))
}

func NewQuickProvider(logger *logging.Logger) Provider {
	if logger == nil {
		logger = logging.Null()
	}
	return &processProvider{
		name: "cloudflare-quick", binary: FindBinary("cloudflared"), logger: logger, timeout: 45 * time.Second,
		args: func(port int) []string {
			return []string{"tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", port), "--no-autoupdate"}
		},
		ready: func(line string) (string, bool) { url := ParseQuickURL(line); return url, url != "" },
	}
}

func NewNamedProvider(tunnelName, hostname string, logger *logging.Logger) (Provider, error) {
	if logger == nil {
		logger = logging.Null()
	}
	hostname, err := NormalizeHostname(hostname)
	if err != nil {
		return nil, err
	}
	tunnelName = strings.TrimSpace(tunnelName)
	if tunnelName == "" || len(tunnelName) > 128 {
		return nil, fmt.Errorf("invalid named tunnel name")
	}
	fixed := "https://" + hostname
	return &processProvider{
		name: "cloudflare-named", binary: FindBinary("cloudflared"), logger: logger, timeout: 45 * time.Second, fixedURL: fixed,
		args: func(port int) []string {
			return []string{"tunnel", "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", port), "run", tunnelName}
		},
		ready: func(line string) (string, bool) {
			if namedReadyRE.MatchString(line) {
				return fixed, true
			}
			return "", false
		},
	}, nil
}

func ProviderForState(state State, logger *logging.Logger) (Provider, error) {
	if NamedReady(state) {
		return NewNamedProvider(state.TunnelName, state.Hostname, logger)
	}
	return NewQuickProvider(logger), nil
}

func (p *processProvider) Name() string { return p.name }

func (p *processProvider) Start(ctx context.Context, localPort int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if localPort < 1 || localPort > 65535 {
		return "", fmt.Errorf("invalid local port %d", localPort)
	}
	p.mu.Lock()
	if p.cmd != nil {
		if p.url != "" {
			url := p.url
			p.mu.Unlock()
			return url, nil
		}
		p.mu.Unlock()
		return "", fmt.Errorf("%s tunnel is already starting", p.name)
	}
	if p.binary == "" {
		p.binary = FindBinary("cloudflared")
	}
	if p.binary == "" {
		p.mu.Unlock()
		return "", fmt.Errorf("NEED_CLOUDFLARED: cloudflared is not installed")
	}
	command := exec.Command(p.binary, p.args(localPort)...)
	readyCh := make(chan string, 1)
	exitCh := make(chan error, 1)

	p.url = ""
	p.lastError = ""
	generation := p.generation + 1
	handleLine := func(line string) {
		if url, ok := p.ready(line); ok {
			select {
			case readyCh <- url:
			default:
			}
		}
		if cloudflaredErrorRE.MatchString(line) {
			p.mu.Lock()
			if p.generation == generation {
				p.lastError = truncate(line, 400)
			}
			p.mu.Unlock()
			p.logger.Debug("cloudflared: %s", truncate(line, 400))
		}
	}
	stdout := newLineWriter(handleLine)
	stderr := newLineWriter(handleLine)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		p.mu.Unlock()
		return "", err
	}
	p.generation = generation
	p.cmd = command
	p.mu.Unlock()

	go func() {
		waitErr := command.Wait()
		stdout.Flush()
		stderr.Flush()
		p.mu.Lock()
		if p.generation == generation && p.cmd == command {
			p.cmd = nil
			p.url = ""
		}
		p.mu.Unlock()
		exitCh <- waitErr
	}()

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case url := <-readyCh:
		p.mu.Lock()
		active := p.generation == generation && p.cmd == command
		if active {
			p.url = url
		}
		p.mu.Unlock()
		if !active {
			return "", fmt.Errorf("%s tunnel stopped while starting", p.name)
		}
		p.logger.Info("%s tunnel established at %s", p.name, url)
		return url, nil
	case waitErr := <-exitCh:
		detail := p.errorDetail()
		if waitErr == nil {
			waitErr = errors.New("cloudflared exited before becoming ready")
		}
		if detail != "" {
			return "", fmt.Errorf("%w: %s", waitErr, detail)
		}
		return "", waitErr
	case <-timer.C:
		_ = p.stopGeneration(generation)
		return "", fmt.Errorf("%s tunnel start timed out", p.name)
	case <-ctx.Done():
		_ = p.stopGeneration(generation)
		return "", ctx.Err()
	}
}

func (p *processProvider) stopGeneration(generation uint64) error {
	p.mu.Lock()
	if p.generation != generation || p.cmd == nil {
		p.mu.Unlock()
		return nil
	}
	command := p.cmd
	p.cmd = nil
	p.url = ""
	p.mu.Unlock()
	if command.Process != nil {
		return command.Process.Kill()
	}
	return nil
}

func (p *processProvider) Stop() error {
	p.mu.Lock()
	command := p.cmd
	p.cmd = nil
	p.url = ""
	p.generation++
	p.mu.Unlock()
	if command != nil && command.Process != nil {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}

func (p *processProvider) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Running: p.cmd != nil && p.url != "", URL: p.url, Provider: p.name, Detail: p.lastError}
}

func (p *processProvider) Doctor(ctx context.Context) DoctorReport {
	binary := p.binary
	if binary == "" {
		binary = FindBinary("cloudflared")
	}
	status := p.Status()
	problems := make([]string, 0)
	if binary == "" {
		problems = append(problems, "cloudflared binary was not found")
	}
	if binary != "" && !status.Running {
		problems = append(problems, "tunnel process is not connected")
	}
	return DoctorReport{Provider: p.name, BinaryFound: binary != "", BinaryPath: binary, Running: status.Running, URL: status.URL, Problems: problems}
}

func (p *processProvider) errorDetail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastError
}

const maxTunnelLineBytes = 64 << 10

type lineWriter struct {
	pending    []byte
	discarding bool
	onLine     func(string)
}

func newLineWriter(onLine func(string)) *lineWriter {
	return &lineWriter{onLine: onLine}
}

func (w *lineWriter) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		if w.discarding {
			newline := bytes.IndexByte(data, '\n')
			if newline < 0 {
				return written, nil
			}
			w.discarding = false
			data = data[newline+1:]
			continue
		}

		newline := bytes.IndexByte(data, '\n')
		segment := data
		complete := false
		if newline >= 0 {
			segment = data[:newline]
			complete = true
		}

		remaining := maxTunnelLineBytes - len(w.pending)
		if len(segment) > remaining {
			w.pending = append(w.pending, segment[:remaining]...)
			w.emitPending()
			if !complete {
				w.discarding = true
				return written, nil
			}
		} else {
			w.pending = append(w.pending, segment...)
		}

		if !complete {
			return written, nil
		}
		w.emitPending()
		data = data[newline+1:]
	}
	return written, nil
}

func (w *lineWriter) Flush() {
	if !w.discarding {
		w.emitPending()
	}
	w.pending = nil
	w.discarding = false
}

func (w *lineWriter) emitPending() {
	if len(w.pending) == 0 {
		return
	}
	w.onLine(strings.TrimSuffix(string(w.pending), "\r"))
	w.pending = nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func FindBinary(name string) string {
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	executable := name
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join("/opt/homebrew/bin", executable), filepath.Join("/usr/local/bin", executable), filepath.Join("/usr/bin", executable),
		filepath.Join(home, ".local", "bin", executable),
		filepath.Join(`C:\Program Files\cloudflared`, executable), filepath.Join(`C:\Program Files (x86)\cloudflared`, executable),
	}
	for _, candidate := range candidates {
		if found, err := exec.LookPath(candidate); err == nil {
			return found
		}
	}
	return ""
}
