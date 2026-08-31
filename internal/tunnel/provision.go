package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type ListedTunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Account interface {
	HasCert() bool
	Login(context.Context) error
	List(context.Context) ([]ListedTunnel, error)
	Create(context.Context, string) (ListedTunnel, error)
	RouteDNS(context.Context, string, string) error
}

type ProcessAccount struct{ Binary string }

func (a ProcessAccount) binary() (string, error) {
	binary := a.Binary
	if binary == "" {
		binary = FindBinary("cloudflared")
	}
	if binary == "" {
		return "", fmt.Errorf("NEED_CLOUDFLARED: cloudflared is not installed")
	}
	return binary, nil
}

func (a ProcessAccount) HasCert() bool { return HasCloudflaredCert() }

func (a ProcessAccount) Login(ctx context.Context) error {
	if a.HasCert() {
		return nil
	}
	binary, err := a.binary()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "tunnel", "login")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("Cloudflare login failed: %w", err)
	}
	if !a.HasCert() {
		return fmt.Errorf("Cloudflare login completed without creating an origin certificate")
	}
	return nil
}

func (a ProcessAccount) run(ctx context.Context, args ...string) (string, error) {
	binary, err := a.binary()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("cloudflared %s failed: %s", strings.Join(args, " "), text)
	}
	return text, nil
}

func (a ProcessAccount) List(ctx context.Context) ([]ListedTunnel, error) {
	output, err := a.run(ctx, "tunnel", "list", "--output", "json")
	if err == nil {
		var rows []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(output), &rows) == nil {
			result := make([]ListedTunnel, 0, len(rows))
			for _, row := range rows {
				if row.ID != "" && row.Name != "" {
					result = append(result, ListedTunnel{ID: row.ID, Name: row.Name})
				}
			}
			return result, nil
		}
	}
	output, err = a.run(ctx, "tunnel", "list")
	if err != nil {
		return nil, err
	}
	return ParseTunnelList(output), nil
}

var tunnelIDRE = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func ParseTunnelList(output string) []ListedTunnel {
	trimmed := strings.TrimSpace(output)
	var rows []ListedTunnel
	if strings.HasPrefix(trimmed, "[") {
		if json.Unmarshal([]byte(trimmed), &rows) == nil {
			return validListedTunnels(rows)
		}
	} else if strings.HasPrefix(trimmed, "{") {
		var envelope struct {
			Tunnels []ListedTunnel `json:"tunnels"`
		}
		if json.Unmarshal([]byte(trimmed), &envelope) == nil {
			return validListedTunnels(envelope.Tunnels)
		}
	}
	result := make([]ListedTunnel, 0)
	for _, line := range strings.Split(output, "\n") {
		id := tunnelIDRE.FindString(line)
		if id == "" {
			continue
		}
		rest := strings.TrimSpace(line[strings.Index(line, id)+len(id):])
		fields := strings.Fields(rest)
		if len(fields) > 0 && fields[0] != "NAME" {
			result = append(result, ListedTunnel{ID: id, Name: fields[0]})
		}
	}
	return result
}

func validListedTunnels(rows []ListedTunnel) []ListedTunnel {
	result := make([]ListedTunnel, 0, len(rows))
	for _, row := range rows {
		row.ID = strings.TrimSpace(row.ID)
		row.Name = strings.TrimSpace(row.Name)
		if tunnelIDRE.MatchString(row.ID) && row.Name != "" {
			result = append(result, row)
		}
	}
	return result
}

func (a ProcessAccount) Create(ctx context.Context, name string) (ListedTunnel, error) {
	existing, err := a.List(ctx)
	if err == nil {
		for _, item := range existing {
			if item.Name == name {
				return item, nil
			}
		}
	}
	output, createErr := a.run(ctx, "tunnel", "create", name)
	if id := tunnelIDRE.FindString(output); id != "" {
		return ListedTunnel{ID: id, Name: name}, nil
	}
	if createErr != nil && strings.Contains(strings.ToLower(output+createErr.Error()), "already exists") {
		items, listErr := a.List(ctx)
		if listErr == nil {
			for _, item := range items {
				if item.Name == name {
					return item, nil
				}
			}
		}
	}
	if createErr != nil {
		return ListedTunnel{}, createErr
	}
	return ListedTunnel{}, fmt.Errorf("cloudflared did not return a tunnel ID")
}

func (a ProcessAccount) RouteDNS(ctx context.Context, tunnelName, hostname string) error {
	output, err := a.run(ctx, "tunnel", "route", "dns", tunnelName, hostname)
	if err == nil {
		return nil
	}
	message := strings.ToLower(output + " " + err.Error())
	if strings.Contains(message, "already exists") || strings.Contains(message, "duplicate") || strings.Contains(message, "exists as a cname") {
		return nil
	}
	return err
}

type ProvisionResult struct {
	OK          bool   `json:"ok"`
	State       State  `json:"state"`
	Fallback    bool   `json:"fallback"`
	UserMessage string `json:"userMessage,omitempty"`
	Error       string `json:"error,omitempty"`
}

func ProvisionNamed(ctx context.Context, workspaceID, workspaceName, zone, hostname string, account Account) ProvisionResult {
	if account == nil {
		account = ProcessAccount{}
	}
	zone, err := ParseZone(zone)
	if err != nil {
		return fallback(workspaceID, "invalid_zone", err)
	}
	if hostname == "" {
		hostname, err = SuggestedHostname(zone, workspaceName, workspaceID)
	} else {
		hostname, err = NormalizeHostname(hostname)
	}
	if err != nil {
		return fallback(workspaceID, "invalid_hostname", err)
	}
	if !account.HasCert() {
		if err := account.Login(ctx); err != nil {
			return fallback(workspaceID, "login_failed", err)
		}
	}
	tunnelName := "codexlink-" + workspaceID
	created, err := account.Create(ctx, tunnelName)
	if err != nil {
		return fallback(workspaceID, "create_failed", err)
	}
	if err := account.RouteDNS(ctx, created.Name, hostname); err != nil {
		return fallback(workspaceID, "dns_failed", err)
	}
	state, err := WriteState(State{
		WorkspaceID: workspaceID, Preference: PreferenceNamed, AskedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Provider: "cloudflare-named", TunnelName: created.Name, TunnelID: created.ID, Hostname: hostname, Zone: zone,
		ConfiguredAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fallback(workspaceID, "state_failed", err)
	}
	return ProvisionResult{OK: true, State: state}
}

func fallback(workspaceID, reason string, err error) ProvisionResult {
	state, stateErr := ChooseQuick(workspaceID, reason)
	message := "Stable hostname setup did not finish; the workspace was switched to a temporary tunnel."
	if stateErr != nil {
		return ProvisionResult{OK: false, Fallback: true, UserMessage: message, Error: err.Error() + "; state: " + stateErr.Error()}
	}
	return ProvisionResult{OK: true, State: state, Fallback: true, UserMessage: message, Error: err.Error()}
}
