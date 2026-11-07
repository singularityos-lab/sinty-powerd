package service

import (
	"testing"

	"github.com/singularityos-lab/sinty-powerd/internal/supply"
)

func TestBatteryStateHoldsAndRecovers(t *testing.T) {
	cases := []struct {
		onBattery      bool
		percentage     float64
		low, recovered bool
	}{
		{true, 50, false, true},
		{true, 20, true, false},
		{true, 22, false, false},
		{true, 25, false, true},
		{false, 10, false, true},
		{true, 0, false, false},
	}
	for _, c := range cases {
		low, recovered := batteryState(supply.DefaultPolicy, c.onBattery, c.percentage)
		if low != c.low || recovered != c.recovered {
			t.Errorf("battery %v at %v%%: low %v recovered %v", c.onBattery, c.percentage, low, recovered)
		}
	}
}
