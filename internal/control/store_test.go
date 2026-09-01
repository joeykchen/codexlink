package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestStorePrepareSubmitReplayAndPersistence(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	store, err := NewStore(root, "workspace123", workspace)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Prepare("cl_deadbeef", 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.RequestID[:3] != "cr_" || len(r.RequestID) != 35 {
		t.Fatalf("request id = %q", r.RequestID)
	}
	reused, err := store.Prepare("cl_deadbeef", 2, time.Minute)
	if err != nil || !reused.Reused || reused.RequestID != r.RequestID {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
	in := Submission{RequestID: r.RequestID, TaskID: r.TaskID, Iteration: r.Iteration, State: StatePlan, Summary: "plan", Plan: []PlanStep{{Description: "change", Files: []string{"internal/a.go"}, Tests: []string{"go test ./internal/a"}}}}
	first, err := store.Submit(in)
	if err != nil || first.Duplicate {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.Submit(in)
	if err != nil || !second.Duplicate {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	conflict := in
	conflict.Summary = "different"
	if _, err := store.Submit(conflict); err == nil || err.(*Error).Code != "CONTROL_RESPONSE_CONFLICT" {
		t.Fatalf("conflict=%v", err)
	}
	reloaded, err := NewStore(root, "workspace123", workspace)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get(r.RequestID, r.TaskID, r.Iteration)
	if err != nil || got.Response.Summary != "plan" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if mode := fileMode(t, filepath.Join(root, "control", "workspace123.json")); validatePrivateFileMode(mode) != nil {
		t.Fatalf("private mode validation failed for %o", mode.Perm())
	}
}

func TestControlDurationPolicy(t *testing.T) {
	if DefaultTTL != 2*time.Hour || DefaultWait != 90*time.Minute || MaxTTL != 4*time.Hour || MaxWait != 4*time.Hour || Retention != 30*time.Minute {
		t.Fatalf("unexpected duration policy: ttl=%v wait=%v maxTTL=%v maxWait=%v retention=%v", DefaultTTL, DefaultWait, MaxTTL, MaxWait, Retention)
	}
	store, err := NewStore(t.TempDir(), "w", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.Prepare("cl_deadbeef", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if lifetime := request.ExpiresAt.Sub(request.CreatedAt); lifetime != DefaultTTL {
		t.Fatalf("default lifetime = %v", lifetime)
	}
	if _, err := store.Prepare("cl_feedface", 0, MaxTTL+time.Second); err == nil {
		t.Fatal("request above maximum TTL accepted")
	} else if controlErr, ok := err.(*Error); !ok || controlErr.Code != "INVALID_ARGUMENT" || controlErr.Message != "ttl must be between 1 minute and 4 hours" {
		t.Fatalf("unexpected maximum TTL error: %#v", err)
	}
}

func TestStoreFirstWriteWins(t *testing.T) {
	store, _ := NewStore(t.TempDir(), "w", t.TempDir())
	r, _ := store.Prepare("cl_0123abcd", 0, time.Minute)
	in := Submission{RequestID: r.RequestID, TaskID: r.TaskID, State: StateDone, Summary: "done"}
	var wg sync.WaitGroup
	success := 0
	conflicts := 0
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := in
			if i > 0 {
				v.Summary = "other"
			}
			_, err := store.Submit(v)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if err.(*Error).Code == "CONTROL_RESPONSE_CONFLICT" {
				conflicts++
			}
		}(i)
	}
	wg.Wait()
	if success < 1 || conflicts < 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestSubmissionValidation(t *testing.T) {
	base := Submission{RequestID: "cr_12345678901234567890123456789012", TaskID: "cl_deadbeef", State: StateDone, Summary: "ok"}
	cases := []Submission{{}, func() Submission { x := base; x.TaskID = "bad"; return x }(), func() Submission { x := base; x.Plan = []PlanStep{{Description: "no"}}; return x }(), func() Submission { x := base; x.State = StatePlan; return x }(), func() Submission {
		x := base
		x.State = StatePlan
		x.Plan = []PlanStep{{Description: "x", Files: []string{"../secret"}}}
		return x
	}()}
	for i, c := range cases {
		if err := validateSubmission(c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestStoreExpiryReturnsStableErrorAndPrunesFile(t *testing.T) {
	root := t.TempDir()
	store, _ := NewStore(root, "w", t.TempDir())
	now := time.Now()
	store.now = func() time.Time { return now }
	request, _ := store.Prepare("cl_deadbeef", 0, time.Minute)
	now = now.Add(2 * time.Minute)
	_, err := store.Get(request.RequestID, request.TaskID, 0)
	if err == nil || err.(*Error).Code != "CONTROL_REQUEST_EXPIRED" {
		t.Fatalf("expiry error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "control", "w.json")); !os.IsNotExist(err) {
		t.Fatalf("expired state file remains: %v", err)
	}
}

func TestStoreRejectsStateInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(workspace, ".state")
	if _, err := NewStore(stateRoot, "w", workspace); err == nil {
		t.Fatal("state inside workspace accepted")
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "control")); !os.IsNotExist(err) {
		t.Fatalf("state directory created: %v", err)
	}
}

func TestPrepareReusesSubmittedTuple(t *testing.T) {
	store, _ := NewStore(t.TempDir(), "w", t.TempDir())
	request, _ := store.Prepare("cl_deadbeef", 0, time.Minute)
	_, err := store.Submit(Submission{RequestID: request.RequestID, TaskID: request.TaskID, State: StateDone, Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Prepare(request.TaskID, 0, time.Minute)
	if err != nil || again.RequestID != request.RequestID || !again.Reused || again.Status != "submitted" {
		t.Fatalf("again=%+v err=%v", again, err)
	}
}

func TestStoreRejectsUnsafeOrMalformedState(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{{"symlink", func(file string) error {
		target := filepath.Join(filepath.Dir(file), "target")
		if err := os.WriteFile(target, []byte("[]"), 0600); err != nil {
			return err
		}
		return os.Symlink(target, file)
	}}, {"permissions", func(file string) error { return os.WriteFile(file, []byte("[]"), 0644) }}, {"unknown field", func(file string) error { return os.WriteFile(file, []byte(`[{"unknown":true}]`), 0600) }}, {"null state", func(file string) error { return os.WriteFile(file, []byte(`null`), 0600) }}} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "permissions" && runtime.GOOS == "windows" {
				t.Skip("Windows has no authoritative POSIX mode bits")
			}
			root := t.TempDir()
			dir := filepath.Join(root, "control")
			os.MkdirAll(dir, 0700)
			if err := test.setup(filepath.Join(dir, "w.json")); err != nil {
				if test.name == "symlink" {
					t.Skipf("symlink unavailable: %v", err)
				}
				t.Fatal(err)
			}
			if _, err := NewStore(root, "w", t.TempDir()); err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
}

func TestStoreRejectsPersistedPendingLifetimeBelowMinimum(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	store, err := NewStore(root, "w", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare("cl_deadbeef", 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "control", "w.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var records []*Request
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	records[0].ExpiresAt = records[0].CreatedAt.Add(30 * time.Second)
	stateData, _ := json.Marshal(records)
	if err := os.WriteFile(file, stateData, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root, "w", workspace); err == nil {
		t.Fatal("short persisted lifetime accepted")
	}
}

func TestStoreRemovesValidEmptyState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "control")
	os.MkdirAll(dir, 0700)
	file := filepath.Join(dir, "w.json")
	os.WriteFile(file, []byte("[]\n"), 0600)
	if _, err := NewStore(root, "w", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("empty state remains: %v", err)
	}
}
func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
