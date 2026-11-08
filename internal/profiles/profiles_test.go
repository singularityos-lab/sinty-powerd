package profiles

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func put(t *testing.T, root, path, value string) {
	t.Helper()
	p := filepath.Join(root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func machine(t *testing.T) string {
	root := t.TempDir()
	put(t, root, "sys/firmware/acpi/platform_profile_choices", "low-power balanced performance")
	put(t, root, "sys/firmware/acpi/platform_profile", "balanced")
	for _, p := range []string{"policy0", "policy1"} {
		dir := "sys/devices/system/cpu/cpufreq/" + p + "/"
		put(t, root, dir+"energy_performance_available_preferences", "default performance balance_performance balance_power power")
		put(t, root, dir+"energy_performance_preference", "balance_performance")
		put(t, root, dir+"scaling_driver", "intel_pstate")
	}
	return root
}

func TestProfilesMatchPowerProfilesDaemon(t *testing.T) {
	b := Detect(machine(t))
	want := []Profile{
		{PowerSaver, "intel_pstate", "platform_profile", "multiple"},
		{Balanced, "intel_pstate", "platform_profile", "multiple"},
		{Performance, "intel_pstate", "platform_profile", "multiple"},
	}
	if got := b.Profiles(); !reflect.DeepEqual(got, want) {
		t.Errorf("profiles\n got %+v\nwant %+v", got, want)
	}
}

func TestApplyWritesEveryKnob(t *testing.T) {
	root := machine(t)
	b := Detect(root)
	if err := b.Apply(PowerSaver, false); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(root, "sys/firmware/acpi/platform_profile")); got != "low-power" {
		t.Errorf("platform profile %q", got)
	}
	for _, p := range []string{"policy0", "policy1"} {
		if got := read(filepath.Join(root, "sys/devices/system/cpu/cpufreq", p, "energy_performance_preference")); got != "power" {
			t.Errorf("%s epp %q", p, got)
		}
	}
	b.Apply(Performance, false)
	if got := read(filepath.Join(root, "sys/firmware/acpi/platform_profile")); got != "performance" {
		t.Errorf("platform profile %q", got)
	}
}

func TestNoDriversStillOffersTwoProfiles(t *testing.T) {
	b := Detect(t.TempDir())
	got := b.Profiles()
	if len(got) != 2 || got[0].Driver != "placeholder" || b.Supports(Performance) {
		t.Errorf("profiles without drivers: %+v", got)
	}
	if err := b.Apply(Performance, false); err == nil {
		t.Error("performance must be refused without a driver")
	}
}

func TestBalancedOnBatterySavesPower(t *testing.T) {
	root := machine(t)
	b := Detect(root)
	epp := filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy0/energy_performance_preference")
	if err := b.Apply(Balanced, true); err != nil {
		t.Fatal(err)
	}
	if got := read(epp); got != "balance_power" {
		t.Errorf("balanced on battery epp %q", got)
	}
	if got := read(filepath.Join(root, "sys/firmware/acpi/platform_profile")); got != "balanced" {
		t.Errorf("balanced on battery platform profile %q", got)
	}
	b.Apply(Balanced, false)
	if got := read(epp); got != "balance_performance" {
		t.Errorf("balanced on mains epp %q", got)
	}
	b.Apply(Performance, true)
	if got := read(epp); got != "performance" {
		t.Errorf("performance on battery epp %q", got)
	}
}

func TestSettingsTurnBoostOffAndOn(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/devices/system/cpu/cpufreq/policy0/energy_performance_preference", "balance_performance")
	put(t, root, "sys/devices/system/cpu/cpufreq/policy0/energy_performance_available_preferences", "default performance balance_performance balance_power power")
	put(t, root, "sys/devices/system/cpu/intel_pstate/no_turbo", "0")
	b := Detect(root)
	if err := b.ApplySettings(PowerSaver, "power", false); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(root, "sys/devices/system/cpu/intel_pstate/no_turbo")); got != "1" {
		t.Fatalf("no_turbo %s", got)
	}
	if got := read(filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy0/energy_performance_preference")); got != "power" {
		t.Fatalf("epp %s", got)
	}
	b.ApplySettings(Balanced, "balance-power", true)
	if got := read(filepath.Join(root, "sys/devices/system/cpu/intel_pstate/no_turbo")); got != "0" {
		t.Fatalf("no_turbo after balanced %s", got)
	}
	if got := read(filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy0/energy_performance_preference")); got != "balance_power" {
		t.Fatalf("epp %s", got)
	}
}
