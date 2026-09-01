package bridge

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/control"
)

type controlIdentity struct {
	RequestID string `json:"requestId"`
	TaskID    string `json:"taskId"`
	Iteration int    `json:"iteration"`
}

func (s *Server) registerControlAdmin(mux *http.ServeMux) {
	mux.Handle("/admin/control/prepare", s.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			adminMethod(w, http.MethodPost)
			return
		}
		audience := s.baseURL(r) + "/mcp"
		if !s.AuthStore.HasActiveScopeForAudience(audience, auth.ScopeControlRespond) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "control_scope_required", "message": "authorize control.respond before preparing a response"})
			return
		}
		var in struct {
			TaskID     string `json:"taskId"`
			Iteration  int    `json:"iteration"`
			TTLSeconds int    `json:"ttlSeconds,omitempty"`
		}
		if !decodeControlBody(w, r, &in) {
			return
		}
		if ttl := time.Duration(in.TTLSeconds) * time.Second; in.TTLSeconds != 0 && (ttl < control.MinTTL || ttl > control.MaxTTL) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "ttlSeconds must be between 60 and 14400"})
			return
		}
		record, err := s.ControlStore.Prepare(in.TaskID, in.Iteration, time.Duration(in.TTLSeconds)*time.Second)
		if err != nil {
			writeControlError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, record)
	})))
	mux.Handle("/admin/control/wait", s.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			adminMethod(w, http.MethodPost)
			return
		}
		var in struct {
			controlIdentity
			WaitMS int `json:"waitMs,omitempty"`
		}
		if !decodeControlBody(w, r, &in) {
			return
		}
		if in.WaitMS < 0 || in.WaitMS > 25000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "waitMs must be between 0 and 25000"})
			return
		}
		deadline := time.Now().Add(time.Duration(in.WaitMS) * time.Millisecond)
		for {
			record, err := s.ControlStore.Get(in.RequestID, in.TaskID, in.Iteration)
			if err != nil {
				writeControlError(w, err)
				return
			}
			if record.Status == "submitted" || !time.Now().Before(deadline) {
				writeJSON(w, http.StatusOK, record)
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	})))
	mux.Handle("/admin/control/cancel", s.adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			adminMethod(w, http.MethodPost)
			return
		}
		var in controlIdentity
		if !decodeControlBody(w, r, &in) {
			return
		}
		record, cancelled, err := s.ControlStore.Cancel(in.RequestID, in.TaskID, in.Iteration)
		if err != nil {
			writeControlError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": cancelled, "request": record})
	})))
}

func decodeControlBody(w http.ResponseWriter, r *http.Request, target any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": "request body must be valid JSON"})
		return false
	}
	if err := ensureSingleJSONValue(d); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": err.Error()})
		return false
	}
	return true
}
func writeControlError(w http.ResponseWriter, err error) {
	if ce, ok := err.(*control.Error); ok {
		status := http.StatusBadRequest
		if ce.Code == "CONTROL_REQUEST_NOT_FOUND" {
			status = http.StatusNotFound
		}
		if ce.Code == "CONTROL_RESPONSE_CONFLICT" {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": ce.Code, "message": ce.Message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "control_failed", "message": "control operation failed"})
}
