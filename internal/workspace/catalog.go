package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	FindFilesDefaultDepth = 12
	FindFilesMaxDepth     = 32
	FindFilesDefaultLimit = 100
	FindFilesMaxLimit     = 500
	FindFilesMaxOffset    = 10000
	FindFilesMaxEntries   = 50000
	FindFilesMaxGlobBytes = 256
)

type FindFilesOptions struct {
	Path     string
	Glob     string
	MaxDepth int
	Limit    int
	Offset   int
}

type FileMatch struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
}

type FindFilesResult struct {
	Path            string      `json:"path"`
	Glob            string      `json:"glob"`
	MaxDepth        int         `json:"maxDepth"`
	Offset          int         `json:"offset"`
	Limit           int         `json:"limit"`
	Matches         []FileMatch `json:"matches"`
	Returned        int         `json:"returned"`
	HasMore         bool        `json:"hasMore"`
	NextOffset      *int        `json:"nextOffset"`
	ScannedEntries  int         `json:"scannedEntries"`
	SkippedSymlinks int         `json:"skippedSymlinks"`
	ScanTruncated   bool        `json:"scanTruncated"`
	PageTruncated   bool        `json:"pageTruncated"`
}

func (w *Workspace) FindFiles(ctx context.Context, options FindFilesOptions) (FindFilesResult, error) {
	path := options.Path
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	abs, rel, err := w.Resolve(path, false)
	if err != nil {
		return FindFilesResult{}, err
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return FindFilesResult{}, NewError(ErrFileNotFound, "directory not found: %s", rel)
	}
	if err != nil {
		return FindFilesResult{}, err
	}
	if !info.IsDir() {
		return FindFilesResult{}, NewError(ErrNotDirectory, "not a directory: %s", rel)
	}
	glob := strings.TrimSpace(options.Glob)
	if glob == "" || len([]byte(glob)) > FindFilesMaxGlobBytes || strings.ContainsRune(glob, '\x00') {
		return FindFilesResult{}, NewError(ErrInvalidArgument, "glob must contain between 1 and %d bytes", FindFilesMaxGlobBytes)
	}
	if _, ok := compileIgnoreRule(glob); !ok {
		return FindFilesResult{}, NewError(ErrInvalidExpression, "glob is invalid")
	}
	depth := options.MaxDepth
	if depth == 0 {
		depth = FindFilesDefaultDepth
	}
	if depth < 1 || depth > FindFilesMaxDepth {
		return FindFilesResult{}, NewError(ErrInvalidArgument, "max_depth must be between 1 and %d", FindFilesMaxDepth)
	}
	limit := options.Limit
	if limit == 0 {
		limit = FindFilesDefaultLimit
	}
	if limit < 1 || limit > FindFilesMaxLimit {
		return FindFilesResult{}, NewError(ErrInvalidArgument, "limit must be between 1 and %d", FindFilesMaxLimit)
	}
	if options.Offset < 0 || options.Offset > FindFilesMaxOffset {
		return FindFilesResult{}, NewError(ErrInvalidArgument, "offset must be between 0 and %d", FindFilesMaxOffset)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := FindFilesResult{Path: rel, Glob: glob, MaxDepth: depth, Offset: options.Offset, Limit: limit, Matches: []FileMatch{}}
	all := make([]FileMatch, 0, limit+options.Offset+1)
	baseRel := rel
	if baseRel == "." {
		baseRel = ""
	}
	var walk func(string, string, int) error
	walk = func(dirAbs, dirRel string, level int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := os.ReadDir(dirAbs)
		if readErr != nil {
			return nil
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if result.ScannedEntries >= FindFilesMaxEntries {
				result.ScanTruncated = true
				return nil
			}
			result.ScannedEntries++
			childRel := entry.Name()
			if dirRel != "" {
				childRel = strings.TrimSuffix(dirRel, "/") + "/" + entry.Name()
			}
			if w.Policy.IsHidden(childRel) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				result.SkippedSymlinks++
				continue
			}
			childAbs := filepath.Join(dirAbs, entry.Name())
			if entry.IsDir() {
				if level < depth {
					if err := walk(childAbs, childRel, level+1); err != nil {
						return err
					}
				}
				continue
			}
			metadata, statErr := entry.Info()
			if statErr != nil || !metadata.Mode().IsRegular() || !MatchGlob(glob, childRel) {
				continue
			}
			all = append(all, FileMatch{Path: childRel, SizeBytes: metadata.Size()})
		}
		return nil
	}
	if err := walk(abs, baseRel, 1); err != nil {
		return FindFilesResult{}, err
	}
	sort.Slice(all, func(i, j int) bool {
		left, right := strings.ToLower(all[i].Path), strings.ToLower(all[j].Path)
		if left == right {
			return all[i].Path < all[j].Path
		}
		return left < right
	})
	finishFindFilesPage(all, options.Offset, limit, &result)
	return result, nil
}

func finishFindFilesPage(all []FileMatch, offset, limit int, result *FindFilesResult) {
	start := offset
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	result.Matches = append(result.Matches, all[start:end]...)
	result.Returned = len(result.Matches)
	if end < len(all) && end <= FindFilesMaxOffset {
		result.HasMore = true
		next := end
		result.NextOffset = &next
	} else if end < len(all) {
		result.PageTruncated = true
	}
}
