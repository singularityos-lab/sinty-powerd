// Package diagnose finds what keeps a machine from saving power: the
// processes and interrupts that wake the processor, the wakeup sources that
// end a suspend, the devices kept powered and the idle states actually
// reached.
package diagnose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func number(path string) uint64 {
	v, _ := strconv.ParseUint(read(path), 10, 64)
	return v
}

type Snapshot struct {
	At       time.Time
	Procs    map[int]proc
	Idle     map[string]uint64
	Wakeups  map[string]uint64
	Packages map[string]uint64
}

type proc struct {
	name     string
	switches uint64
}

func Take(root string, now time.Time) Snapshot {
	s := Snapshot{At: now, Procs: map[int]proc{}, Idle: map[string]uint64{}, Wakeups: map[string]uint64{}, Packages: map[string]uint64{}}
	dirs, _ := filepath.Glob(filepath.Join(root, "proc/[0-9]*"))
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		var p proc
		for _, line := range strings.Split(read(filepath.Join(d, "status")), "\n") {
			k, v, _ := strings.Cut(line, ":")
			v = strings.TrimSpace(v)
			switch k {
			case "Name":
				p.name = v
			case "voluntary_ctxt_switches", "nonvoluntary_ctxt_switches":
				n, _ := strconv.ParseUint(v, 10, 64)
				p.switches += n
			}
		}
		if p.name != "" {
			s.Procs[pid] = p
		}
	}
	states, _ := filepath.Glob(filepath.Join(root, "sys/devices/system/cpu/cpu[0-9]*/cpuidle/state[0-9]*"))
	for _, st := range states {
		s.Idle[read(filepath.Join(st, "name"))] += number(filepath.Join(st, "time"))
	}
	sources, _ := filepath.Glob(filepath.Join(root, "sys/class/wakeup/wakeup*"))
	for _, w := range sources {
		if n := number(filepath.Join(w, "event_count")); n > 0 {
			s.Wakeups[read(filepath.Join(w, "name"))] += n
		}
	}
	for _, f := range []string{"low_power_idle_cpu_residency_us", "low_power_idle_system_residency_us"} {
		if v := read(filepath.Join(root, "sys/devices/system/cpu/cpuidle", f)); v != "" {
			s.Packages[f], _ = strconv.ParseUint(v, 10, 64)
		}
	}
	return s
}

type Rate struct {
	Name   string
	PerSec float64
}

func top(m map[string]float64, n int) []Rate {
	out := make([]Rate, 0, len(m))
	for k, v := range m {
		if v > 0 {
			out = append(out, Rate{k, v})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PerSec != out[j].PerSec {
			return out[i].PerSec > out[j].PerSec
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func Wakers(a, b Snapshot, n int) []Rate {
	secs := b.At.Sub(a.At).Seconds()
	if secs <= 0 {
		return nil
	}
	m := map[string]float64{}
	for pid, pb := range b.Procs {
		if pa, ok := a.Procs[pid]; ok && pa.name == pb.name && pb.switches >= pa.switches {
			m[fmt.Sprintf("%s[%d]", pb.name, pid)] = float64(pb.switches-pa.switches) / secs
		}
	}
	return top(m, n)
}

func WakeupSources(a, b Snapshot, n int) []Rate {
	secs := b.At.Sub(a.At).Seconds()
	if secs <= 0 {
		return nil
	}
	m := map[string]float64{}
	for k, vb := range b.Wakeups {
		if vb >= a.Wakeups[k] {
			m[k] = float64(vb-a.Wakeups[k]) / secs
		}
	}
	return top(m, n)
}

func IdleShare(a, b Snapshot, cpus int) []Rate {
	secs := b.At.Sub(a.At).Seconds()
	if secs <= 0 || cpus == 0 {
		return nil
	}
	m := map[string]float64{}
	for k, vb := range b.Idle {
		if vb >= a.Idle[k] {
			m[k] = float64(vb-a.Idle[k]) / 1e6 / secs / float64(cpus) * 100
		}
	}
	for k, vb := range b.Packages {
		if vb >= a.Packages[k] {
			m[k] = float64(vb-a.Packages[k]) / 1e6 / secs * 100
		}
	}
	return top(m, len(m))
}

func CPUs(root string) int {
	dirs, _ := filepath.Glob(filepath.Join(root, "sys/devices/system/cpu/cpu[0-9]*/cpuidle"))
	return len(dirs)
}

func LastSuspend(root string) []string {
	var out []string
	for _, f := range []string{"mem_sleep", "pm_wakeup_irq", "suspend_stats/success", "suspend_stats/fail", "suspend_stats/last_failed_dev", "suspend_stats/last_hw_sleep", "suspend_stats/total_hw_sleep"} {
		if v := read(filepath.Join(root, "sys/power", f)); v != "" {
			out = append(out, f+": "+v)
		}
	}
	return out
}

func Report(root string, d time.Duration, awake []string) map[string][]string {
	a := Take(root, time.Now())
	time.Sleep(d)
	b := Take(root, time.Now())
	format := func(rs []Rate, unit string) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, fmt.Sprintf("%s %.1f%s", r.Name, r.PerSec, unit))
		}
		return out
	}
	return map[string][]string{
		"wakers":         format(Wakers(a, b, 15), "/s"),
		"wakeup-sources": format(WakeupSources(a, b, 10), "/s"),
		"idle":           format(IdleShare(a, b, CPUs(root)), "%"),
		"devices-awake":  awake,
		"suspend":        LastSuspend(root),
	}
}
