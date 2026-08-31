package execution

import (
	"encoding/json"
	"sync"

	"github.com/joeykchen/codexlink/internal/config"
	"github.com/joeykchen/codexlink/internal/state"
)

type Record struct {
	TaskID       string  `json:"taskId"`
	Iteration    int     `json:"iteration"`
	ChangedFiles any     `json:"changedFiles"`
	Tests        *string `json:"tests"`
	ExitStatus   string  `json:"exitStatus"`
	Timestamp    string  `json:"timestamp"`
	Notes        string  `json:"notes,omitempty"`
}

var appendMu sync.Mutex

func recordRepository() state.Repository { return state.New(config.StateDir()) }

func Append(workspaceID string, record Record) error {
	appendMu.Lock()
	defer appendMu.Unlock()
	return recordRepository().AppendJSONLine("executions", workspaceID, record)
}

func Read(workspaceID string, limit int) ([]Record, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	ring := make([]Record, limit)
	count := 0
	err := recordRepository().ScanJSONLines("executions", workspaceID, func(line []byte) error {
		var record Record
		if json.Unmarshal(line, &record) != nil {
			return nil
		}
		ring[count%limit] = record
		count++
		return nil
	})
	if err != nil {
		return nil, err
	}
	size := count
	if size > limit {
		size = limit
	}
	result := make([]Record, 0, size)
	start := count - size
	for i := 0; i < size; i++ {
		result = append(result, ring[(start+i)%limit])
	}
	return result, nil
}

func Latest(workspaceID string) (*Record, error) {
	records, err := Read(workspaceID, 1)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return &records[0], nil
}
