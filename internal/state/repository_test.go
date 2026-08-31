package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRepositoryJSONAndPermissions(t *testing.T) {
	repo := New(t.TempDir())
	input := map[string]any{"name": "bridge", "count": float64(2)}
	if err := repo.WriteJSON("runtime", "workspace", input); err != nil {
		t.Fatal(err)
	}
	path, err := repo.Path("runtime", "workspace", ".json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	var output map[string]any
	found, err := repo.ReadJSON("runtime", "workspace", &output)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if output["name"] != "bridge" || output["count"] != float64(2) {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestRepositoryRejectsUnsafeComponents(t *testing.T) {
	repo := New(t.TempDir())
	bad := []string{"", ".", "..", "a/b", `a\\b`, "a\x00b"}
	for _, value := range bad {
		if _, err := repo.Path(value, "key", ".json"); err == nil {
			t.Errorf("bucket %q should fail", value)
		}
		if _, err := repo.Path("bucket", value, ".json"); err == nil {
			t.Errorf("key %q should fail", value)
		}
	}
	if _, err := repo.Path("bucket", "key", "json"); err == nil {
		t.Fatal("extension without leading dot should fail")
	}
}

func TestAppendAndScanJSONLines(t *testing.T) {
	repo := New(t.TempDir())
	for i := 0; i < 3; i++ {
		if err := repo.AppendJSONLine("executions", "ws", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	var values []int
	if err := repo.ScanJSONLines("executions", "ws", func(line []byte) error {
		var record map[string]int
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		values = append(values, record["i"])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || values[0] != 0 || values[2] != 2 {
		t.Fatalf("values = %v", values)
	}
	path, _ := repo.Path("executions", "ws", ".jsonl")
	if filepath.Ext(path) != ".jsonl" {
		t.Fatalf("unexpected path: %s", path)
	}
}
