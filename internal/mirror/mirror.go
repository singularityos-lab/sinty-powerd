package mirror

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Sysctl interface {
	Int(name string) (int64, bool)
	Str(name string) (string, bool)
	SetInt(name string, v int64) error
}

type Mirror struct {
	Root   string
	Ctl    Sysctl
	mu     sync.Mutex
	writes map[string]func(string) error
	last   map[string]string
}

func New(root string, ctl Sysctl) *Mirror {
	return &Mirror{Root: root, Ctl: ctl, writes: map[string]func(string) error{}, last: map[string]string{}}
}

func (m *Mirror) put(rel, value string) error {
	path := filepath.Join(m.Root, rel)
	if m.last[path] == value {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return err
	}
	m.last[path] = value
	return nil
}

func (m *Mirror) writable(rel, value string, apply func(string) error) error {
	path := filepath.Join(m.Root, rel)
	m.writes[path] = apply
	if _, seen := m.last[path]; seen {
		return nil
	}
	return m.put(rel, value)
}

var eppNames = []struct {
	name  string
	value int64
}{
	{"performance", 0},
	{"balance_performance", 33},
	{"default", 50},
	{"balance_power", 66},
	{"power", 100},
}

func eppName(v int64) string {
	best := eppNames[0]
	for _, e := range eppNames {
		if abs(e.value-v) < abs(best.value-v) {
			best = e
		}
	}
	return best.name
}

func eppValue(name string) (int64, bool) {
	for _, e := range eppNames {
		if e.name == name {
			return e.value, true
		}
	}
	return 0, false
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func celsiusMilli(deciKelvin int64) int64 { return deciKelvin*100 - 273150 }

func (m *Mirror) Sync() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []string
	note := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	if ac, ok := m.Ctl.Int("hw.acpi.acline"); ok {
		note(m.put("sys/class/power_supply/AC/uevent", fmt.Sprintf("POWER_SUPPLY_TYPE=Mains\nPOWER_SUPPLY_ONLINE=%d\n", ac)))
	}
	if units, ok := m.Ctl.Int("hw.acpi.battery.units"); ok && units > 0 {
		note(m.put("sys/class/power_supply/BAT0/uevent", m.battery()))
	}
	for i := 0; ; i++ {
		base := fmt.Sprintf("hw.acpi.thermal.tz%d.", i)
		t, ok := m.Ctl.Int(base + "temperature")
		if !ok {
			break
		}
		zone := fmt.Sprintf("sys/class/thermal/thermal_zone%d/", i)
		note(m.put(zone+"type", "acpitz\n"))
		note(m.put(zone+"temp", strconv.FormatInt(celsiusMilli(t), 10)+"\n"))
		if crt, ok := m.Ctl.Int(base + "_CRT"); ok && crt > 0 {
			note(m.put(zone+"trip_point_0_type", "critical\n"))
			note(m.put(zone+"trip_point_0_temp", strconv.FormatInt(celsiusMilli(crt), 10)+"\n"))
		}
	}
	for i := 0; ; i++ {
		name := fmt.Sprintf("dev.hwpstate_intel.%d.epp", i)
		v, ok := m.Ctl.Int(name)
		if !ok {
			break
		}
		policy := fmt.Sprintf("sys/devices/system/cpu/cpufreq/policy%d/", i)
		note(m.put(policy+"scaling_driver", "hwpstate_intel\n"))
		note(m.put(policy+"energy_performance_available_preferences", "default performance balance_performance balance_power power\n"))
		note(m.writable(policy+"energy_performance_preference", eppName(v)+"\n", func(s string) error {
			want, ok := eppValue(s)
			if !ok {
				return fmt.Errorf("unknown energy preference %q", s)
			}
			return m.Ctl.SetInt(name, want)
		}))
	}
	if levels, ok := m.Ctl.Str("dev.cpu.0.freq_levels"); ok {
		if top := topLevel(levels); top > 0 {
			policy := "sys/devices/system/cpu/cpufreq/policy0/"
			note(m.put(policy+"cpuinfo_max_freq", strconv.FormatInt(top*1000, 10)+"\n"))
			note(m.writable(policy+"scaling_max_freq", strconv.FormatInt(top*1000, 10)+"\n", func(s string) error {
				khz, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return err
				}
				return m.Ctl.SetInt("dev.cpu.0.freq", floorLevel(levels, khz/1000))
			}))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mirror: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Mirror) battery() string {
	life, _ := m.Ctl.Int("hw.acpi.battery.life")
	state, _ := m.Ctl.Int("hw.acpi.battery.state")
	ac, _ := m.Ctl.Int("hw.acpi.acline")
	status := "Not charging"
	switch {
	case state&1 != 0:
		status = "Discharging"
	case state&2 != 0:
		status = "Charging"
	case ac == 1 && life >= 100:
		status = "Full"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "POWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_PRESENT=1\nPOWER_SUPPLY_STATUS=%s\nPOWER_SUPPLY_CAPACITY=%d\n", status, life)
	if rate, ok := m.Ctl.Int("hw.acpi.battery.rate"); ok && rate > 0 {
		fmt.Fprintf(&b, "POWER_SUPPLY_POWER_NOW=%d\n", rate*1000)
	}
	return b.String()
}

func parseLevels(levels string) []int64 {
	var out []int64
	for _, f := range strings.Fields(levels) {
		mhz, _, _ := strings.Cut(f, "/")
		if v, err := strconv.ParseInt(mhz, 10, 64); err == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

func topLevel(levels string) int64 {
	l := parseLevels(levels)
	if len(l) == 0 {
		return 0
	}
	return l[0]
}

func floorLevel(levels string, mhz int64) int64 {
	l := parseLevels(levels)
	for _, v := range l {
		if v <= mhz {
			return v
		}
	}
	return l[len(l)-1]
}

func (m *Mirror) Writable() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.writes))
	for p := range m.writes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (m *Mirror) Apply(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	apply, ok := m.writes[path]
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	value := strings.TrimSpace(string(data))
	if value == strings.TrimSpace(m.last[path]) {
		return nil
	}
	if err := apply(value); err != nil {
		return err
	}
	m.last[path] = value + "\n"
	return nil
}
