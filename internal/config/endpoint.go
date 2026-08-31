package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/joeykchen/codexlink/internal/state"
)

const DefaultConnectorName = "CodexLink"

type Endpoint struct {
	WorkspaceID   string  `json:"workspaceId"`
	Port          int     `json:"port"`
	PublicURL     *string `json:"publicUrl"`
	MCPURL        *string `json:"mcpUrl"`
	ConnectorName string  `json:"connectorName"`
	SavedAt       string  `json:"savedAt"`
}

func endpointRepository() state.Repository { return state.New(StateDir()) }

func ReadEndpoint(workspaceID string) (*Endpoint, error) {
	var endpoint Endpoint
	found, err := endpointRepository().ReadJSON("endpoints", workspaceID, &endpoint)
	if err != nil || !found {
		return nil, err
	}
	return &endpoint, nil
}

func WriteEndpoint(endpoint Endpoint) (Endpoint, error) {
	endpoint.SavedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := endpointRepository().WriteJSON("endpoints", endpoint.WorkspaceID, endpoint); err != nil {
		return Endpoint{}, err
	}
	return endpoint, nil
}

func NormalizePublicURL(value string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(value), "/"))
}

func MCPURL(publicURL string) string {
	base := strings.TrimSuffix(NormalizePublicURL(publicURL), "/mcp")
	return base + "/mcp"
}

func ConnectorAction(previous, next *string) string {
	if next == nil || strings.TrimSpace(*next) == "" {
		return "none"
	}
	if previous == nil || strings.TrimSpace(*previous) == "" {
		return "create"
	}
	if NormalizePublicURL(*previous) == NormalizePublicURL(*next) {
		return "none"
	}
	return "update"
}

var connectorUnsafe = regexp.MustCompile(`[^\p{L}\p{N}._\- ]+`)
var repeatedSpace = regexp.MustCompile(`\s+`)

func ConnectorName(workspaceName, workspaceID string, previous *Endpoint) string {
	if previous != nil && strings.TrimSpace(previous.ConnectorName) != "" {
		return strings.TrimSpace(previous.ConnectorName)
	}
	label := repeatedSpace.ReplaceAllString(connectorUnsafe.ReplaceAllString(workspaceName, ""), " ")
	label = strings.TrimSpace(label)
	if len([]rune(label)) > 40 {
		label = string([]rune(label)[:40])
	}
	if label == "" {
		label = workspaceID[:minInt(6, len(workspaceID))]
	}
	return fmt.Sprintf("%s · %s", DefaultConnectorName, label)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
