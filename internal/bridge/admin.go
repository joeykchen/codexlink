package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/setupui"
)

func (s *Server) registerAdmin(mux *http.ServeMux) {
	mux.Handle("/admin/setup-session", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			adminMethod(response, http.MethodPost)
			return
		}
		var body struct {
			ConnectorName   string `json:"connectorName"`
			ConnectorAction string `json:"connectorAction"`
			MCPURL          string `json:"mcpUrl"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64*1024))
		if err := decoder.Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "request body must be valid JSON"})
			return
		}
		if err := ensureSingleJSONValue(decoder); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": err.Error()})
			return
		}
		switch body.ConnectorAction {
		case "", "create", "update", "none":
		default:
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "connectorAction must be create, update, or none"})
			return
		}
		code, expiresAt, err := s.Pairing.Create()
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "pairing_failed", "message": err.Error()})
			return
		}
		session, err := s.SetupUI.Create(setupui.Session{
			WorkspaceName: s.Workspace.Name, ConnectorName: body.ConnectorName,
			ConnectorAction: body.ConnectorAction, MCPURL: body.MCPURL,
			PairingCode: code, ExpiresAt: expiresAt,
		})
		if err != nil {
			s.Pairing.Invalidate()
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": err.Error()})
			return
		}
		s.logger.Info("created local setup session")
		writeJSON(response, http.StatusOK, map[string]any{
			"code": code, "expiresAt": expiresAt.UnixMilli(),
			"setupUrl": s.LocalBaseURL() + "/setup/" + session.ID,
		})
	})))
	mux.Handle("/admin/pairing", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			adminMethod(response, http.MethodPost)
			return
		}
		code, expiresAt, err := s.Pairing.Create()
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "pairing_failed", "message": err.Error()})
			return
		}
		s.logger.Info("created a one-time pairing session")
		writeJSON(response, http.StatusOK, map[string]any{"code": code, "expiresAt": expiresAt.UnixMilli()})
	})))
	mux.Handle("/admin/info", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			adminMethod(response, http.MethodGet)
			return
		}
		audience := strings.TrimRight(s.baseURL(request), "/") + "/mcp"
		writeJSON(response, http.StatusOK, map[string]any{
			"service": buildinfo.ServiceName, "version": buildinfo.Version,
			"workspaceId": s.Workspace.ID, "workspaceName": s.Workspace.Name, "workspaceRoot": s.Workspace.Root,
			"workspaceMode": s.TopologyMode, "repositoryCount": s.RepositoryCount,
			"port": s.Port, "publicUrl": nullableString(s.PublicURL()), "tunnel": s.Tunnel.Status(),
			"tokenCount": s.AuthStore.TokenCountForAudience(audience), "tokenRecordCount": s.AuthStore.TokenCount(),
			"pairingActive": s.Pairing.Active(), "audience": audience,
			"controlResponseAuthorized": s.AuthStore.HasActiveScopeForAudience(audience, auth.ScopeControlRespond),
			"pid":                       os.Getpid(), "startedAt": s.startedAt.Format(time.RFC3339Nano),
		})
	})))
	s.registerControlAdmin(mux)
	mux.Handle("/admin/tunnel/start", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			adminMethod(response, http.MethodPost)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		publicURL, err := s.Tunnel.Start(ctx, s.Port)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "tunnel_failed", "message": err.Error()})
			return
		}
		s.mu.Lock()
		s.publicURL = strings.TrimRight(publicURL, "/")
		s.mu.Unlock()
		if err := s.persistRuntime(); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "runtime_state_failed", "message": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"url": publicURL})
	})))
	mux.Handle("/admin/tunnel/stop", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			adminMethod(response, http.MethodPost)
			return
		}
		if err := s.Tunnel.Stop(); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "tunnel_stop_failed", "message": err.Error()})
			return
		}
		s.mu.Lock()
		s.publicURL = ""
		s.mu.Unlock()
		_ = s.persistRuntime()
		writeJSON(response, http.StatusOK, map[string]any{"stopped": true})
	})))
	mux.Handle("/admin/revoke-all", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			adminMethod(response, http.MethodPost)
			return
		}
		count, err := s.AuthStore.RevokeAll()
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "revoke_failed", "message": err.Error()})
			return
		}
		s.Pairing.Invalidate()
		writeJSON(response, http.StatusOK, map[string]any{"revoked": count})
	})))
	mux.Handle("/admin/shutdown", s.adminOnly(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			adminMethod(response, http.MethodPost)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"shuttingDown": true})
		go func() { time.Sleep(50 * time.Millisecond); _ = s.Close() }()
	})))
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("request body must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid JSON after request object")
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func adminMethod(response http.ResponseWriter, allowed string) {
	response.Header().Set("Allow", allowed)
	writeJSON(response, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
