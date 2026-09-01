package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/config"
	"github.com/joeykchen/codexlink/internal/logging"
	"github.com/joeykchen/codexlink/internal/mcp"
	stateruntime "github.com/joeykchen/codexlink/internal/runtime"
	"github.com/joeykchen/codexlink/internal/setupui"
	"github.com/joeykchen/codexlink/internal/state"
	"github.com/joeykchen/codexlink/internal/tunnel"
	"github.com/joeykchen/codexlink/internal/workspace"
)

type Options struct {
	WorkspaceRoot  string
	Host           string
	Port           int
	Logger         *logging.Logger
	AuthStoreFile  string
	PairingTTL     time.Duration
	AccessTokenTTL time.Duration
}

type Server struct {
	Workspace       *workspace.Workspace
	Host            string
	Port            int
	AdminToken      string
	PublicRef       string
	AuthStore       *auth.Store
	Pairing         *auth.PairingManager
	Tunnel          tunnel.Provider
	SetupUI         *setupui.Manager
	TopologyMode    workspace.TopologyMode
	RepositoryCount int
	logger          *logging.Logger
	ownsLogger      bool
	httpServer      *http.Server
	listener        net.Listener
	startedAt       time.Time
	mu              sync.RWMutex
	publicURL       string
	closeOnce       sync.Once
	done            chan struct{}
	lockPath        string
}

type lockRecord struct {
	PID       int   `json:"pid"`
	CreatedAt int64 `json:"createdAt"`
}

