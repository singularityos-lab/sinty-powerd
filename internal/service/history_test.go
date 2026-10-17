package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-powerd/internal/supply"
)

func TestHistoryKeepsChangesNewestFirst(t *testing.T) {
	h := newHistory()
	start := time.Unix(1_000_000, 0)
	for i := 0; i < 10; i++ {
		h.record(&supply.Device{Percentage: float64(90 - i), State: supply.StateDischarging}, start.Add(time.Duration(i)*time.Minute))
		h.record(&supply.Device{Percentage: float64(90 - i), State: supply.StateDischarging}, start.Add(time.Duration(i)*time.Minute+time.Second))
	}
	now := start.Add(10 * time.Minute)
	all := h.get("charge", 0, 0, now)
	if len(all) != 10 || all[0].Value != 81 || all[9].Value != 90 {
		t.Fatalf("history %+v", all)
	}
	if recent := h.get("charge", 180, 0, now); len(recent) != 3 {
		t.Errorf("last 3 minutes: %d samples", len(recent))
	}
	if thin := h.get("charge", 0, 4, now); len(thin) != 4 {
		t.Errorf("thinned to %d samples", len(thin))
	}
	path := filepath.Join(t.TempDir(), "h.json")
	if err := h.save(path); err != nil {
		t.Fatal(err)
	}
	if got := loadHistory(path).get("charge", 0, 0, now); len(got) != 10 {
		t.Errorf("reloaded %d samples", len(got))
	}
}
