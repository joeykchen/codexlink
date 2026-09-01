package mcp

import (
	"github.com/joeykchen/codexlink/internal/control"
	"github.com/joeykchen/codexlink/internal/workspace"
)

// WorkspaceTools is the composition root for all public read-only
// capabilities. Domain behavior remains in workspace/execution packages.
func WorkspaceTools(ws *workspace.Workspace, controls ...*control.Store) (*Registry, error) {
	workspaceTools := workspaceToolset(ws)
	gitTools := gitToolset(ws)
	executionTools := executionToolset(ws)
	var controlTools []Tool
	if len(controls) > 0 && controls[0] != nil {
		controlTools = controlToolset(controls[0])
	}
	tools := make([]Tool, 0, len(workspaceTools)+len(gitTools)+len(executionTools)+len(controlTools))
	tools = append(tools, workspaceTools...)
	tools = append(tools, gitTools...)
	tools = append(tools, executionTools...)
	tools = append(tools, controlTools...)
	return NewRegistry(tools...)
}