func Start(options Options) (*Server, error) {
	if strings.TrimSpace(options.WorkspaceRoot) == "" {
		options.WorkspaceRoot = "."
	}
	ws, err := workspace.New(options.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	repositories, err := ws.Repositories()
	if err != nil {
		return nil, err
	}
	topologyMode, err := ws.TopologyMode()
	if err != nil {
		return nil, err
	}
	if live, _ := stateruntime.FindLive(context.Background(), ws.ID); live != nil {
		return nil, fmt.Errorf("a bridge is already running for this workspace on port %d", live.Port)
	}
	lockPath, err := acquireWorkspaceLock(ws.ID)
	if err != nil {
		return nil, err
	}
	cleanupLock := true
	defer func() {
		if cleanupLock {
			_ = os.Remove(lockPath)
		}
	}()

	host := strings.TrimSpace(options.Host)
	if host == "" || host == "localhost" {
		host = config.DefaultHost
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("bridge host must be a loopback address")
	}
	port := options.Port
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", port)
	}
	if port == 0 {
		port = config.DefaultPort
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil && port != 0 {
		listener, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		return nil, err
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	logger := options.Logger
	ownsLogger := false
	if logger == nil {
		logger, err = logging.New("bridge", false)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		ownsLogger = true
	}
	store, err := auth.NewStore(ws.ID, auth.StoreOptions{File: options.AuthStoreFile, AccessTokenTTL: options.AccessTokenTTL})
	if err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}
	pairing := auth.NewPairingManager(ws.ID, auth.PairingOptions{TTL: options.PairingTTL})
	tunnelState, err := tunnel.ReadState(ws.ID)
	if err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}
	provider, err := tunnel.ProviderForState(tunnelState, logger)
	if err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}
	adminToken, err := stateruntime.NewAdminToken()
	if err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}
	publicRef, err := stateruntime.PublicRef(ws.ID)
	if err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}

	server := &Server{
		Workspace: ws, Host: host, Port: actualPort, AdminToken: adminToken, PublicRef: publicRef,
		AuthStore: store, Pairing: pairing, Tunnel: provider,
		TopologyMode: topologyMode, RepositoryCount: len(repositories),
		logger: logger, ownsLogger: ownsLogger, listener: listener,
		startedAt: time.Now().UTC(), done: make(chan struct{}), lockPath: lockPath,
	}
	server.SetupUI = setupui.New(func(session setupui.Session) setupui.Status {
		count := store.TokenCountForAudience(session.MCPURL)
		if count > 0 {
			return setupui.Status{State: setupui.StateConnected, Authorized: true, TokenCount: count}
		}
		pairingStatus := pairing.Status()
		switch pairingStatus.State {
		case auth.PairingStateActive:
			return setupui.Status{State: setupui.StateWaiting}
		case auth.PairingStateConsumed:
			if time.Since(pairingStatus.ChangedAt) <= time.Minute {
				return setupui.Status{State: setupui.StateFinishing}
			}
			return setupui.Status{State: setupui.StateFailed, Message: "ChatGPT did not finish the token exchange. Run codexlink again."}
		case auth.PairingStateExpired:
			return setupui.Status{State: setupui.StateFailed, Message: "The pairing code expired. Run codexlink again."}
		case auth.PairingStateLocked:
			return setupui.Status{State: setupui.StateFailed, Message: "The pairing code was locked after too many attempts. Run codexlink again."}
		default:
			return setupui.Status{State: setupui.StateFailed, Message: "No active pairing session exists. Run codexlink again."}
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/health", server.health)
	mux.Handle("/setup/", server.SetupUI)
	oauthServer := auth.NewOAuthServer(store, pairing, ws.Name, server.baseURL, logger)
	oauthServer.Register(mux)
	registry, err := mcp.WorkspaceTools(ws)
	if err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}
	mcpHandler := mcp.NewHandler(registry, logger)
	mux.Handle("/mcp", server.mcpOriginGuard(auth.BearerMiddleware(store, server.baseURL, mcpHandler)))
	server.registerAdmin(mux)
	server.httpServer = &http.Server{
		Handler:           server.recoverAndLog(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       35 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}
	if err := server.persistRuntime(); err != nil {
		_ = listener.Close()
		if ownsLogger {
			_ = logger.Close()
		}
		return nil, err
	}
	cleanupLock = false
	go func() {
		err := server.httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly: %v", err)
			go server.Close()
		}
	}()
	logger.Info("bridge listening on %s:%d for workspace %s (%s)", host, actualPort, ws.Name, ws.ID)
	return server, nil
}

func (s *Server) baseURL(_ *http.Request) string {
	s.mu.RLock()
	public := s.publicURL
	s.mu.RUnlock()
	if public != "" {
		return strings.TrimRight(public, "/")
	}
	return "http://" + net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

func (s *Server) LocalBaseURL() string {
	return "http://" + net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

func (s *Server) PublicURL() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.publicURL }

func (s *Server) Done() <-chan struct{} { return s.done }

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(response, http.StatusOK, stateruntime.Health{Service: buildinfo.ServiceName, Version: buildinfo.Version, WorkspaceRef: s.PublicRef, PID: os.Getpid(), Status: "ok"})
}

func (s *Server) persistRuntime() error {
	return stateruntime.Write(stateruntime.State{
		Service: buildinfo.ServiceName, Version: buildinfo.Version, WorkspaceID: s.Workspace.ID, WorkspaceRoot: s.Workspace.Root,
		PublicRef: s.PublicRef, PID: os.Getpid(), Port: s.Port, AdminToken: s.AdminToken, PublicURL: s.PublicURL(),
		StartedAt: s.startedAt.Format(time.RFC3339Nano),
	})
}

func (s *Server) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		_ = s.Tunnel.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.httpServer != nil {
			if err := s.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				closeErr = err
			}
		}
		_ = stateruntime.Clear(s.Workspace.ID)
		_ = os.Remove(s.lockPath)
		s.logger.Info("bridge stopped")
		if s.ownsLogger {
			_ = s.logger.Close()
		}
		close(s.done)
	})
	return closeErr
}

func (s *Server) recoverAndLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic serving %s: %v", request.URL.Path, recovered)
				http.Error(response, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(response, request)
	})
}

func acquireWorkspaceLock(workspaceID string) (string, error) {
	dir, err := state.New(config.StateDir()).Bucket("locks")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, workspaceID+".lock")
	for attempt := 0; attempt < 2; attempt++ {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr == nil {
			record := lockRecord{PID: os.Getpid(), CreatedAt: time.Now().Unix()}
			_ = json.NewEncoder(file).Encode(record)
			_ = file.Sync()
			_ = file.Close()
			_ = os.Chmod(path, 0o600)
			return path, nil
		}
		if !errors.Is(createErr, os.ErrExist) {
			return "", createErr
		}
		var record lockRecord
		_, _ = state.ReadJSONFile(path, &record)
		age := time.Since(time.Unix(record.CreatedAt, 0))
		if age < 30*time.Second || (record.PID > 0 && processAlive(record.PID)) {
			return "", fmt.Errorf("workspace bridge is already running or starting (pid %d)", record.PID)
		}
		_ = os.Remove(path)
	}
	return "", fmt.Errorf("unable to acquire workspace bridge lock")
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
