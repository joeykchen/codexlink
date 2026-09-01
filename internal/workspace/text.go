package workspace

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

type ReadFileOptions struct {
	StartLine int
	EndLine   int
	MaxLines  int
	MaxBytes  int
}

type ReadFileResult struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"sizeBytes"`
	TotalLines     int    `json:"totalLines"`
	StartLine      int    `json:"startLine"`
	EndLine        int    `json:"endLine"`
	Truncated      bool   `json:"truncated"`
	RemainingLines int    `json:"remainingLines"`
	NextStartLine  *int   `json:"nextStartLine"`
	Content        string `json:"content"`
}

const (
	defaultMaxLines = 400
	hardMaxLines    = 2000
	defaultMaxBytes = 256 * 1024
	hardMaxBytes    = 1024 * 1024
)

type resolvedTextFile struct {
	absolute string
	relative string
	size     int64
}

func (w *Workspace) ReadFile(requested string, options ReadFileOptions) (ReadFileResult, error) {
	file, err := w.resolveTextFile(requested, false, 0)
	if err != nil {
		return ReadFileResult{}, err
	}
	return readTextRange(file, options)
}

func (w *Workspace) resolveTextFile(requested string, allowSensitive bool, maxSourceBytes int64) (resolvedTextFile, error) {
	absolute, relative, err := w.Resolve(requested, allowSensitive)
	if err != nil {
		return resolvedTextFile{}, err
	}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return resolvedTextFile{}, NewError(ErrFileNotFound, "file not found: %s", relative)
	}
	if err != nil {
		return resolvedTextFile{}, err
	}
	if !info.Mode().IsRegular() {
		return resolvedTextFile{}, NewError(ErrNotFile, "not a regular file: %s", relative)
	}
	if maxSourceBytes > 0 && info.Size() > maxSourceBytes {
		return resolvedTextFile{}, NewError(ErrFileTooLarge, "file exceeds %d bytes: %s", maxSourceBytes, relative)
	}
	binary, err := isBinary(absolute)
	if err != nil {
		return resolvedTextFile{}, err
	}
	if binary {
		return resolvedTextFile{}, NewError(ErrBinaryFile, "binary file (%d bytes): %s", info.Size(), relative)
	}
	return resolvedTextFile{absolute: absolute, relative: relative, size: info.Size()}, nil
}

func readTextRange(source resolvedTextFile, options ReadFileOptions) (ReadFileResult, error) {
	start := options.StartLine
	if start < 1 {
		start = 1
	}
	maxLines := options.MaxLines
	if maxLines < 1 {
		maxLines = defaultMaxLines
	}
	if maxLines > hardMaxLines {
		maxLines = hardMaxLines
	}
	end := options.EndLine
	if end < start {
		end = start + maxLines - 1
	}
	if end > start+hardMaxLines-1 {
		end = start + hardMaxLines - 1
	}
	maxBytes := options.MaxBytes
	if maxBytes < 1024 {
		maxBytes = defaultMaxBytes
	}
	if maxBytes > hardMaxBytes {
		maxBytes = hardMaxBytes
	}

	file, err := os.Open(source.absolute)
	if err != nil {
		return ReadFileResult{}, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	selected := make([]string, 0, maxLines)
	total := 0
	actualEnd := start - 1
	bytesUsed := 0
	byteTruncated := false
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			total++
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if total >= start && total <= end && !byteTruncated {
				cost := len([]byte(line))
				if len(selected) > 0 {
					cost++
				}
				if bytesUsed+cost <= maxBytes {
					selected = append(selected, line)
					bytesUsed += cost
					actualEnd = total
				} else if len(selected) == 0 {
					prefix := utf8SafePrefix([]byte(line), maxBytes)
					selected = append(selected, string(prefix))
					bytesUsed = len(prefix)
					actualEnd = total
					byteTruncated = true
				} else {
					byteTruncated = true
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return ReadFileResult{}, readErr
			}
			break
		}
	}
	remaining := total - actualEnd
	if remaining < 0 {
		remaining = 0
	}
	var next *int
	if remaining > 0 || byteTruncated {
		value := actualEnd + 1
		if byteTruncated && len(selected) == 1 && actualEnd == start {
			value = 0
		}
		if value > 0 {
			next = &value
		}
	}
	return ReadFileResult{
		Path:           source.relative,
		SizeBytes:      source.size,
		TotalLines:     total,
		StartLine:      start,
		EndLine:        actualEnd,
		Truncated:      remaining > 0 || byteTruncated,
		RemainingLines: remaining,
		NextStartLine:  next,
		Content:        strings.Join(selected, "\n"),
	}, nil
}

func utf8SafePrefix(data []byte, limit int) []byte {
	if len(data) <= limit {
		return data
	}
	end := limit
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return data[:end]
}

func isBinary(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 8192)
	count, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	for _, value := range buffer[:count] {
		if value == 0 {
			return true, nil
		}
	}
	return false, nil
}

func boundedSingleLine(value string, maxBytes int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return string(utf8SafePrefix([]byte(value), maxBytes))
}
