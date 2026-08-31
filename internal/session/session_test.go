package session

import (
	"testing"
)

func stringPointer(value string) *string { return &value }
func modePointer(value Mode) *Mode       { return &value }

func TestProjectSessionNormalizeMergeResolveAndClear(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	normalized, err := NormalizeProjectURL("https://www.chatgpt.com/g/g-p-AbC123/project/?ignored=yes")
	if err != nil || normalized != "https://chatgpt.com/g/g-p-AbC123/project" {
		t.Fatalf("normalized=%q err=%v", normalized, err)
	}
	if _, err := NormalizeProjectURL("https://example.com/g/g-p-x/project"); err == nil {
		t.Fatal("foreign project URL should fail")
	}
	for _, invalid := range []string{
		"http://chatgpt.com/g/g-p-x/project",
		"https://user@chatgpt.com/g/g-p-x/project",
		"https://chatgpt.com/g/g-p-x/project#fragment",
	} {
		if _, err := NormalizeProjectURL(invalid); err == nil {
			t.Fatalf("unsafe project URL should fail: %s", invalid)
		}
	}
	next, err := Merge(nil, Patch{ConversationMode: modePointer(ModeProject), ProjectURL: stringPointer(normalized), URL: stringPointer("https://chatgpt.com/c/123"), ConnectorName: stringPointer("demo")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write("workspace", *next); err != nil {
		t.Fatal(err)
	}
	view := Resolve(next)
	if view.Mode != ModeProject || !view.ProjectReady || view.ReuseSavedChat || view.ChatURL == nil {
		t.Fatalf("unexpected view: %#v", view)
	}
	cleared, kept, err := ClearChat("workspace")
	if err != nil || !cleared || !kept {
		t.Fatalf("clear result: %v %v %v", cleared, kept, err)
	}
	saved, err := Read("workspace")
	if err != nil || saved == nil || saved.URL != "" || saved.ProjectURL != normalized || saved.ConnectorName != "demo" {
		t.Fatalf("saved after clear: %#v err=%v", saved, err)
	}
}

func TestLongChatClearDeletesSession(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	url := "https://chatgpt.com/c/abc"
	mode := ModeLongChat
	next, err := Merge(nil, Patch{URL: &url, ConversationMode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write("ws", *next); err != nil {
		t.Fatal(err)
	}
	cleared, kept, err := ClearChat("ws")
	if err != nil || !cleared || kept {
		t.Fatalf("clear result: %v %v %v", cleared, kept, err)
	}
	if saved, _ := Read("ws"); saved != nil {
		t.Fatalf("session still exists: %#v", saved)
	}
}
