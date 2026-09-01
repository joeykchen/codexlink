package setupui

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const ChatGPTAppsURL = "https://chatgpt.com/plugins"

type Session struct {
	ID              string
	WorkspaceName   string
	ConnectorName   string
	ConnectorAction string
	MCPURL          string
	PairingCode     string
	ExpiresAt       time.Time
}

type AuthorizationState string

const (
	StateWaiting   AuthorizationState = "waiting"
	StateFinishing AuthorizationState = "finishing"
	StateConnected AuthorizationState = "connected"
	StateFailed    AuthorizationState = "failed"
)

type Status struct {
	State      AuthorizationState `json:"state"`
	Authorized bool               `json:"authorized"`
	TokenCount int                `json:"tokenCount"`
	Message    string             `json:"message,omitempty"`
}

type StatusProvider func(Session) Status

type Manager struct {
	mu       sync.Mutex
	sessions map[string]Session
	now      func() time.Time
	status   StatusProvider
}

func New(providers ...StatusProvider) *Manager {
	manager := &Manager{sessions: make(map[string]Session), now: time.Now}
	if len(providers) > 0 {
		manager.status = providers[0]
	}
	return manager
}

func (m *Manager) Create(session Session) (Session, error) {
	if strings.TrimSpace(session.WorkspaceName) == "" || strings.TrimSpace(session.ConnectorName) == "" {
		return Session{}, fmt.Errorf("workspace and connector names are required")
	}
	if !strings.HasSuffix(strings.TrimRight(session.MCPURL, "/"), "/mcp") {
		return Session{}, fmt.Errorf("MCP URL must end with /mcp")
	}
	if strings.TrimSpace(session.PairingCode) == "" || session.ExpiresAt.IsZero() {
		return Session{}, fmt.Errorf("pairing code and expiration are required")
	}
	id, err := randomID(24)
	if err != nil {
		return Session{}, err
	}
	session.ID = id
	session.WorkspaceName = truncate(session.WorkspaceName, 160)
	session.ConnectorName = truncate(session.ConnectorName, 200)
	session.MCPURL = strings.TrimSpace(session.MCPURL)
	session.ConnectorAction = strings.TrimSpace(session.ConnectorAction)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	// PairingManager allows one live code. Keep the browser model equally
	// simple: creating a new setup session invalidates every older setup page.
	m.sessions = map[string]Session{id: session}
	return session, nil
}

func (m *Manager) Lookup(id string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	session, ok := m.sessions[id]
	return session, ok
}

func (m *Manager) pruneLocked() {
	now := m.now()
	for id, session := range m.sessions {
		if !now.Before(session.ExpiresAt) {
			delete(m.sessions, id)
		}
	}
}

func (m *Manager) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || !directLoopbackRequest(request) {
		http.NotFound(response, request)
		return
	}
	relative := strings.Trim(strings.TrimPrefix(request.URL.Path, "/setup/"), "/")
	parts := strings.Split(relative, "/")
	if relative == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "status") {
		http.NotFound(response, request)
		return
	}
	session, ok := m.Lookup(parts[0])
	if !ok {
		secureHeaders(response, "")
		if len(parts) == 2 {
			writeStatusJSON(response, http.StatusGone, Status{State: StateFailed, Message: "This setup session expired. Run codexlink again."})
			return
		}
		http.Error(response, "This setup link expired. Run codexlink again.", http.StatusGone)
		return
	}
	if len(parts) == 2 {
		status := Status{State: StateWaiting}
		if m.status != nil {
			status = m.status(session)
		}
		if status.Authorized {
			status.State = StateConnected
		}
		if status.State == "" {
			status.State = StateWaiting
		}
		secureHeaders(response, "")
		writeStatusJSON(response, http.StatusOK, status)
		return
	}
	nonce, err := randomID(18)
	if err != nil {
		http.Error(response, "setup page unavailable", http.StatusInternalServerError)
		return
	}
	secureHeaders(response, nonce)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_ = setupTemplate.Execute(response, struct {
		Session
		ChatGPTURL string
		Nonce      string
		ExpiresIn  string
	}{Session: session, ChatGPTURL: ChatGPTAppsURL, Nonce: nonce, ExpiresIn: formatRemaining(m.now(), session.ExpiresAt)})
}

