package execution

import (
	"fmt"
	"testing"
)

func TestExecutionRecordsReturnLatestBoundedHistory(t *testing.T) {
	t.Setenv("CODEXLINK_STATE_DIR", t.TempDir())
	for i := 1; i <= 5; i++ {
		tests := fmt.Sprintf("%d passed", i)
		if err := Append("workspace", Record{TaskID: "task", Iteration: i, ChangedFiles: i, Tests: &tests, ExitStatus: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := Read("workspace", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Iteration != 3 || records[2].Iteration != 5 {
		t.Fatalf("records = %#v", records)
	}
	latest, err := Latest("workspace")
	if err != nil || latest == nil || latest.Iteration != 5 {
		t.Fatalf("latest = %#v err=%v", latest, err)
	}
}
