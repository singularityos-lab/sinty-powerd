package diagnose

import (
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

func TestRatesBetweenSnapshots(t *testing.T) {
	root := t.TempDir()
	put(t, root, "proc/10/status", "Name:\tchatty\nvoluntary_ctxt_switches:\t100\nnonvoluntary_ctxt_switches:\t0")
	put(t, root, "proc/11/status", "Name:\tquiet\nvoluntary_ctxt_switches:\t5\nnonvoluntary_ctxt_switches:\t0")
	put(t, root, "sys/devices/system/cpu/cpu0/cpuidle/state1/name", "C1")
	put(t, root, "sys/devices/system/cpu/cpu0/cpuidle/state1/time", "0")
	put(t, root, "sys/devices/system/cpu/cpu0/cpuidle/state2/name", "C10")
	put(t, root, "sys/devices/system/cpu/cpu0/cpuidle/state2/time", "0")
	put(t, root, "sys/class/wakeup/wakeup0/name", "PNP0C0D:00")
	put(t, root, "sys/class/wakeup/wakeup0/event_count", "1")
	start := time.Unix(100, 0)
	a := Take(root, start)
	put(t, root, "proc/10/status", "Name:\tchatty\nvoluntary_ctxt_switches:\t300\nnonvoluntary_ctxt_switches:\t100")
	put(t, root, "sys/devices/system/cpu/cpu0/cpuidle/state1/time", "500000")
	put(t, root, "sys/devices/system/cpu/cpu0/cpuidle/state2/time", "1000000")
	put(t, root, "sys/class/wakeup/wakeup0/event_count", "3")
	b := Take(root, start.Add(2*time.Second))
	w := Wakers(a, b, 5)
	if len(w) != 1 || w[0].Name != "chatty[10]" || w[0].PerSec != 150 {
		t.Fatalf("wakers %+v", w)
	}
	idle := IdleShare(a, b, CPUs(root))
	if len(idle) != 2 || idle[0].Name != "C10" || idle[0].PerSec != 50 || idle[1].PerSec != 25 {
		t.Fatalf("idle %+v", idle)
	}
	if ws := WakeupSources(a, b, 5); len(ws) != 1 || ws[0].PerSec != 1 {
		t.Fatalf("wakeup sources %+v", ws)
	}
}
