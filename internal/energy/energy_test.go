package energy

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func put(t *testing.T, root, path, v string) {
	t.Helper()
	p := filepath.Join(root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(v+"\n"), 0o644)
}

func TestMeterReportsPackagePowerAndIdle(t *testing.T) {
	root := t.TempDir()
	if Detect(root) != nil {
		t.Fatal("a meter without counters")
	}
	put(t, root, "sys/class/powercap/intel-rapl:0/energy_uj", "1000000")
	put(t, root, "sys/class/powercap/intel-rapl:0/max_energy_range_uj", "10000000")
	put(t, root, "sys/devices/system/cpu/cpuidle/low_power_idle_system_residency_us", "2500000")
	m := Detect(root)
	if m == nil {
		t.Fatal("counters not found")
	}
	start := time.Unix(1000, 0)
	m.Sample(start)
	if _, ok := m.Readings()["package-watts"]; ok {
		t.Fatal("power from a single sample")
	}
	put(t, root, "sys/class/powercap/intel-rapl:0/energy_uj", "4000000")
	m.Sample(start.Add(2 * time.Second))
	r := m.Readings()
	if r["package-watts"] != 1.5 {
		t.Fatalf("readings %v", r)
	}
	if r["system-low-power-idle-seconds"] != 2.5 {
		t.Fatalf("readings %v", r)
	}
	if _, ok := r["cpu-low-power-idle-seconds"]; ok {
		t.Fatalf("readings %v", r)
	}
	put(t, root, "sys/class/powercap/intel-rapl:0/energy_uj", "1000000")
	m.Sample(start.Add(4 * time.Second))
	if w := m.Readings()["package-watts"]; math.Abs(w-3.5) > 1e-9 {
		t.Fatalf("after the counter wrapped: %v", w)
	}
}
