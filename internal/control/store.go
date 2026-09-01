package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joeykchen/codexlink/internal/state"
)

var taskPattern = regexp.MustCompile(`^cl_[0-9a-f]{8}$`)

type Store struct {
	mu            sync.Mutex
	workspaceID   string
	file          string
	workspaceRoot string
	now           func() time.Time
	records       map[string]*Request
}

func NewStore(root, workspaceID, workspaceRoot string) (*Store, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(workspaceID) {
		return nil, fmt.Errorf("workspace ID is required")
	}
	s := &Store{workspaceID: workspaceID, file: filepath.Join(root, "control", workspaceID+".json"), now: time.Now, records: map[string]*Request{}}
	s.workspaceRoot = workspaceRoot
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if err := s.validateLocation(); err != nil {
		return nil, err
	}
	var data []byte
	info, statErr := os.Lstat(s.file)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("control state must be a regular non-symlink file")
		}
		if err := validatePrivateFileMode(info.Mode()); err != nil {
			return nil, err
		}
		if info.Size() > 1<<20 {
			return nil, fail("CONTROL_STATE_TOO_LARGE", "control state exceeds 1 MiB")
		}
		file, err := os.Open(s.file)
		if err != nil {
			return nil, err
		}
		openedInfo, statFileErr := file.Stat()
		if statFileErr != nil {
			file.Close()
			return nil, statFileErr
		}
		if !openedInfo.Mode().IsRegular() || openedInfo.Size() > 1<<20 || !os.SameFile(info, openedInfo) {
			file.Close()
			return nil, fmt.Errorf("control state changed during secure open")
		}
		if err := validatePrivateFileMode(openedInfo.Mode()); err != nil {
			file.Close()
			return nil, err
		}
		data, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if len(data) > 1<<20 {
		return nil, fail("CONTROL_STATE_TOO_LARGE", "control state exceeds 1 MiB")
	}
	if len(data) > 0 {
		var records []*Request
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&records); err != nil {
			return nil, fmt.Errorf("decode control state: %w", err)
		}
		if records == nil {
			return nil, fmt.Errorf("decode control state: expected an array")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("decode control state: trailing JSON")
		}
		if len(records) > MaxActive {
			return nil, fail("CONTROL_CAPACITY_EXCEEDED", "control request capacity reached")
		}
		seenTuple := map[string]bool{}
		for _, record := range records {
			if err := s.validateRecord(record); err != nil {
				return nil, fmt.Errorf("invalid control state: %w", err)
			}
			tuple := fmt.Sprintf("%s/%d", record.TaskID, record.Iteration)
			if s.records[record.RequestID] != nil || seenTuple[tuple] {
				return nil, fmt.Errorf("invalid control state: duplicate request identity")
			}
			seenTuple[tuple] = true
			s.records[record.RequestID] = record
		}
	}
	s.mu.Lock()
	_, pruneErr := s.pruneAndSaveLocked()
	if pruneErr == nil && len(s.records) == 0 && len(data) > 0 {
		pruneErr = s.saveLocked()
	}
	s.mu.Unlock()
	return s, pruneErr
}

func (s *Store) Prepare(taskID string, iteration int, ttl time.Duration) (*Request, error) {
	if err := validateIdentity(taskID, iteration); err != nil {
		return nil, err
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return nil, fail("INVALID_ARGUMENT", "ttl must be between 1 minute and 4 hours")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.pruneAndSaveLocked(); err != nil {
		return nil, err
	}
	for _, record := range s.records {
		if record.TaskID == taskID && record.Iteration == iteration {
			copy := cloneRequest(record)
			copy.Reused = true
			return copy, nil
		}
	}
	if len(s.records) >= MaxActive {
		return nil, fail("CONTROL_CAPACITY_EXCEEDED", "control request capacity reached")
	}
	id, err := requestID()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	record := &Request{SchemaVersion: SchemaVersion, WorkspaceID: s.workspaceID, RequestID: id, TaskID: taskID, Iteration: iteration, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(ttl)}
	s.records[id] = record
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		return nil, err
	}
	return cloneRequest(record), nil
}

