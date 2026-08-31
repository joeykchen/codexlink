package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/config"
	"github.com/joeykchen/codexlink/internal/state"
)

type State struct {
	Service       string `json:"service"`
	Version       string `json:"version"`
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceRoot string `json:"workspaceRoot"`
	PublicRef     string `json:"publicRef"`
	PID           int    `json:"pid"`
	Port          int    `json:"port"`
	AdminToken    string `json:"adminToken"`
	PublicURL     string `json:"publicUrl,omitempty"`
	StartedAt     string `json:"startedAt"`
}

type Health struct {
	Service      string `json:"service"`
	Version      string `json:"version"`
	WorkspaceRef string `json:"workspaceRef"`
	Status       string `json:"status"`
}

func runtimeRepository() state.Repository { return state.New(config.StateDir()) }

func Write(value State) error {
	return runtimeRepository().WriteJSON("runtime", value.WorkspaceID, value)
}

func Read(workspaceID string) (*State, error) {
	var state State
	found, err := runtimeRepository().ReadJSON("runtime", workspaceID, &state)
	if err != nil || !found {
		return nil, err
	}
	return &state, nil
}

func Clear(workspaceID string) error {
	return runtimeRepository().Remove("runtime", workspaceID, ".json")
}

func Probe(ctx context.Context, port int) (*Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/health", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health returned %d", response.StatusCode)
	}
	var health Health
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&health); err != nil {
		return nil, err
	}
	if health.Service != buildinfo.ServiceName || health.Status != "ok" {
		return nil, fmt.Errorf("unexpected service")
	}
	return &health, nil
}

func FindLive(ctx context.Context, workspaceID string) (*State, error) {
	state, err := Read(workspaceID)
	if err != nil || state == nil {
		return nil, err
	}
	health, err := Probe(ctx, state.Port)
	if err == nil && health.WorkspaceRef == state.PublicRef {
		return state, nil
	}
	_ = Clear(workspaceID)
	return nil, nil
}

func AdminRequest(ctx context.Context, state State, method, route string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", state.Port, route), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.AdminToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var message struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(payload, &message)
		if message.Message == "" {
			message.Message = message.Error
		}
		if message.Message == "" {
			message.Message = string(payload)
		}
		return fmt.Errorf("admin request failed (%d): %s", response.StatusCode, message.Message)
	}
	if target != nil && len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, target); err != nil {
			return err
		}
	}
	return nil
}

func InstallSecret() ([]byte, error) {
	path := filepath.Join(config.StateDir(), "install-secret")
	if data, err := os.ReadFile(path); err == nil {
		if decoded, decodeErr := decodeInstallSecret(data); decodeErr == nil {
			return decoded, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := state.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}

	lockPath := path + ".lock"
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		// The creating process publishes the secret atomically. Readers may use
		// it immediately without waiting for the lock owner to exit.
		if data, readErr := os.ReadFile(path); readErr == nil {
			if decoded, decodeErr := decodeInstallSecret(data); decodeErr == nil {
				return decoded, nil
			}
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.RemoveAll(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for install secret initialization")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer os.RemoveAll(lockPath)

	// A competing process may have completed between the first read and lock
	// acquisition. Prefer the already-published value.
	if data, err := os.ReadFile(path); err == nil {
		if decoded, decodeErr := decodeInstallSecret(data); decodeErr == nil {
			return decoded, nil
		}
		// Repair a corrupt file while holding the creation lock. Removing the
		// destination also keeps replacement portable to Windows.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	encoded := []byte(base64.RawURLEncoding.EncodeToString(secret) + "\n")
	if err := state.WriteFileAtomic(path, encoded); err != nil {
		return nil, err
	}
	return secret, nil
}

func decodeInstallSecret(data []byte) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || len(decoded) < 32 {
		return nil, fmt.Errorf("invalid install secret")
	}
	return decoded, nil
}

func PublicRef(workspaceID string) (string, error) {
	secret, err := InstallSecret()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write(secret)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(workspaceID))
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}

func NewAdminToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "cl_admin_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}
