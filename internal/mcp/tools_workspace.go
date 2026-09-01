package mcp

import (
	"context"
	"strings"

	"github.com/joeykchen/codexlink/internal/workspace"
)

type listDirectoryArgs struct {
	Path   string `json:"path"`
	Depth  int    `json:"depth"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}
type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}
type searchWorkspaceArgs struct {
	Query string `json:"query"`
	Path  string `json:"path"`
	Glob  string `json:"glob"`
	Limit int    `json:"limit"`
	Regex bool   `json:"regex"`
}
type findFilesArgs struct {
	Path     string `json:"path"`
	Glob     string `json:"glob"`
	MaxDepth int    `json:"max_depth"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}
type readFilesRequestArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}
type readFilesArgs struct {
	Files    []readFilesRequestArgs `json:"files"`
	MaxBytes int                    `json:"max_bytes"`
}
type fileOutlineArgs struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

func workspaceToolset(ws *workspace.Workspace) []Tool {
	return []Tool{
		newWorkspaceDataTool(ToolDefinition{Name: "workspace_info", Title: "Workspace information", Description: "Return workspace identity, detected project metadata, and a compact Git summary.", InputSchema: objectSchema(map[string]any{})}, scopeWorkspaceRead, func(ctx context.Context, _ noArgs) (any, error) {
			topology, err := ws.InspectTopology(ctx)
			if err != nil {
				return nil, err
			}
			git := workspace.GitInfo{IsRepo: false}
			if len(topology.Repositories) == 1 {
				git = topology.Repositories[0].Git
			}
			return map[string]any{"workspaceId": ws.ID, "name": ws.Name, "rootAlias": "workspace:/", "mode": topology.Mode, "repositoryCount": topology.RepositoryCount, "defaultRepository": topology.DefaultRepository, "repositories": topology.Repositories, "relations": topology.Relations, "project": ws.DetectProject(), "git": git}, nil
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "list_directory", Title: "List workspace directory", Description: "List safe files and directories below a workspace-relative path. Sensitive and generated paths are omitted.", InputSchema: objectSchema(map[string]any{"path": map[string]any{"type": "string", "description": "Workspace-relative path; defaults to ."}, "depth": map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "default": 1}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 200}, "offset": map[string]any{"type": "integer", "minimum": 0, "default": 0}})}, scopeWorkspaceRead, func(_ context.Context, args listDirectoryArgs) (any, error) {
			if args.Path == "" {
				args.Path = "."
			}
			if args.Depth == 0 {
				args.Depth = 1
			}
			if args.Limit == 0 {
				args.Limit = 200
			}
			if args.Depth < 1 || args.Depth > 4 {
				return nil, NewToolError("INVALID_ARGUMENT", "depth must be between 1 and 4")
			}
			if args.Limit < 1 || args.Limit > 1000 {
				return nil, NewToolError("INVALID_ARGUMENT", "limit must be between 1 and 1000")
			}
			if args.Offset < 0 {
				return nil, NewToolError("INVALID_ARGUMENT", "offset must not be negative")
			}
			return ws.ListDirectory(args.Path, workspace.ListOptions{Depth: args.Depth, Limit: args.Limit, Offset: args.Offset})
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "read_file", Title: "Read workspace file", Description: "Read a bounded line range from a non-sensitive text file inside the workspace.", InputSchema: objectSchema(map[string]any{"path": map[string]any{"type": "string", "description": "Workspace-relative file path"}, "start_line": map[string]any{"type": "integer", "minimum": 1, "default": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}}, "path")}, scopeWorkspaceRead, func(_ context.Context, args readFileArgs) (any, error) {
			if strings.TrimSpace(args.Path) == "" {
				return nil, NewToolError("INVALID_ARGUMENT", "path is required")
			}
			if args.StartLine == 0 {
				args.StartLine = 1
			}
			if args.StartLine < 1 || (args.EndLine != 0 && args.EndLine < args.StartLine) {
				return nil, NewToolError("INVALID_ARGUMENT", "invalid line range")
			}
			return ws.ReadFile(args.Path, workspace.ReadFileOptions{StartLine: args.StartLine, EndLine: args.EndLine})
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "search_workspace", Title: "Search workspace", Description: "Search text across safe workspace files using ripgrep when available and a bounded Go fallback otherwise.", InputSchema: objectSchema(map[string]any{"query": map[string]any{"type": "string", "minLength": 2}, "path": map[string]any{"type": "string", "default": "."}, "glob": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50}, "regex": map[string]any{"type": "boolean", "default": false}}, "query")}, scopeWorkspaceSearch, func(ctx context.Context, args searchWorkspaceArgs) (any, error) {
			if len(strings.TrimSpace(args.Query)) < 2 {
				return nil, NewToolError("INVALID_ARGUMENT", "query must contain at least two characters")
			}
			if args.Path == "" {
				args.Path = "."
			}
			if args.Limit == 0 {
				args.Limit = 50
			}
			if args.Limit < 1 || args.Limit > 200 {
				return nil, NewToolError("INVALID_ARGUMENT", "limit must be between 1 and 200")
			}
			return ws.Search(ctx, workspace.SearchOptions{Query: args.Query, Path: args.Path, Glob: args.Glob, Limit: args.Limit, Regex: args.Regex})
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "find_files", Title: "Find workspace files", Description: "Find ordinary files by a bounded glob without reading their contents. Sensitive, generated, and symlink paths are omitted.", InputSchema: objectSchema(map[string]any{"path": map[string]any{"type": "string", "default": "."}, "glob": map[string]any{"type": "string", "minLength": 1, "maxLength": workspace.FindFilesMaxGlobBytes}, "max_depth": map[string]any{"type": "integer", "minimum": 1, "maximum": workspace.FindFilesMaxDepth, "default": workspace.FindFilesDefaultDepth}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": workspace.FindFilesMaxLimit, "default": workspace.FindFilesDefaultLimit}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": workspace.FindFilesMaxOffset, "default": 0}}, "glob")}, scopeWorkspaceSearch, func(ctx context.Context, args findFilesArgs) (any, error) {
			return ws.FindFiles(ctx, workspace.FindFilesOptions{Path: args.Path, Glob: args.Glob, MaxDepth: args.MaxDepth, Limit: args.Limit, Offset: args.Offset})
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "read_files", Title: "Read multiple workspace files", Description: "Read bounded line ranges from up to 16 non-sensitive text files under one aggregate output budget.", InputSchema: objectSchema(map[string]any{"files": map[string]any{"type": "array", "minItems": 1, "maxItems": workspace.ReadFilesMaxCount, "items": objectSchema(map[string]any{"path": map[string]any{"type": "string"}, "start_line": map[string]any{"type": "integer", "minimum": 1, "default": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}}, "path")}, "max_bytes": map[string]any{"type": "integer", "minimum": workspace.ReadFilesMinBytes, "maximum": workspace.ReadFilesMaxBytes, "default": workspace.ReadFilesDefaultBytes}}, "files")}, scopeWorkspaceRead, func(_ context.Context, args readFilesArgs) (any, error) {
			requests := make([]workspace.ReadFileRequest, 0, len(args.Files))
			for _, file := range args.Files {
				requests = append(requests, workspace.ReadFileRequest{Path: file.Path, StartLine: file.StartLine, EndLine: file.EndLine})
			}
			return ws.ReadFiles(workspace.ReadFilesOptions{Files: requests, MaxBytes: args.MaxBytes})
		}),
		newWorkspaceDataTool(ToolDefinition{Name: "file_outline", Title: "Outline source file", Description: "Return declarations from a bounded Go file or headings from a bounded Markdown file without returning bodies, comments, source expression literal values, or struct tags. Import paths remain normalized metadata.", InputSchema: objectSchema(map[string]any{"path": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": workspace.FileOutlineMaxLimit, "default": workspace.FileOutlineDefaultLimit}}, "path")}, scopeWorkspaceRead, func(_ context.Context, args fileOutlineArgs) (any, error) {
			return ws.OutlineFile(args.Path, args.Limit)
		}),
	}
}
