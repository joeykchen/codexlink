package mcp

import (
	"context"

	"github.com/joeykchen/codexlink/internal/workspace"
)

type gitStatusArgs struct {
	Repository string `json:"repository"`
}
type gitDiffArgs struct {
	Repository string `json:"repository"`
	Mode       string `json:"mode"`
	Path       string `json:"path"`
	Offset     int    `json:"offset"`
	MaxBytes   int    `json:"max_bytes"`
}
type gitLogArgs struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

func gitToolset(ws *workspace.Workspace) []Tool {
	return []Tool{
		newWorkspaceDataTool(ToolDefinition{Name: "git_status", Title: "Git status", Description: "Return structured staged, unstaged, untracked, conflicted, branch, and upstream information without modifying the repository. For a workspace group, repository is required.", InputSchema: objectSchema(map[string]any{"repository": map[string]any{"type": "string", "description": "Workspace-relative repository path; required when multiple repositories are present"}})}, scopeGitRead, func(ctx context.Context, args gitStatusArgs) (any, error) {
			return ws.GitStatusFor(ctx, args.Repository)
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "git_diff", Title: "Git diff", Description: "Return a byte-paginated Git diff after excluding sensitive paths and external diff drivers. For a workspace group, repository is required.", InputSchema: objectSchema(map[string]any{"repository": map[string]any{"type": "string", "description": "Workspace-relative repository path; required when multiple repositories are present"}, "mode": map[string]any{"type": "string", "enum": []string{"unstaged", "staged", "head"}, "default": "unstaged"}, "path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0, "default": 0}, "max_bytes": map[string]any{"type": "integer", "minimum": 1024, "maximum": 262144, "default": 65536}})}, scopeGitRead, func(ctx context.Context, args gitDiffArgs) (any, error) {
			if args.Mode == "" {
				args.Mode = string(workspace.DiffUnstaged)
			}
			if args.Mode != string(workspace.DiffUnstaged) && args.Mode != string(workspace.DiffStaged) && args.Mode != string(workspace.DiffHead) {
				return nil, NewToolError("INVALID_ARGUMENT", "mode must be unstaged, staged, or head")
			}
			if args.Offset < 0 {
				return nil, NewToolError("INVALID_ARGUMENT", "offset must not be negative")
			}
			if args.MaxBytes == 0 {
				args.MaxBytes = 64 * 1024
			}
			if args.MaxBytes < 1024 || args.MaxBytes > 256*1024 {
				return nil, NewToolError("INVALID_ARGUMENT", "max_bytes must be between 1024 and 262144")
			}
			return ws.GitDiff(ctx, workspace.GitDiffOptions{Repository: args.Repository, Mode: workspace.DiffMode(args.Mode), Path: args.Path, Offset: args.Offset, MaxBytes: args.MaxBytes})
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "git_log", Title: "Git history", Description: "Return bounded metadata for commits reachable from the current HEAD. Arbitrary revisions and historical file contents are not supported. For a workspace group, repository is required.", InputSchema: objectSchema(map[string]any{"repository": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": workspace.GitLogMaxLimit, "default": workspace.GitLogDefaultLimit}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": workspace.GitLogMaxOffset, "default": 0}})}, scopeGitRead, func(ctx context.Context, args gitLogArgs) (any, error) {
			return ws.GitLog(ctx, workspace.GitLogOptions{Repository: args.Repository, Path: args.Path, Limit: args.Limit, Offset: args.Offset})
		}),
	}
}
