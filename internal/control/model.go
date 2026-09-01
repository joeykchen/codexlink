package control

import "time"

const (
	SchemaVersion = 1
	DefaultTTL    = 2 * time.Hour
	MinTTL        = time.Minute
	MaxTTL        = 4 * time.Hour
	DefaultWait   = 90 * time.Minute
	MaxWait       = 4 * time.Hour
	Retention     = 30 * time.Minute
	MaxActive     = 16
)

type State string

const (
	StatePlan    State = "PLAN"
	StateDone    State = "DONE"
	StateBlocked State = "BLOCKED"
	StateError   State = "ERROR"
)

type PlanStep struct {
	Description string   `json:"description"`
	Files       []string `json:"files,omitempty"`
	Tests       []string `json:"tests,omitempty"`
}

type Submission struct {
	RequestID string     `json:"request_id"`
	TaskID    string     `json:"task_id"`
	Iteration int        `json:"iteration"`
	State     State      `json:"state"`
	Summary   string     `json:"summary"`
	Plan      []PlanStep `json:"plan,omitempty"`
}

type Request struct {
	SchemaVersion int         `json:"schemaVersion"`
	WorkspaceID   string      `json:"workspaceId"`
	RequestID     string      `json:"requestId"`
	TaskID        string      `json:"taskId"`
	Iteration     int         `json:"iteration"`
	Status        string      `json:"status"`
	CreatedAt     time.Time   `json:"createdAt"`
	ExpiresAt     time.Time   `json:"expiresAt"`
	SubmittedAt   *time.Time  `json:"submittedAt,omitempty"`
	Response      *Submission `json:"response,omitempty"`
	Reused        bool        `json:"reused,omitempty"`
}

type Receipt struct {
	Accepted    bool      `json:"accepted"`
	Duplicate   bool      `json:"duplicate"`
	RequestID   string    `json:"requestId"`
	TaskID      string    `json:"taskId"`
	Iteration   int       `json:"iteration"`
	State       State     `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type Error struct{ Code, Message string }

func (e *Error) Error() string        { return e.Message }
func fail(code, message string) error { return &Error{Code: code, Message: message} }
