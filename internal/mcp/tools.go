package mcp

import "github.com/joeykchen/codexlink/internal/workspace"

// WorkspaceTools is the composition root for all public read-only
// capabilities. Domain behavior remains in workspace/execution packages.
func WorkspaceTools(ws *workspace.Workspace) (*Registry, error) {
	workspaceTools := workspaceToolset(ws)
	gitTools := gitToolset(ws)
	executionTools := executionToolset(ws)
	tools := make([]Tool, 0, len(workspaceTools)+len(gitTools)+len(executionTools))
	tools = append(tools, workspaceTools...)
	tools = append(tools, gitTools...)
	tools = append(tools, executionTools...)
	return NewRegistry(tools...)
}
