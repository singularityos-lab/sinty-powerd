package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		in   State
		want Settings
	}{
		{"balanced on mains", State{Profile: profiles.Balanced, BatteryAware: true, Form: Laptop},
			Settings{Platform: profiles.Balanced, Bias: BiasBalancePerformance, Boost: true}},
		{"balanced on battery", State{Profile: profiles.Balanced, OnBattery: true, BatteryAware: true, Form: Laptop},
			Settings{Platform: profiles.Balanced, Bias: BiasBalancePower, Boost: true, RuntimePM: true, AudioSaving: true}},
		{"balanced on battery, not battery aware", State{Profile: profiles.Balanced, OnBattery: true, Form: Laptop},
			Settings{Platform: profiles.Balanced, Bias: BiasBalancePerformance, Boost: true, RuntimePM: true, AudioSaving: true}},
		{"power saver", State{Profile: profiles.PowerSaver, Form: Laptop},
			Settings{Platform: profiles.PowerSaver, Bias: BiasPower, RuntimePM: true, AudioSaving: true}},
		{"performance stays fast while idle", State{Profile: profiles.Performance, Idle: true, Form: Laptop},
			Settings{Platform: profiles.Performance, Bias: BiasPerformance, Boost: true}},
		{"idle session on mains", State{Profile: profiles.Balanced, Idle: true, Form: Laptop},
			Settings{Platform: profiles.Balanced, Bias: BiasPower, Boost: true, RuntimePM: true, AudioSaving: true}},
		{"headless", State{Profile: profiles.Balanced, Form: Headless},
			Settings{Platform: profiles.Balanced, Bias: BiasPower, Boost: true, RuntimePM: true, AudioSaving: true, PackageWatts: 4}},
		{"phone", State{Profile: profiles.Balanced, OnBattery: true, BatteryAware: true, Form: Phone},
			Settings{Platform: profiles.Balanced, Bias: BiasBalancePower, Boost: true, RuntimePM: true, AudioSaving: true, PackageWatts: 6}},
		{"tv on mains", State{Profile: profiles.Balanced, Form: TV},
			Settings{Platform: profiles.Balanced, Bias: BiasBalancePerformance, Boost: true, RuntimePM: true}},
		{"phone in performance has no budget", State{Profile: profiles.Performance, Form: Phone},
			Settings{Platform: profiles.Performance, Bias: BiasPerformance, Boost: true, RuntimePM: true, AudioSaving: true}},
	}
	for _, c := range cases {
		if got := Decide(c.in); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseForm(t *testing.T) {
	if f, ok := ParseForm("tv"); !ok || f != TV {
		t.Fatal("tv not parsed")
	}
	if _, ok := ParseForm("toaster"); ok {
		t.Fatal("unknown form accepted")
	}
}

func TestDetectForm(t *testing.T) {
	root := t.TempDir()
	if f := DetectForm(root); f != Desktop {
		t.Fatalf("empty machine: %s", f)
	}
	os.MkdirAll(filepath.Join(root, "sys/class/dmi/id"), 0o755)
	os.WriteFile(filepath.Join(root, "sys/class/dmi/id/chassis_type"), []byte("10\n"), 0o644)
	if f := DetectForm(root); f != Laptop {
		t.Fatalf("notebook chassis: %s", f)
	}
}
