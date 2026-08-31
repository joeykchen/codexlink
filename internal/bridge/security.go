package bridge

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func (s *Server) mcpOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		origin := strings.TrimSpace(request.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(response, request)
			return
		}
		normalized, err := normalizeOrigin(origin)
		if err != nil || !s.allowedOrigin(normalized) {
			response.Header().Set("Content-Type", "application/json; charset=utf-8")
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"jsonrpc": "2.0",
				"error":   map[string]any{"code": -32000, "message": "Origin is not allowed."},
				"id":      nil,
			})
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (s *Server) allowedOrigin(origin string) bool {
	allowed := []string{s.LocalBaseURL(), "https://chatgpt.com", "https://www.chatgpt.com", "https://chat.openai.com"}
	if public := s.PublicURL(); public != "" {
		allowed = append(allowed, public)
	}
	for _, configured := range strings.Split(os.Getenv("CODEXLINK_ALLOWED_ORIGINS"), ",") {
		if configured = strings.TrimSpace(configured); configured != "" {
			allowed = append(allowed, configured)
		}
	}
	for _, candidate := range allowed {
		normalized, err := normalizeOrigin(candidate)
		if err == nil && subtle.ConstantTimeCompare([]byte(normalized), []byte(origin)) == 1 {
			return true
		}
	}
	return false
}

func normalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid origin scheme")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("origin must not include a path")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			host = request.RemoteAddr
		}
		ip := net.ParseIP(host)
		forwarded := request.Header.Get("CF-Connecting-IP") != "" || request.Header.Get("X-Forwarded-For") != "" || request.Header.Get("Forwarded") != ""
		header := strings.TrimSpace(request.Header.Get("Authorization"))
		token := ""
		if len(header) >= 7 && strings.EqualFold(header[:7], "Bearer ") {
			token = strings.TrimSpace(header[7:])
		}
		validToken := subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) == 1
		if ip == nil || !ip.IsLoopback() || forwarded || !validToken {
			http.NotFound(response, request)
			return
		}
		next.ServeHTTP(response, request)
	})
}
