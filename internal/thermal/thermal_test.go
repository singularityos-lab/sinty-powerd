package thermal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func put(t *testing.T, root, path, v string) {
	t.Helper()
	p := filepath.Join(root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(v+"\n"), 0o644)
}

func TestGuardThrottlesAndRecovers(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/class/thermal/thermal_zone0/type", "acpitz")
	put(t, root, "sys/class/thermal/thermal_zone0/temp", "40000")
	put(t, root, "sys/class/thermal/thermal_zone0/trip_point_0_type", "critical")
	put(t, root, "sys/class/thermal/thermal_zone0/trip_point_0_temp", "105000")
	put(t, root, "sys/class/thermal/thermal_zone1/type", "x86_pkg_temp")
	put(t, root, "sys/class/thermal/thermal_zone1/temp", "60000")
	put(t, root, "sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw", "28000000")
	g := Detect(root)
	if g == nil || g.Target() != 90000 || len(g.temps) != 1 || g.temps[0] != filepath.Join(root, "sys/class/thermal/thermal_zone1/temp") {
		t.Fatalf("guard %+v", g)
	}
	limit := func() int {
		data, _ := os.ReadFile(filepath.Join(root, "sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw"))
		v, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		return v
	}
	if g.Step() || limit() != 28000000 {
		t.Fatal("a cool processor must keep full power")
	}
	put(t, root, "sys/class/thermal/thermal_zone1/temp", "93000")
	for i := 0; i < 20; i++ {
		g.Step()
	}
	if got := limit(); got != 28000000*4/10 || !g.Step() {
		t.Errorf("hot processor limited to %d, want the floor", got)
	}
	put(t, root, "sys/class/thermal/thermal_zone1/temp", "85000")
	before := limit()
	g.Step()
	if limit() != before {
		t.Error("the limit must hold inside the hysteresis band")
	}
	put(t, root, "sys/class/thermal/thermal_zone1/temp", "70000")
	for i := 0; i < 40; i++ {
		g.Step()
	}
	if limit() != 28000000 || g.Step() {
		t.Errorf("cooled processor at %d, want full power back", limit())
	}
}

func TestNothingToSteerMeansNoGuard(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/class/thermal/thermal_zone0/type", "x86_pkg_temp")
	put(t, root, "sys/class/thermal/thermal_zone0/temp", "50000")
	if Detect(root) != nil {
		t.Error("a guard with nothing to steer")
	}
}

func TestFallbackWithoutRAPL(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/class/thermal/thermal_zone0/type", "x86_pkg_temp")
	put(t, root, "sys/class/thermal/thermal_zone0/temp", "99000")
	put(t, root, "sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq", "4800000")
	put(t, root, "sys/devices/system/cpu/cpufreq/policy1/scaling_max_freq", "3600000")
	g := Detect(root)
	if g == nil || g.Kind() != "max-freq" {
		t.Fatalf("guard %+v", g)
	}
	for i := 0; i < 20; i++ {
		g.Step()
	}
	read := func(p string) string {
		data, _ := os.ReadFile(filepath.Join(root, p))
		return strings.TrimSpace(string(data))
	}
	if read("sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq") != "1920000" || read("sys/devices/system/cpu/cpufreq/policy1/scaling_max_freq") != "1440000" {
		t.Fatal("frequencies not lowered to the floor")
	}
	g.Restore()
	if read("sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq") != "4800000" {
		t.Fatal("frequency not given back")
	}
	put(t, root, "sys/devices/system/cpu/intel_pstate/max_perf_pct", "100")
	if g := Detect(root); g == nil || g.Kind() != "max-perf-pct" {
		t.Fatal("intel_pstate limit not preferred over frequencies")
	}
}

func TestPowerBudgetCapsTheLimit(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/class/thermal/thermal_zone0/type", "x86_pkg_temp")
	put(t, root, "sys/class/thermal/thermal_zone0/temp", "50000")
	put(t, root, "sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw", "40000000")
	g := Detect(root)
	limit := func() string {
		data, _ := os.ReadFile(filepath.Join(root, "sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw"))
		return strings.TrimSpace(string(data))
	}
	g.SetCeilingWatts(20)
	if limit() != "20000000" || g.Step() {
		t.Fatalf("budget limit %s", limit())
	}
	g.SetCeilingWatts(0)
	if limit() != "40000000" {
		t.Fatalf("budget not lifted: %s", limit())
	}
}

func TestGuardWatchesEveryCoreZoneOnArm(t *testing.T) {
	root := t.TempDir()
	zones := map[string]string{"0": "soc-thermal", "1": "bigcore0-thermal", "2": "littlecore-thermal", "3": "gpu-thermal", "4": "battery"}
	for n, kind := range zones {
		put(t, root, "sys/class/thermal/thermal_zone"+n+"/type", kind)
		put(t, root, "sys/class/thermal/thermal_zone"+n+"/temp", "50000")
	}
	put(t, root, "sys/class/thermal/thermal_zone0/trip_point_0_type", "critical")
	put(t, root, "sys/class/thermal/thermal_zone0/trip_point_0_temp", "115000")
	for _, p := range []string{"policy0", "policy4"} {
		put(t, root, "sys/devices/system/cpu/cpufreq/"+p+"/scaling_max_freq", "2400000")
	}
	g := Detect(root)
	if g == nil || len(g.temps) != 3 || g.Kind() != "max-freq" {
		t.Fatalf("guard %+v", g)
	}
	put(t, root, "sys/class/thermal/thermal_zone3/temp", "99000")
	if g.Step() {
		t.Fatal("a hot GPU zone must not slow the processor")
	}
	put(t, root, "sys/class/thermal/thermal_zone1/temp", "99000")
	if !g.Step() || g.Temperature() != 99000 {
		t.Fatalf("hot big core not acted on: %d", g.Temperature())
	}
	data, _ := os.ReadFile(filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy4/scaling_max_freq"))
	if strings.TrimSpace(string(data)) == "2400000" {
		t.Fatal("frequency cap not lowered")
	}
}
