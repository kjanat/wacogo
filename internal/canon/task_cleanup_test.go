package canon

import (
	"errors"
	"strings"
	"testing"
)

func TestTaskEndDrainsAfterReleasePanic(t *testing.T) {
	primary := errors.New("release failed")
	var task Task
	var calls []int
	task.AddRelease(func() { calls = append(calls, 1); panic(primary) })
	task.AddRelease(func() { calls = append(calls, 2) })
	task.BorrowIssued()
	err := task.End()
	if !errors.Is(err, primary) || !strings.Contains(err.Error(), "outstanding borrows") {
		t.Fatalf("missing cleanup failure: %v", err)
	}
	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("release order: %v", calls)
	}
	task.BorrowDropped()
	if err := task.End(); err != nil || len(calls) != 2 {
		t.Fatalf("second End: error=%v calls=%v", err, calls)
	}
}
