package service

import (
	"reflect"
	"testing"

	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
)

func TestReasonsExplainWhatHoldsTheComputerBack(t *testing.T) {
	s := &Service{chosen: profiles.Balanced, batteryAware: true}
	if got := s.reasons(); len(got) != 0 {
		t.Fatalf("reasons on mains: %v", got)
	}
	s.onBattery = true
	if got := s.reasons(); !reflect.DeepEqual(got, [][]string{{"battery", "", ""}}) {
		t.Fatalf("on battery: %v", got)
	}
	s.holds = append(s.holds, hold{profile: profiles.PowerSaver, app: "sinty-powerd", reason: lowBatteryReason})
	want := [][]string{{"hold-power-saver", "sinty-powerd", lowBatteryReason}}
	if got := s.reasons(); !reflect.DeepEqual(got, want) {
		t.Fatalf("low battery: %v", got)
	}
	s.batteryAware = false
	s.holds = nil
	if got := s.reasons(); len(got) != 0 {
		t.Fatalf("battery awareness off: %v", got)
	}
}
