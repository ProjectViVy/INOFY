package inofy

import (
	"testing"
	"time"
)

// TestRunClockFreezesDuringWait (S05 task 2): wait time does not spend
// the active-run clock, and resuming never resets spent budget.
func TestRunClockFreezesDuringWait(t *testing.T) {
	c := newRunClock()
	time.Sleep(10 * time.Millisecond)
	c.Pause()
	frozenAt := c.ActiveMS()
	time.Sleep(30 * time.Millisecond)
	if got := c.ActiveMS(); got != frozenAt {
		t.Fatalf("active clock advanced during wait: %d -> %d", frozenAt, got)
	}
	c.Resume()
	time.Sleep(10 * time.Millisecond)
	after := c.ActiveMS()
	if after <= frozenAt {
		t.Fatalf("active clock lost spent time on resume: %d -> %d", frozenAt, after)
	}
	if after > frozenAt+80 {
		t.Fatalf("wait duration leaked into active clock: %d vs %d", after, frozenAt)
	}
}
