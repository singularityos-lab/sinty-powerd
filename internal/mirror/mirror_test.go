package mirror

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
	"github.com/singularityos-lab/sinty-powerd/internal/supply"
	"github.com/singularityos-lab/sinty-powerd/internal/thermal"
)

type fake struct {
	ints map[string]int64
	strs map[string]string
	set  map[string]int64
}

func (f *fake) Int(n string) (int64, bool)  { v, ok := f.ints[n]; return v, ok }
func (f *fake) Str(n string) (string, bool) { v, ok := f.strs[n]; return v, ok }
func (f *fake) SetInt(n string, v int64) error {
	f.set[n] = v
	f.ints[n] = v
	return nil
}

func laptop() *fake {
	return &fake{
		ints: map[string]int64{
			"hw.acpi.acline":                  0,
			"hw.acpi.battery.units":           1,
			"hw.acpi.battery.life":            64,
			"hw.acpi.battery.state":           1,
			"hw.acpi.battery.rate":            7350,
			"hw.acpi.thermal.tz0.temperature": 3231,
			"hw.acpi.thermal.tz0._CRT":        3781,
			"dev.hwpstate_intel.0.epp":        50,
			"dev.hwpstate_intel.1.epp":        50,
		},
		strs: map[string]string{"dev.cpu.0.freq_levels": "2401/15000 2400/15000 1800/9000 1200/5000 800/2000"},
		set:  map[string]int64{},
	}
}

func TestExistingReadersSeeTheMirror(t *testing.T) {
	root := t.TempDir()
	ctl := laptop()
	m := New(root, ctl)
	if err := m.Sync(); err != nil {
		t.Fatal(err)
	}
	devs := supply.Scan(filepath.Join(root, "sys/class/power_supply"), supply.DefaultPolicy, nil)
	var ac, bat *supply.Device
	for _, d := range devs {
		switch d.NativePath {
		case "AC":
			ac = d
		case "BAT0":
			bat = d
		}
	}
	if ac == nil || ac.Online || bat == nil || bat.Percentage != 64 || bat.EnergyRate != 7.35 {
		t.Fatalf("supplies ac=%+v bat=%+v", ac, bat)
	}
	if g := thermal.Detect(root); g == nil || g.Kind() != "max-freq" || g.Target() != 89950 {
		t.Fatalf("thermal guard %+v", g)
	}
	b := profiles.Detect(root)
	if !b.Supports(profiles.Performance) {
		t.Fatal("energy preference not offered")
	}
	if err := b.Apply(profiles.PowerSaver, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Writable() {
		if err := m.Apply(p); err != nil {
			t.Fatal(err)
		}
	}
	if ctl.set["dev.hwpstate_intel.0.epp"] != 100 || ctl.set["dev.hwpstate_intel.1.epp"] != 100 {
		t.Fatalf("epp written %v", ctl.set)
	}
	if _, ok := ctl.set["dev.cpu.0.freq"]; ok {
		t.Fatal("frequency changed without a thermal step")
	}
}

func TestFrequencyCeilingPicksALevelBelow(t *testing.T) {
	root := t.TempDir()
	ctl := laptop()
	m := New(root, ctl)
	m.Sync()
	f := filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq")
	os.WriteFile(f, []byte("2000000"), 0)
	if err := m.Apply(f); err != nil {
		t.Fatal(err)
	}
	if ctl.set["dev.cpu.0.freq"] != 1800 {
		t.Fatalf("freq %v", ctl.set)
	}
	os.WriteFile(f, []byte("100000"), 0)
	m.Apply(f)
	if ctl.set["dev.cpu.0.freq"] != 800 {
		t.Fatalf("freq floor %v", ctl.set)
	}
}

func TestSyncKeepsWhatPowerdWrote(t *testing.T) {
	root := t.TempDir()
	ctl := laptop()
	m := New(root, ctl)
	m.Sync()
	f := filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy0/energy_performance_preference")
	os.WriteFile(f, []byte("power"), 0)
	ctl.ints["hw.acpi.acline"] = 1
	m.Sync()
	data, _ := os.ReadFile(f)
	if string(data) != "power" {
		t.Fatalf("sync overwrote a pending write: %q", data)
	}
	ac, _ := os.ReadFile(filepath.Join(root, "sys/class/power_supply/AC/uevent"))
	if string(ac) != "POWER_SUPPLY_TYPE=Mains\nPOWER_SUPPLY_ONLINE=1\n" {
		t.Fatalf("ac %q", ac)
	}
}

func TestDesktopWithoutACPIBattery(t *testing.T) {
	root := t.TempDir()
	m := New(root, &fake{ints: map[string]int64{"hw.acpi.acline": 1}, strs: map[string]string{}, set: map[string]int64{}})
	if err := m.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sys/class/power_supply/BAT0")); err == nil {
		t.Fatal("battery mirrored on a desktop")
	}
	if thermal.Detect(root) != nil {
		t.Fatal("thermal guard without sensors")
	}
}
