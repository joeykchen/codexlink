package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	GitLogDefaultLimit = 20
	GitLogMaxLimit     = 100
	GitLogMaxOffset    = 1000
	gitLogOutputLimit  = 128 * 1024
	gitLogErrorLimit   = 16 * 1024
)

type GitLogOptions struct {
	Repository string
	Path       string
	Limit      int
	Offset     int
}

type GitCommit struct {
	Hash       string   `json:"hash"`
	Parents    []string `json:"parents"`
	AuthoredAt string   `json:"authoredAt"`
	Subject    string   `json:"subject"`
}

type GitLogResult struct {
	Repository    string      `json:"repository,omitempty"`
	IsRepo        bool        `json:"isRepo"`
	Path          string      `json:"path,omitempty"`
	Offset        int         `json:"offset"`
	Limit         int         `json:"limit"`
	Commits       []GitCommit `json:"commits"`
	Returned      int         `json:"returned"`
	HasMore       bool        `json:"hasMore"`
	NextOffset    *int        `json:"nextOffset"`
	PageTruncated bool        `json:"pageTruncated"`
}

func (w *Workspace) GitLog(ctx context.Context, options GitLogOptions) (GitLogResult, error) {
	if err := ctx.Err(); err != nil {
		return GitLogResult{}, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = GitLogDefaultLimit
	}
	if limit < 1 || limit > GitLogMaxLimit {
		return GitLogResult{}, NewError(ErrInvalidArgument, "limit must be between 1 and %d", GitLogMaxLimit)
	}
	if options.Offset < 0 || options.Offset > GitLogMaxOffset {
		return GitLogResult{}, NewError(ErrInvalidArgument, "offset must be between 0 and %d", GitLogMaxOffset)
	}
	result := GitLogResult{Offset: options.Offset, Limit: limit, Commits: []GitCommit{}}
	repository, found, err := w.selectGitRepository(options.Repository)
	if err != nil {
		return result, err
	}
	if !found {
		return result, nil
	}
	result.Repository = repository.Path
	inside, err := runGitBounded(ctx, repository.Root, 1024, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return result, nil
	}
	result.IsRepo = true
	path := "."
	if strings.TrimSpace(options.Path) != "" {
		path, err = w.repositoryRelativePath(repository, options.Path)
		if err != nil {
			return GitLogResult{}, err
		}
		result.Path = path
	}
	hasHead, err := gitHeadExists(ctx, repository.Root)
	if err != nil {
		return GitLogResult{}, wrapRepositoryError(repository, err)
	}
	if !hasHead {
		return result, nil
	}
	args := []string{"--no-replace-objects", "log", "--topo-order", "--no-show-signature", "--format=%H%x00%P%x00%aI%x00%s%x00", "--skip=" + strconv.Itoa(options.Offset), "-n", strconv.Itoa(limit + 1)}
	if strings.TrimSpace(options.Path) != "" {
		args = append(args, "--", ":(literal)"+path)
	}
	output, err := runGitBounded(ctx, repository.Root, gitLogOutputLimit, args...)
	if err != nil {
		return GitLogResult{}, wrapRepositoryError(repository, err)
	}
	tokens := bytes.Split(output, []byte{0})
	for len(tokens) > 0 && len(tokens[len(tokens)-1]) == 0 {
		tokens = tokens[:len(tokens)-1]
	}
	for index := 0; index+3 < len(tokens); index += 4 {
		parents := strings.Fields(string(tokens[index+1]))
		commit := GitCommit{Hash: strings.TrimSpace(string(tokens[index])), Parents: parents, AuthoredAt: strings.TrimSpace(string(tokens[index+2])), Subject: boundedSingleLine(string(tokens[index+3]), 300)}
		result.Commits = append(result.Commits, commit)
	}
	finishGitLogPage(&result, limit, options.Offset)
	return result, nil
}

func gitHeadExists(ctx context.Context, root string) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(append([]string(nil), gitConfigOverrides...), "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	command := exec.CommandContext(commandCtx, "git", args...)
	command.Dir = root
	command.Env = safeGitEnvironment()
	stdout := &boundedCapture{limit: 1024, cancel: cancel}
	stderr := &boundedCapture{limit: gitLogErrorLimit, cancel: cancel}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return false, NewError(ErrFileTooLarge, "Git HEAD verification exceeded its safety limit")
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return false, fmt.Errorf("Git HEAD verification timed out")
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return false, parentErr
	}
	if err == nil {
		return strings.TrimSpace(stdout.buffer.String()) != "", nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 && strings.TrimSpace(stderr.buffer.String()) == "" {
		return false, nil
	}
	message := strings.TrimSpace(stderr.buffer.String())
	if message == "" {
		message = err.Error()
	}
	return false, fmt.Errorf("git rev-parse HEAD failed: %s", message)
}

func finishGitLogPage(result *GitLogResult, limit, offset int) {
	if len(result.Commits) > limit {
		result.Commits = result.Commits[:limit]
		next := offset + limit
		if next <= GitLogMaxOffset {
			result.HasMore = true
			result.NextOffset = &next
		} else {
			result.PageTruncated = true
		}
	}
	result.Returned = len(result.Commits)
}

func runGitBounded(ctx context.Context, root string, limit int64, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	commandArgs := append(append([]string(nil), gitConfigOverrides...), args...)
	command := exec.CommandContext(commandCtx, "git", commandArgs...)
	command.Dir = root
	command.Env = safeGitEnvironment()
	stdout := &boundedCapture{limit: limit, cancel: cancel}
	stderr := &boundedCapture{limit: gitLogErrorLimit, cancel: cancel}
	command.Stdout = stdout
	command.Stderr = stderr
	waitErr := command.Run()
	if stdout.exceeded {
		return nil, NewError(ErrFileTooLarge, "git log exceeds the %d byte safety limit", limit)
	}
	if stderr.exceeded {
		return nil, NewError(ErrFileTooLarge, "git error output exceeds the %d byte safety limit", gitLogErrorLimit)
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("git command timed out")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.buffer.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, fmt.Errorf("git %s failed: %s", strings.Join(args, " "), message)
	}
	return stdout.buffer.Bytes(), nil
}

type boundedCapture struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
	cancel   context.CancelFunc
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	if !capture.exceeded {
		remaining := capture.limit - int64(capture.buffer.Len())
		if remaining > 0 {
			keep := int64(len(data))
			if keep > remaining {
				keep = remaining
			}
			_, _ = capture.buffer.Write(data[:keep])
		}
		if int64(len(data)) > remaining {
			capture.exceeded = true
			capture.cancel()
		}
	}
	return len(data), nil
}
