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
	if g == nil || g.Target() != 90000 || g.temp != filepath.Join(root, "sys/class/thermal/thermal_zone1/temp") {
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

func TestNoPowerLimitMeansNoGuard(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/class/thermal/thermal_zone0/type", "x86_pkg_temp")
	put(t, root, "sys/class/thermal/thermal_zone0/temp", "50000")
	if Detect(root) != nil {
		t.Error("a guard without a power limit to steer")
	}
}
