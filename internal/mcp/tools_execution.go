package mcp

import (
	"context"

	"github.com/joeykchen/codexlink/internal/execution"
	"github.com/joeykchen/codexlink/internal/workspace"
)

type executionSummaryArgs struct {
	Limit int `json:"limit"`
}

func executionToolset(ws *workspace.Workspace) []Tool {
	return []Tool{
		newReadOnlyTool(ToolDefinition{Name: "test_status", Title: "Latest test status", Description: "Return the most recent execution record produced by the local coding harness. The tool reads a record and never launches tests.", InputSchema: objectSchema(map[string]any{})}, scopeExecutionRead, func(_ context.Context, _ noArgs) (any, error) {
			record, err := execution.Latest(ws.ID)
			if err != nil {
				return nil, err
			}
			if record == nil {
				return map[string]any{"available": false, "message": "No execution record has been written for this workspace."}, nil
			}
			return map[string]any{"available": true, "record": record}, nil
		}),
		newReadOnlyTool(ToolDefinition{Name: "execution_summary", Title: "Execution history", Description: "Return recent bounded execution summaries for planning and independent review.", InputSchema: objectSchema(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 5}})}, scopeExecutionRead, func(_ context.Context, args executionSummaryArgs) (any, error) {
			if args.Limit == 0 {
				args.Limit = 5
			}
			if args.Limit < 1 || args.Limit > 50 {
				return nil, NewToolError("INVALID_ARGUMENT", "limit must be between 1 and 50")
			}
			records, err := execution.Read(ws.ID, args.Limit)
			if err != nil {
				return nil, err
			}
			return map[string]any{"records": records, "count": len(records)}, nil
		}),
	}
}
