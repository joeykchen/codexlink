package session

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/joeykchen/codexlink/internal/config"
	"github.com/joeykchen/codexlink/internal/state"
)

type Mode string

const (
	ModeLongChat Mode = "long-chat"
	ModeProject  Mode = "project"
)

type Saved struct {
	URL              string `json:"url,omitempty"`
	Title            string `json:"title,omitempty"`
	TaskID           string `json:"taskId,omitempty"`
	Iteration        int    `json:"iteration,omitempty"`
	LastState        string `json:"lastState,omitempty"`
	SavedAt          string `json:"savedAt"`
	ConversationMode Mode   `json:"conversationMode,omitempty"`
	ProjectURL       string `json:"projectUrl,omitempty"`
	ConnectorName    string `json:"connectorName,omitempty"`
}

type Patch struct {
	URL              *string
	Title            *string
	TaskID           *string
	Iteration        *int
	LastState        *string
	ConversationMode *Mode
	ProjectURL       *string
	ConnectorName    *string
}

type View struct {
	Mode           Mode    `json:"mode"`
	Reason         string  `json:"reason"`
	ProjectURL     *string `json:"projectUrl"`
	ProjectReady   bool    `json:"projectReady"`
	ChatURL        *string `json:"chatUrl"`
	ConnectorName  *string `json:"connectorName"`
	ReuseSavedChat bool    `json:"reuseSavedChat"`
}

func sessionRepository() state.Repository { return state.New(config.StateDir()) }

func Read(workspaceID string) (*Saved, error) {
	var saved Saved
	found, err := sessionRepository().ReadJSON("sessions", workspaceID, &saved)
	if err != nil || !found {
		return nil, err
	}
	return &saved, nil
}

func Write(workspaceID string, saved Saved) (*Saved, error) {
	saved.SavedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := sessionRepository().WriteJSON("sessions", workspaceID, saved); err != nil {
		return nil, err
	}
	return &saved, nil
}

var projectPath = regexp.MustCompile(`^/g/(g-p-[A-Za-z0-9]+)/project/?$`)

func NormalizeProjectURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" ||
		(parsed.Hostname() != "chatgpt.com" && parsed.Hostname() != "www.chatgpt.com") {
		return "", fmt.Errorf("project URL must be a chatgpt.com Project URL")
	}
	match := projectPath.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return "", fmt.Errorf("project URL must look like https://chatgpt.com/g/g-p-…/project")
	}
	return "https://chatgpt.com/g/" + match[1] + "/project", nil
}

func Resolve(saved *Saved) View {
	if saved == nil {
		return View{Mode: ModeProject, Reason: "new-workspace"}
	}
	var projectURL *string
	if saved.ProjectURL != "" {
		if normalized, err := NormalizeProjectURL(saved.ProjectURL); err == nil {
			projectURL = &normalized
		}
	}
	var chatURL, connector *string
	if saved.URL != "" {
		value := saved.URL
		chatURL = &value
	}
	if saved.ConnectorName != "" {
		value := saved.ConnectorName
		connector = &value
	}
	if saved.ConversationMode == ModeProject || projectURL != nil {
		return View{Mode: ModeProject, Reason: "project", ProjectURL: projectURL, ProjectReady: projectURL != nil, ChatURL: chatURL, ConnectorName: connector, ReuseSavedChat: false}
	}
	return View{Mode: ModeLongChat, Reason: "existing-long-chat", ChatURL: chatURL, ConnectorName: connector, ReuseSavedChat: chatURL != nil}
}

func Merge(previous *Saved, patch Patch) (*Saved, error) {
	next := Saved{}
	if previous != nil {
		next = *previous
	}
	if patch.URL != nil {
		next.URL = *patch.URL
	}
	if patch.Title != nil {
		next.Title = *patch.Title
	}
	if patch.TaskID != nil {
		next.TaskID = *patch.TaskID
	}
	if patch.Iteration != nil {
		next.Iteration = *patch.Iteration
	}
	if patch.LastState != nil {
		next.LastState = *patch.LastState
	}
	if patch.ConversationMode != nil {
		next.ConversationMode = *patch.ConversationMode
	}
	if patch.ConnectorName != nil {
		next.ConnectorName = *patch.ConnectorName
	}
	if patch.ProjectURL != nil {
		normalized, err := NormalizeProjectURL(*patch.ProjectURL)
		if err != nil {
			return nil, err
		}
		next.ProjectURL = normalized
	}
	if next.ConversationMode == ModeProject && next.ProjectURL == "" {
		return nil, fmt.Errorf("project mode requires --project-url")
	}
	if next.URL == "" && next.ProjectURL == "" && next.TaskID == "" && next.ConversationMode == "" {
		return nil, fmt.Errorf("nothing to save")
	}
	next.SavedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return &next, nil
}

func ClearChat(workspaceID string) (cleared, keptProject bool, err error) {
	saved, err := Read(workspaceID)
	if err != nil {
		return false, false, err
	}
	if saved == nil {
		return false, false, nil
	}
	view := Resolve(saved)
	if view.Mode == ModeProject && view.ProjectURL != nil {
		_, err = Write(workspaceID, Saved{ConversationMode: ModeProject, ProjectURL: *view.ProjectURL, ConnectorName: saved.ConnectorName})
		return true, true, err
	}
	err = sessionRepository().Remove("sessions", workspaceID, ".json")
	return true, false, err
}
