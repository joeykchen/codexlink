package workspace

import (
	"errors"
	"strings"
)

const (
	ReadFilesMaxCount       = 16
	ReadFilesDefaultBytes   = 256 * 1024
	ReadFilesMinBytes       = 4 * 1024
	ReadFilesMaxBytes       = 512 * 1024
	ReadFilesMaxLines       = 400
	ReadFilesMaxSourceBytes = 2 * 1024 * 1024
)

type ReadFileRequest struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

type ReadFilesOptions struct {
	Files    []ReadFileRequest
	MaxBytes int
}

type ReadFilesItemError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type ReadFilesItem struct {
	Path   string              `json:"path"`
	Result *ReadFileResult     `json:"result,omitempty"`
	Error  *ReadFilesItemError `json:"error,omitempty"`
}

type ReadFilesResult struct {
	RequestedCount       int             `json:"requestedCount"`
	ProcessedCount       int             `json:"processedCount"`
	SuccessCount         int             `json:"successCount"`
	ErrorCount           int             `json:"errorCount"`
	ReturnedContentBytes int             `json:"returnedContentBytes"`
	Truncated            bool            `json:"truncated"`
	RemainingRequests    int             `json:"remainingRequests"`
	Items                []ReadFilesItem `json:"items"`
}

func (w *Workspace) ReadFiles(options ReadFilesOptions) (ReadFilesResult, error) {
	if len(options.Files) < 1 || len(options.Files) > ReadFilesMaxCount {
		return ReadFilesResult{}, NewError(ErrInvalidArgument, "files must contain between 1 and %d items", ReadFilesMaxCount)
	}
	budget := options.MaxBytes
	if budget == 0 {
		budget = ReadFilesDefaultBytes
	}
	if budget < ReadFilesMinBytes || budget > ReadFilesMaxBytes {
		return ReadFilesResult{}, NewError(ErrInvalidArgument, "max_bytes must be between %d and %d", ReadFilesMinBytes, ReadFilesMaxBytes)
	}
	resolved := make([]ReadFileRequest, len(options.Files))
	seen := make(map[string]struct{}, len(options.Files))
	for index, request := range options.Files {
		if strings.TrimSpace(request.Path) == "" {
			return ReadFilesResult{}, NewError(ErrInvalidArgument, "files[%d].path is required", index)
		}
		if request.StartLine == 0 {
			request.StartLine = 1
		}
		if request.StartLine < 1 || (request.EndLine != 0 && request.EndLine < request.StartLine) {
			return ReadFilesResult{}, NewError(ErrInvalidArgument, "files[%d] has an invalid line range", index)
		}
		if request.EndLine != 0 && request.EndLine-request.StartLine+1 > ReadFilesMaxLines {
			return ReadFilesResult{}, NewError(ErrInvalidArgument, "files[%d] requests more than %d lines", index, ReadFilesMaxLines)
		}
		_, relative, err := w.Resolve(request.Path, true)
		if err != nil {
			return ReadFilesResult{}, err
		}
		key := normalizeIdentityPath(relative)
		if _, exists := seen[key]; exists {
			return ReadFilesResult{}, NewError(ErrInvalidArgument, "files contains duplicate path %q", relative)
		}
		seen[key] = struct{}{}
		request.Path = relative
		resolved[index] = request
	}

	result := ReadFilesResult{RequestedCount: len(resolved), Items: make([]ReadFilesItem, 0, len(resolved))}
	for index, request := range resolved {
		remaining := budget - result.ReturnedContentBytes
		if remaining < 1024 {
			result.Truncated = true
			result.RemainingRequests = len(resolved) - index
			break
		}
		item := ReadFilesItem{Path: request.Path}
		file, resolveErr := w.resolveTextFile(request.Path, false, ReadFilesMaxSourceBytes)
		var readResult ReadFileResult
		if resolveErr == nil {
			item.Path = file.relative
			readResult, resolveErr = readTextRange(file, ReadFileOptions{StartLine: request.StartLine, EndLine: request.EndLine, MaxLines: ReadFilesMaxLines, MaxBytes: remaining})
		}
		if resolveErr != nil {
			code := ErrInvalidArgument
			var workspaceErr *Error
			if errors.As(resolveErr, &workspaceErr) {
				code = workspaceErr.Code
			}
			item.Error = &ReadFilesItemError{Code: code, Message: resolveErr.Error()}
			result.ErrorCount++
		} else {
			item.Result = &readResult
			result.ReturnedContentBytes += len([]byte(readResult.Content))
			result.SuccessCount++
			if readResult.Truncated && result.ReturnedContentBytes >= budget {
				result.Truncated = true
			}
		}
		result.Items = append(result.Items, item)
		result.ProcessedCount++
	}
	if result.RemainingRequests == 0 {
		result.RemainingRequests = result.RequestedCount - result.ProcessedCount
	}
	return result, nil
}
