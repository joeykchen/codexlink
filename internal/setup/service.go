// Package setup orchestrates CodexLink's idempotent one-command workspace setup.
package setup

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/joeykchen/codexlink/internal/config"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
	"github.com/joeykchen/codexlink/internal/workspace"
)

type Options struct {
	WorkspaceRoot string
	Port          int
	LocalOnly     bool
	SkipCodex     bool
	Reconnect     bool
}

type CodexIntegration struct {
	Configured   bool                      `json:"configured"`
	Skipped      bool                      `json:"skipped"`
	Skill        config.SkillInstallResult `json:"skill"`
	Sandbox      config.SandboxAllowResult `json:"sandbox"`
	SkillError   string                    `json:"skillError,omitempty"`
	SandboxError string                    `json:"sandboxError,omitempty"`
}

type Result struct {
	WorkspaceID           string                 `json:"workspaceId"`
	WorkspaceName         string                 `json:"workspaceName"`
	WorkspaceRoot         string                 `json:"workspaceRoot"`
	WorkspaceMode         workspace.TopologyMode `json:"workspaceMode"`
	RepositoryCount       int                    `json:"repositoryCount"`
	Spawned               bool                   `json:"spawned"`
	Port                  int                    `json:"port"`
	PublicURL             string                 `json:"publicUrl,omitempty"`
	MCPURL                string                 `json:"mcpUrl"`
	ConnectorName         string                 `json:"connectorName"`
	ConnectorAction       string                 `json:"connectorAction"`
	Authorized            bool                   `json:"authorized"`
	AuthorizationRequired bool                   `json:"authorizationRequired"`
	TokenCount            int                    `json:"tokenCount"`
	PairingCode           string                 `json:"pairingCode,omitempty"`
	PairingExpiresAt      int64                  `json:"pairingExpiresAt,omitempty"`
	SetupURL              string                 `json:"setupUrl,omitempty"`
	LocalOnly             bool                   `json:"localOnly"`
	Codex                 CodexIntegration       `json:"codex"`
}

type adminInfo struct {
	PublicURL        string                 `json:"publicUrl"`
	TokenCount       int                    `json:"tokenCount"`
	TokenRecordCount int                    `json:"tokenRecordCount"`
	WorkspaceMode    workspace.TopologyMode `json:"workspaceMode"`
	RepositoryCount  int                    `json:"repositoryCount"`
	Tunnel           struct {
		Running bool   `json:"running"`
		URL     string `json:"url"`
	} `json:"tunnel"`
}

type setupSessionRequest struct {
	ConnectorName   string `json:"connectorName"`
	ConnectorAction string `json:"connectorAction"`
	MCPURL          string `json:"mcpUrl"`
}

type setupSessionReply struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"`
	SetupURL  string `json:"setupUrl"`
}

type ensureFunc func(context.Context, string, string, stateruntime.EnsureOptions) (stateruntime.EnsureResult, error)
type adminFunc func(context.Context, stateruntime.State, string, string, any, any) error

// Service owns orchestration while CLI and browser presentation remain separate.
type Service struct {
	ensure           ensureFunc
	admin            adminFunc
	installSkill     func() (config.SkillInstallResult, error)
	configureSandbox func(string, string) (config.SandboxAllowResult, error)
}

func New() *Service {
	return &Service{
		ensure:           stateruntime.Ensure,
		admin:            stateruntime.AdminRequest,
		installSkill:     config.EnsureCodexLinkSkill,
		configureSandbox: config.EnsureCodexSandboxAllowlist,
	}
}

