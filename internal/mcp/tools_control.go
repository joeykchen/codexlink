package mcp

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/joeykchen/codexlink/internal/control"
)

func controlToolset(store *control.Store) []Tool {
	definition := ToolDefinition{
		Name: "submit_control_response", Title: "Submit workflow control response",
		Description: "Fill one locally prepared, bounded, expiring workflow response slot. This writes only ephemeral CodexLink control state outside the repository; it cannot modify files or execute commands.",
		InputSchema: objectSchema(map[string]any{
			"request_id": map[string]any{"type": "string", "pattern": `^cr_[A-Za-z0-9_-]{32}$`},
			"task_id":    map[string]any{"type": "string", "pattern": `^cl_[0-9a-f]{8}$`},
			"iteration":  map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000},
			"state":      map[string]any{"type": "string", "enum": []string{"PLAN", "DONE", "BLOCKED", "ERROR"}},
			"summary":    map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
			"plan": map[string]any{"type": "array", "maxItems": 16, "items": objectSchema(map[string]any{
				"description": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
				"files":       map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string", "maxLength": 512}},
				"tests":       map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string", "maxLength": 1024}},
			}, "description")},
		}, "request_id", "task_id", "iteration", "state", "summary"),
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
	tool := Tool{Definition: definition, Scope: scopeControlRespond, Handler: func(ctx context.Context, args map[string]any) (any, error) {
		encoded, err := json.Marshal(args)
		if err != nil || len(encoded) > 64<<10 {
			return nil, NewToolError("INVALID_ARGUMENT", "control response exceeds 64 KiB")
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		var submission control.Submission
		if err := decoder.Decode(&submission); err != nil {
			return nil, NewToolError("INVALID_ARGUMENT", "control response has invalid fields or types")
		}
		receipt, err := store.Submit(submission)
		if err != nil {
			if ce, ok := err.(*control.Error); ok {
				return nil, NewToolError(ce.Code, "%s", ce.Message)
			}
			return nil, NewToolError("CONTROL_OPERATION_FAILED", "control response could not be persisted")
		}
		return receipt, nil
	}}
	return []Tool{tool}
}
