package mcp

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type ToolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type ToolHandler func(context.Context, map[string]any) (any, error)

type Tool struct {
	Definition ToolDefinition
	Scope      string
	Handler    ToolHandler
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool)}
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) Register(tool Tool) error {
	if tool.Definition.Name == "" || tool.Handler == nil {
		return fmt.Errorf("tool requires a name and handler")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[tool.Definition.Name]; exists {
		return fmt.Errorf("duplicate tool: %s", tool.Definition.Name)
	}
	r.tools[tool.Definition.Name] = tool
	r.order = append(r.order, tool.Definition.Name)
	sort.Strings(r.order)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) List(allowed func(Tool) bool) []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		if allowed == nil || allowed(tool) {
			definitions = append(definitions, tool.Definition)
		}
	}
	return definitions
}

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ToolError) Error() string { return e.Message }

func NewToolError(code, format string, args ...any) error {
	return &ToolError{Code: code, Message: fmt.Sprintf(format, args...)}
}