func (s *Service) Run(ctx context.Context, options Options) (Result, error) {
	if s == nil {
		s = New()
	}
	if strings.TrimSpace(options.WorkspaceRoot) == "" {
		options.WorkspaceRoot = "."
	}
	ws, err := workspace.New(options.WorkspaceRoot)
	if err != nil {
		return Result{}, err
	}
	repositories, err := ws.Repositories()
	if err != nil {
		return Result{}, err
	}
	mode, err := ws.TopologyMode()
	if err != nil {
		return Result{}, err
	}
	result := Result{
		WorkspaceID: ws.ID, WorkspaceName: ws.Name, WorkspaceRoot: ws.Root,
		WorkspaceMode: mode, RepositoryCount: len(repositories),
		LocalOnly: options.LocalOnly,
		Codex:     s.configureCodex(options.SkipCodex),
	}

	ensured, err := s.ensure(ctx, ws.Root, ws.ID, stateruntime.EnsureOptions{Port: options.Port})
	if err != nil {
		return Result{}, err
	}
	state := ensured.State
	result.Spawned = ensured.Spawned
	result.Port = state.Port

	var info adminInfo
	if err := s.admin(ctx, state, http.MethodGet, "/admin/info", nil, &info); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(info.PublicURL) != "" {
		state.PublicURL = strings.TrimRight(info.PublicURL, "/")
	} else if info.Tunnel.Running && strings.TrimSpace(info.Tunnel.URL) != "" {
		state.PublicURL = strings.TrimRight(info.Tunnel.URL, "/")
	} else {
		state.PublicURL = ""
	}
	if options.LocalOnly && info.Tunnel.Running {
		if err := s.admin(ctx, state, http.MethodPost, "/admin/tunnel/stop", nil, nil); err != nil {
			return Result{}, err
		}
		state.PublicURL = ""
	}
	if !options.LocalOnly && (!info.Tunnel.Running || state.PublicURL == "") {
		var tunnelReply struct {
			URL string `json:"url"`
		}
		if err := s.admin(ctx, state, http.MethodPost, "/admin/tunnel/start", nil, &tunnelReply); err != nil {
			return Result{}, err
		}
		if strings.TrimSpace(tunnelReply.URL) == "" {
			return Result{}, fmt.Errorf("tunnel started without returning a public URL")
		}
		state.PublicURL = strings.TrimRight(tunnelReply.URL, "/")
	}
	// Re-read after the final tunnel transition. The bridge reports tokenCount
	// for its current MCP audience, not for stale tunnel URLs.
	if err := s.admin(ctx, state, http.MethodGet, "/admin/info", nil, &info); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(info.PublicURL) != "" {
		state.PublicURL = strings.TrimRight(info.PublicURL, "/")
	} else if options.LocalOnly {
		state.PublicURL = ""
	}
	result.PublicURL = state.PublicURL
	result.MCPURL = fmt.Sprintf("http://127.0.0.1:%d/mcp", state.Port)
	if state.PublicURL != "" {
		result.MCPURL = config.MCPURL(state.PublicURL)
	}

	previous, err := config.ReadEndpoint(ws.ID)
	if err != nil {
		return Result{}, err
	}
	result.ConnectorName = config.ConnectorName(ws.Name, ws.ID, previous)
	var previousMCP *string
	if previous != nil {
		previousMCP = previous.MCPURL
	}
	result.ConnectorAction = config.ConnectorAction(previousMCP, &result.MCPURL)
	result.TokenCount = info.TokenCount
	if options.Reconnect || result.ConnectorAction == "update" {
		if err := s.admin(ctx, state, http.MethodPost, "/admin/revoke-all", nil, nil); err != nil {
			return Result{}, err
		}
		result.TokenCount = 0
	}
	result.Authorized = result.TokenCount > 0
	result.AuthorizationRequired = options.Reconnect || !result.Authorized || result.ConnectorAction != "none"

	endpoint := config.Endpoint{
		WorkspaceID: ws.ID, Port: state.Port, ConnectorName: result.ConnectorName, MCPURL: &result.MCPURL,
	}
	if result.PublicURL != "" {
		publicURL := result.PublicURL
		endpoint.PublicURL = &publicURL
	}
	if _, err := config.WriteEndpoint(endpoint); err != nil {
		return Result{}, err
	}
	if !result.AuthorizationRequired {
		return result, nil
	}

	var setupReply setupSessionReply
	request := setupSessionRequest{
		ConnectorName: result.ConnectorName, ConnectorAction: result.ConnectorAction, MCPURL: result.MCPURL,
	}
	if err := s.admin(ctx, state, http.MethodPost, "/admin/setup-session", request, &setupReply); err != nil {
		return Result{}, err
	}
	if setupReply.Code == "" || setupReply.ExpiresAt <= 0 || setupReply.SetupURL == "" {
		return Result{}, fmt.Errorf("bridge returned an incomplete setup session")
	}
	result.PairingCode = setupReply.Code
	result.PairingExpiresAt = setupReply.ExpiresAt
	result.SetupURL = setupReply.SetupURL
	return result, nil
}

func (s *Service) configureCodex(skip bool) CodexIntegration {
	result := CodexIntegration{Skipped: skip}
	if skip {
		return result
	}
	var skillErr error
	result.Skill, skillErr = s.installSkill()
	var sandboxErr error
	result.Sandbox, sandboxErr = s.configureSandbox("", config.StateDir())
	if skillErr != nil {
		result.SkillError = skillErr.Error()
	}
	if sandboxErr != nil {
		result.SandboxError = sandboxErr.Error()
	}
	result.Configured = skillErr == nil && sandboxErr == nil
	return result
}
