package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/joeykchen/codexlink/internal/config"
	"github.com/joeykchen/codexlink/internal/state"
)

type Preference string

const (
	PreferenceUnset Preference = "unset"
	PreferenceQuick Preference = "quick"
	PreferenceNamed Preference = "named"
)

type State struct {
	WorkspaceID    string     `json:"workspaceId"`
	Preference     Preference `json:"preference"`
	AskedAt        string     `json:"askedAt,omitempty"`
	Provider       string     `json:"provider,omitempty"`
	TunnelName     string     `json:"tunnelName,omitempty"`
	TunnelID       string     `json:"tunnelId,omitempty"`
	Hostname       string     `json:"hostname,omitempty"`
	Zone           string     `json:"zone,omitempty"`
	ConfiguredAt   string     `json:"configuredAt,omitempty"`
	FallbackReason string     `json:"fallbackReason,omitempty"`
}

func tunnelRepository() state.Repository { return state.New(config.StateDir()) }

func ReadState(workspaceID string) (State, error) {
	state := State{WorkspaceID: workspaceID, Preference: PreferenceUnset}
	found, err := tunnelRepository().ReadJSON("tunnels", workspaceID, &state)
	if err != nil {
		return State{}, err
	}
	if !found {
		return state, nil
	}
	if state.WorkspaceID == "" {
		state.WorkspaceID = workspaceID
	}
	if state.Preference == "" {
		state.Preference = PreferenceUnset
	}
	return state, nil
}

func WriteState(state State) (State, error) {
	if state.WorkspaceID == "" {
		return State{}, fmt.Errorf("workspace ID is required")
	}
	if err := tunnelRepository().WriteJSON("tunnels", state.WorkspaceID, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func NeedsChoice(state State) bool { return state.Preference == PreferenceUnset || state.AskedAt == "" }

func NamedReady(state State) bool {
	return state.Preference == PreferenceNamed && strings.TrimSpace(state.TunnelName) != "" && strings.TrimSpace(state.Hostname) != ""
}

func ChooseQuick(workspaceID, reason string) (State, error) {
	return WriteState(State{
		WorkspaceID: workspaceID, Preference: PreferenceQuick, AskedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Provider: "cloudflare-quick", FallbackReason: reason,
	})
}

var hostnameRE = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)$`)

func NormalizeHostname(value string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if len(host) < 3 || len(host) > 253 || !hostnameRE.MatchString(host) {
		return "", fmt.Errorf("invalid DNS hostname: %s", value)
	}
	return host, nil
}

func ParseZone(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	return NormalizeHostname(value)
}

func HostnameSlug(workspaceName, workspaceID string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(workspaceName) {
		if r <= unicode.MaxASCII && ((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			builder.WriteRune(r)
			lastHyphen = false
		} else if builder.Len() > 0 && !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
		if builder.Len() >= 30 {
			break
		}
	}
	label := strings.Trim(builder.String(), "-")
	if label == "" {
		id := workspaceID
		if len(id) > 8 {
			id = id[:8]
		}
		label = "ws-" + id
	}
	return "codexlink-" + label
}

func SuggestedHostname(zone, workspaceName, workspaceID string) (string, error) {
	normalized, err := ParseZone(zone)
	if err != nil {
		return "", err
	}
	return NormalizeHostname(HostnameSlug(workspaceName, workspaceID) + "." + normalized)
}

func cloudflaredCertPath() string {
	if override := strings.TrimSpace(os.Getenv("TUNNEL_ORIGIN_CERT")); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", "cert.pem")
}

func HasCloudflaredCert() bool {
	info, err := os.Stat(cloudflaredCertPath())
	return err == nil && info.Mode().IsRegular()
}

func StablePublicRef(secret []byte, workspaceID string) string {
	digest := sha256.Sum256(append(append([]byte(nil), secret...), []byte(workspaceID)...))
	return hex.EncodeToString(digest[:])[:16]
}