func (s *Store) Submit(in Submission) (*Receipt, error) {
	if err := validateSubmission(in); err != nil {
		return nil, err
	}
	canonical, _ := json.Marshal(in)
	if len(canonical) > 64<<10 {
		return nil, fail("INVALID_ARGUMENT", "control response exceeds 64 KiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.records[in.RequestID]; record != nil && !s.now().Before(record.ExpiresAt) {
		delete(s.records, in.RequestID)
		if err := s.saveLocked(); err != nil {
			s.records[in.RequestID] = record
			return nil, err
		}
		return nil, fail("CONTROL_REQUEST_EXPIRED", "control request expired")
	}
	if _, err := s.pruneAndSaveLocked(); err != nil {
		return nil, err
	}
	record := s.records[in.RequestID]
	if record == nil {
		return nil, fail("CONTROL_REQUEST_NOT_FOUND", "control request was not found")
	}
	if record.TaskID != in.TaskID || record.Iteration != in.Iteration {
		return nil, fail("CONTROL_REQUEST_MISMATCH", "control request identity does not match")
	}
	if record.Status == "submitted" {
		existing, _ := json.Marshal(record.Response)
		if sha256.Sum256(existing) != sha256.Sum256(canonical) {
			return nil, fail("CONTROL_RESPONSE_CONFLICT", "control response is immutable")
		}
		return receipt(record, true), nil
	}
	if record.Status != "pending" || !s.now().Before(record.ExpiresAt) {
		return nil, fail("CONTROL_REQUEST_EXPIRED", "control request is no longer active")
	}
	now := s.now().UTC()
	before := cloneRequest(record)
	record.Status = "submitted"
	record.SubmittedAt = &now
	responseCopy := cloneSubmission(in)
	record.Response = &responseCopy
	record.ExpiresAt = now.Add(Retention)
	if err := s.saveLocked(); err != nil {
		s.records[in.RequestID] = before
		return nil, err
	}
	return receipt(record, false), nil
}

func (s *Store) Get(requestID, taskID string, iteration int) (*Request, error) {
	if err := validateIdentity(taskID, iteration); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.records[requestID]; record != nil && !s.now().Before(record.ExpiresAt) {
		delete(s.records, requestID)
		if err := s.saveLocked(); err != nil {
			s.records[requestID] = record
			return nil, err
		}
		return nil, fail("CONTROL_REQUEST_EXPIRED", "control request expired")
	}
	if _, err := s.pruneAndSaveLocked(); err != nil {
		return nil, err
	}
	r := s.records[requestID]
	if r == nil {
		return nil, fail("CONTROL_REQUEST_NOT_FOUND", "control request was not found")
	}
	if r.TaskID != taskID || r.Iteration != iteration {
		return nil, fail("CONTROL_REQUEST_MISMATCH", "control request identity does not match")
	}
	return cloneRequest(r), nil
}

func (s *Store) Cancel(requestID, taskID string, iteration int) (*Request, bool, error) {
	r, err := s.Get(requestID, taskID, iteration)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.records[requestID]
	if current == nil {
		return nil, false, fail("CONTROL_REQUEST_NOT_FOUND", "control request was not found")
	}
	if current.Status != "pending" {
		return cloneRequest(current), false, nil
	}
	delete(s.records, requestID)
	if err := s.saveLocked(); err != nil {
		s.records[requestID] = current
		return nil, false, err
	}
	return r, true, nil
}

func validateIdentity(taskID string, iteration int) error {
	if !taskPattern.MatchString(taskID) {
		return fail("INVALID_ARGUMENT", "task_id must match cl_<8 lowercase hex characters>")
	}
	if iteration < 0 || iteration > 1_000_000 {
		return fail("INVALID_ARGUMENT", "iteration must be between 0 and 1000000")
	}
	return nil
}

func validateSubmission(in Submission) error {
	if err := validateIdentity(in.TaskID, in.Iteration); err != nil {
		return err
	}
	if !regexp.MustCompile(`^cr_[A-Za-z0-9_-]{32}$`).MatchString(in.RequestID) {
		return fail("INVALID_ARGUMENT", "request_id has invalid format")
	}
	if strings.TrimSpace(in.Summary) == "" || len(in.Summary) > 4096 || hasControl(in.Summary) {
		return fail("INVALID_ARGUMENT", "summary must contain 1 to 4096 bytes")
	}
	if in.State != StatePlan && in.State != StateDone && in.State != StateBlocked && in.State != StateError {
		return fail("INVALID_ARGUMENT", "state must be PLAN, DONE, BLOCKED, or ERROR")
	}
	if in.State == StatePlan && (len(in.Plan) < 1 || len(in.Plan) > 16) {
		return fail("INVALID_ARGUMENT", "PLAN requires 1 to 16 steps")
	}
	if in.State != StatePlan && len(in.Plan) != 0 {
		return fail("INVALID_ARGUMENT", "plan is allowed only for PLAN")
	}
	for _, step := range in.Plan {
		if strings.TrimSpace(step.Description) == "" || len(step.Description) > 2048 || hasControl(step.Description) || len(step.Files) > 32 || len(step.Tests) > 16 {
			return fail("INVALID_ARGUMENT", "plan step exceeds a configured limit")
		}
		for _, path := range step.Files {
			if len(path) > 512 || path == "" || strings.Contains(path, "\\") || pathpkg.Clean(path) != path || path == "." || strings.HasPrefix(path, "/") || regexp.MustCompile(`^[A-Za-z]:`).MatchString(path) || path == ".." || strings.HasPrefix(path, "../") || hasControl(path) {
				return fail("INVALID_ARGUMENT", "plan file must be a normalized workspace-relative path")
			}
		}
		for _, test := range step.Tests {
			if len(test) == 0 || len(test) > 1024 || hasControl(test) {
				return fail("INVALID_ARGUMENT", "test label exceeds a configured limit")
			}
		}
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
func requestID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cr_" + base64.RawURLEncoding.EncodeToString(b), nil
}
func receipt(r *Request, duplicate bool) *Receipt {
	return &Receipt{Accepted: true, Duplicate: duplicate, RequestID: r.RequestID, TaskID: r.TaskID, Iteration: r.Iteration, State: r.Response.State, SubmittedAt: *r.SubmittedAt, ExpiresAt: r.ExpiresAt}
}
func (s *Store) pruneLocked() bool {
	now := s.now()
	changed := false
	for id, r := range s.records {
		if !now.Before(r.ExpiresAt) {
			delete(s.records, id)
			changed = true
		}
	}
	return changed
}
func (s *Store) pruneAndSaveLocked() (bool, error) {
	before := cloneRecords(s.records)
	changed := s.pruneLocked()
	if !changed {
		return false, nil
	}
	if err := s.saveLocked(); err != nil {
		s.records = before
		return false, err
	}
	return true, nil
}
func (s *Store) saveLocked() error {
	if err := s.validateLocation(); err != nil {
		return err
	}
	if info, err := os.Lstat(s.file); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("control state must be a regular non-symlink file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(s.records) == 0 {
		if err := os.Remove(s.file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]*Request, 0, len(ids))
	for _, id := range ids {
		records = append(records, s.records[id])
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fail("CONTROL_STATE_TOO_LARGE", "control state exceeds 1 MiB")
	}
	return state.WriteFileAtomic(s.file, append(data, '\n'))
}

func cloneRequest(r *Request) *Request {
	if r == nil {
		return nil
	}
	data, _ := json.Marshal(r)
	var out Request
	_ = json.Unmarshal(data, &out)
	return &out
}
func cloneRecords(in map[string]*Request) map[string]*Request {
	out := make(map[string]*Request, len(in))
	for id, r := range in {
		out[id] = cloneRequest(r)
	}
	return out
}
func cloneSubmission(in Submission) Submission {
	data, _ := json.Marshal(in)
	var out Submission
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *Store) validateRecord(r *Request) error {
	if r == nil || r.SchemaVersion != SchemaVersion || r.WorkspaceID != s.workspaceID || !regexp.MustCompile(`^cr_[A-Za-z0-9_-]{32}$`).MatchString(r.RequestID) || r.Reused || r.CreatedAt.IsZero() || r.ExpiresAt.IsZero() || !r.ExpiresAt.After(r.CreatedAt) {
		return fail("INVALID_ARGUMENT", "invalid record metadata")
	}
	if err := validateIdentity(r.TaskID, r.Iteration); err != nil {
		return err
	}
	switch r.Status {
	case "pending":
		if r.Response != nil || r.SubmittedAt != nil {
			return fail("INVALID_ARGUMENT", "pending record contains response")
		}
		lifetime := r.ExpiresAt.Sub(r.CreatedAt)
		if lifetime < MinTTL || lifetime > MaxTTL {
			return fail("INVALID_ARGUMENT", "pending lifetime exceeds limit")
		}
	case "submitted":
		if r.Response == nil || r.SubmittedAt == nil {
			return fail("INVALID_ARGUMENT", "submitted record lacks response")
		}
		if r.Response.RequestID != r.RequestID || r.Response.TaskID != r.TaskID || r.Response.Iteration != r.Iteration {
			return fail("INVALID_ARGUMENT", "response identity mismatch")
		}
		if err := validateSubmission(*r.Response); err != nil {
			return err
		}
		retention := r.ExpiresAt.Sub(*r.SubmittedAt)
		if r.SubmittedAt.Before(r.CreatedAt) || !r.SubmittedAt.Before(r.ExpiresAt) || retention <= 0 || retention > Retention {
			return fail("INVALID_ARGUMENT", "submitted timestamps are invalid")
		}
		encoded, _ := json.Marshal(r.Response)
		if len(encoded) > 64<<10 {
			return fail("INVALID_ARGUMENT", "stored response exceeds 64 KiB")
		}
	default:
		return fail("INVALID_ARGUMENT", "invalid record status")
	}
	return nil
}
func (s *Store) validateLocation() error {
	stateParent, err := canonicalExisting(filepath.Dir(s.file))
	if err != nil {
		return err
	}
	workspaceRoot, err := canonicalExisting(s.workspaceRoot)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(workspaceRoot, stateParent)
	if err == nil && (rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")) {
		return fmt.Errorf("control state directory must be outside the workspace")
	}
	workspaceInfo, err := os.Stat(workspaceRoot)
	if err != nil {
		return err
	}
	for current := stateParent; ; current = filepath.Dir(current) {
		if info, statErr := os.Stat(current); statErr == nil && os.SameFile(info, workspaceInfo) {
			return fmt.Errorf("control state directory must be outside the workspace")
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	controlDir := filepath.Dir(s.file)
	if info, err := os.Lstat(controlDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("control state directory must be a private directory")
		}
		if err := validatePrivateDirMode(info.Mode()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func canonicalExisting(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	current := abs
	tail := []string{}
	for {
		resolved, evalErr := filepath.EvalSymlinks(current)
		if evalErr == nil {
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", evalErr
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
}
