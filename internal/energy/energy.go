// Package energy reads what the hardware reports about energy use: the power
// drawn by the processor package from its RAPL counter and the time the
// processor and the whole system spent in their low power idle states.
package energy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Meter struct {
	energy     string
	wrap       uint64
	cpuIdle    string
	systemIdle string
	last       uint64
	lastAt     time.Time
	watts      float64
}

func readUint(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return v, err == nil
}

func Detect(root string) *Meter {
	m := &Meter{}
	rapl := filepath.Join(root, "sys/class/powercap/intel-rapl:0")
	if _, ok := readUint(filepath.Join(rapl, "energy_uj")); ok {
		m.energy = filepath.Join(rapl, "energy_uj")
		m.wrap, _ = readUint(filepath.Join(rapl, "max_energy_range_uj"))
	}
	idle := filepath.Join(root, "sys/devices/system/cpu/cpuidle")
	if _, ok := readUint(filepath.Join(idle, "low_power_idle_cpu_residency_us")); ok {
		m.cpuIdle = filepath.Join(idle, "low_power_idle_cpu_residency_us")
	}
	if _, ok := readUint(filepath.Join(idle, "low_power_idle_system_residency_us")); ok {
		m.systemIdle = filepath.Join(idle, "low_power_idle_system_residency_us")
	}
	if m.energy == "" && m.cpuIdle == "" && m.systemIdle == "" {
		return nil
	}
	return m
}

func (m *Meter) Sample(now time.Time) {
	if m.energy == "" {
		return
	}
	v, ok := readUint(m.energy)
	if !ok {
		return
	}
	if !m.lastAt.IsZero() {
		delta := v - m.last
		if v < m.last {
			if m.wrap == 0 {
				m.last, m.lastAt = v, now
				return
			}
			delta = m.wrap - m.last + v
		}
		if elapsed := now.Sub(m.lastAt).Seconds(); elapsed > 0 {
			m.watts = float64(delta) / 1e6 / elapsed
		}
	}
	m.last, m.lastAt = v, now
}

func (m *Meter) Readings() map[string]float64 {
	out := map[string]float64{}
	if m.energy != "" && m.watts > 0 {
		out["package-watts"] = m.watts
	}
	if v, ok := readUint(m.cpuIdle); ok && m.cpuIdle != "" {
		out["cpu-low-power-idle-seconds"] = float64(v) / 1e6
	}
	if v, ok := readUint(m.systemIdle); ok && m.systemIdle != "" {
		out["system-low-power-idle-seconds"] = float64(v) / 1e6
	}
	return out
}