func writeStatusJSON(response http.ResponseWriter, status int, value Status) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func directLoopbackRequest(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	forwarded := request.Header.Get("CF-Connecting-IP") != "" || request.Header.Get("X-Forwarded-For") != "" || request.Header.Get("Forwarded") != ""
	return ip != nil && ip.IsLoopback() && !forwarded
}

func secureHeaders(response http.ResponseWriter, nonce string) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	policy := "default-src 'none'; style-src 'unsafe-inline'; img-src 'none'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	if nonce != "" {
		policy += "; script-src 'nonce-" + nonce + "'"
	}
	response.Header().Set("Content-Security-Policy", policy)
}

func randomID(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func truncate(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}

func formatRemaining(now, expires time.Time) string {
	remaining := time.Until(expires)
	if !now.IsZero() {
		remaining = expires.Sub(now)
	}
	minutes := int(remaining.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		return "less than one minute"
	}
	if minutes == 1 {
		return "about one minute"
	}
	return fmt.Sprintf("about %d minutes", minutes)
}

var setupTemplate = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>CodexLink setup</title>
<style>
:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;color-scheme:light dark;--bg:#f4f7fb;--card:#fff;--text:#172033;--muted:#667085;--line:#d9e0ec;--accent:#315eea;--ok:#087443}*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(circle at top,#e9efff,var(--bg) 42%);color:var(--text);display:grid;place-items:center;padding:24px}main{width:min(760px,100%);background:var(--card);border:1px solid var(--line);border-radius:22px;box-shadow:0 24px 80px rgba(30,45,70,.14);padding:34px}h1{font-size:1.65rem;margin:0}.lead{color:var(--muted);margin:8px 0 26px;line-height:1.5}.connection{margin:18px 0 4px;padding:12px 14px;border:1px solid var(--line);border-radius:12px;color:var(--muted);font-weight:700}.connection.connected{border-color:var(--ok);color:var(--ok);background:color-mix(in srgb,var(--card) 90%,#e7f8ef)}.connection.failed{border-color:#b42318;color:#b42318;background:color-mix(in srgb,var(--card) 92%,#fee4e2)}.status{display:inline-flex;align-items:center;gap:7px;color:var(--ok);font-weight:700;font-size:.88rem;margin-bottom:12px}.status:before{content:"✓";display:grid;place-items:center;width:20px;height:20px;border-radius:50%;background:#e7f8ef}.step{border-top:1px solid var(--line);padding:22px 0}.step:first-of-type{border-top:0}.number{display:inline-grid;place-items:center;width:28px;height:28px;border-radius:9px;background:#edf1ff;color:var(--accent);font-weight:800;margin-right:8px}.label{font-weight:750}.value{display:flex;gap:10px;align-items:center;margin-top:10px;padding:13px 14px;border:1px solid var(--line);border-radius:12px;background:color-mix(in srgb,var(--card) 88%,#e9efff);font:600 .92rem ui-monospace,SFMono-Regular,Consolas,monospace;word-break:break-all}.value code{flex:1}.code{font-size:1.35rem;letter-spacing:.14em}.button,button{border:0;border-radius:10px;padding:10px 13px;background:#e9eeff;color:#2449bf;font-weight:750;cursor:pointer;white-space:nowrap;text-decoration:none}.primary{display:inline-flex;margin-top:12px;background:var(--accent);color:white;padding:12px 17px}.hint{font-size:.84rem;line-height:1.5;color:var(--muted);margin:9px 0 0}.action{font-size:.82rem;color:var(--muted);margin-top:7px}.copied{color:var(--ok)!important}@media(max-width:620px){main{padding:24px}.value{align-items:stretch;flex-direction:column}.button,button{width:100%}}@media(prefers-color-scheme:dark){:root{--bg:#0f1420;--card:#171e2e;--text:#edf2ff;--muted:#aeb8cc;--line:#303a52;--accent:#7395ff;--ok:#55d693}.status:before{background:#183d2d}.button,button{background:#26375f;color:#b8c8ff}.primary{background:#466fef;color:white}}
</style>
</head>
<body><main>
<div class="status">Local bridge is ready</div>
<h1>Connect {{.WorkspaceName}} to ChatGPT</h1>
<p class="lead">CodexLink prepared a read-only MCP endpoint. Complete the following one-time ChatGPT step; this page stays only on your computer.</p>
<section class="step"><span class="number">1</span><span class="label">Open ChatGPT Apps</span><br><a class="button primary" href="{{.ChatGPTURL}}" target="_blank" rel="noreferrer">Open ChatGPT</a></section>
<section class="step"><span class="number">2</span><span class="label">Create an app with this name</span><div class="value"><code id="connector">{{.ConnectorName}}</code><button data-copy="connector">Copy</button></div>{{if eq .ConnectorAction "update"}}<p class="action">The endpoint changed. Replace the existing app for this workspace.</p>{{else if eq .ConnectorAction "none"}}<p class="action">The saved endpoint is unchanged. Reconnect only if ChatGPT asks.</p>{{end}}</section>
<section class="step"><span class="number">3</span><span class="label">Use this MCP endpoint</span><div class="value"><code id="mcp">{{.MCPURL}}</code><button data-copy="mcp">Copy</button></div></section>
<section class="step"><span class="number">4</span><span class="label">Enter this code on the authorization page</span><div class="value code"><code id="pairing">{{.PairingCode}}</code><button data-copy="pairing">Copy</button></div><p class="hint">Valid for {{.ExpiresIn}}. The code is destroyed after successful authorization.</p></section>
<section class="step"><span class="number">5</span><span class="label">Open it in regular Chat</span><p class="hint">After ChatGPT creates the app, open its details and choose <strong>Try in chat</strong>. If ChatGPT opens Work, switch to <strong>Chat</strong>; CodexLink stays attached to the new conversation.</p></section>
<div class="connection" id="connection" role="status">Waiting for ChatGPT authorization…</div><p class="hint">ChatGPT receives only the files and Git information it explicitly requests through CodexLink's read-only tools. It cannot edit files or execute commands through this connection.</p>
</main>
<script nonce="{{.Nonce}}">document.querySelectorAll('[data-copy]').forEach(function(b){b.addEventListener('click',async function(){var n=document.getElementById(b.dataset.copy);try{await navigator.clipboard.writeText(n.textContent);var old=b.textContent;b.textContent='Copied';b.classList.add('copied');setTimeout(function(){b.textContent=old;b.classList.remove('copied')},1200)}catch(e){var r=document.createRange();r.selectNodeContents(n);var s=getSelection();s.removeAllRanges();s.addRange(r)}})});var connection=document.getElementById('connection');async function poll(){try{var response=await fetch(location.pathname.replace(/\/$/,'')+'/status',{cache:'no-store'});var status=await response.json();if(status.state==='connected'){connection.textContent='Connected. Open the app details in ChatGPT, choose Try in chat, then switch to Chat if Work opens.';connection.classList.add('connected');return}if(status.state==='failed'){connection.textContent=status.message||'Authorization did not complete. Run codexlink again.';connection.classList.add('failed');return}if(status.state==='finishing'){connection.textContent='Authorization approved. ChatGPT is finishing the secure token exchange…'}else{connection.textContent='Waiting for ChatGPT authorization…'}}catch(e){}setTimeout(poll,1000)}poll()</script>
</body></html>`))
