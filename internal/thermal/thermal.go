// Package thermal keeps the processor below its throttling temperature by
// lowering its sustained power limit while it runs hot and giving the power
// back once it has cooled, which is what thermald does for the package. The
// kernel still owns the hard trip points; this only acts before them, so the
// machine slows down gradually instead of hitting the firmware clamp.
package thermal

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/singularityos-lab/libsingularity/src/system/core/go/kernfs"
)

// Guard watches one temperature and steers one power limit.
type Guard struct {
	temps      []*sensor
	zone       string
	trip       *Trip
	knobs      []knob
	original   uint64
	current    uint64
	floor      uint64
	target     int64
	hysteresis int64
	last       int64
	kind       string
}

type knob struct {
	path     string
	original uint64
}

type sensor struct {
	path string
	f    *os.File
	buf  [32]byte
}

func (s *sensor) read() (int64, bool) {
	if s.f == nil {
		f, err := os.Open(s.path)
		if err != nil {
			return 0, false
		}
		s.f = f
	}
	n, err := s.f.ReadAt(s.buf[:], 0)
	if n == 0 && err != nil {
		s.f.Close()
		s.f = nil
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(s.buf[:n])), 10, 64)
	return v, err == nil
}

func readInt(path string) (int64, bool) {
	data, err := kernfs.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return v, err == nil
}

// Detect finds the package temperature and the RAPL package power limit
// under root, which is "/" on a running system. It returns nil when either is
// missing: there is then nothing to steer.
func Detect(root string) *Guard {
	zones, _ := filepath.Glob(filepath.Join(root, "sys/class/thermal/thermal_zone*"))
	var temp, zone string
	critical := int64(0)
	for _, pref := range []string{"x86_pkg_temp", "TCPU", "acpitz"} {
		for _, z := range zones {
			data, _ := kernfs.ReadFile(filepath.Join(z, "type"))
			if strings.TrimSpace(string(data)) != pref {
				continue
			}
			if temp == "" {
				temp, zone = filepath.Join(z, "temp"), z
			}
		}
	}
	for _, z := range zones {
		trips, _ := filepath.Glob(filepath.Join(z, "trip_point_*_type"))
		for _, t := range trips {
			data, _ := kernfs.ReadFile(t)
			if strings.TrimSpace(string(data)) != "critical" {
				continue
			}
			if v, ok := readInt(strings.TrimSuffix(t, "_type") + "_temp"); ok && v > 0 && (critical == 0 || v < critical) {
				critical = v
			}
		}
	}
	paths := []string{temp}
	if temp == "" {
		paths = socZones(zones)
	}
	if len(paths) == 0 {
		return nil
	}
	temps := make([]*sensor, len(paths))
	for i, p := range paths {
		temps[i] = &sensor{path: p}
	}
	knobs, kind := limits(root)
	if len(knobs) == 0 {
		return nil
	}
	target := int64(95000)
	if critical > 0 && critical-15000 < target {
		target = critical - 15000
	}
	return &Guard{
		temps:      temps,
		zone:       zone,
		knobs:      knobs,
		kind:       kind,
		original:   1000,
		current:    1000,
		floor:      400,
		target:     target,
		hysteresis: 8000,
	}
}

func socZones(zones []string) []string {
	var out []string
	for _, z := range zones {
		data, _ := kernfs.ReadFile(filepath.Join(z, "type"))
		kind := strings.ToLower(strings.TrimSpace(string(data)))
		if strings.Contains(kind, "gpu") || strings.Contains(kind, "battery") || strings.Contains(kind, "pmic") {
			continue
		}
		if strings.Contains(kind, "cpu") || strings.Contains(kind, "core") || strings.Contains(kind, "soc") || strings.Contains(kind, "cluster") {
			out = append(out, filepath.Join(z, "temp"))
		}
	}
	return out
}

func (g *Guard) hottest() (int64, bool) {
	var best int64
	found := false
	for _, p := range g.temps {
		if t, ok := p.read(); ok && (!found || t > best) {
			best, found = t, true
		}
	}
	return best, found
}

func limits(root string) ([]knob, string) {
	rapl := filepath.Join(root, "sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw")
	if v, ok := readInt(rapl); ok && v > 0 {
		return []knob{{rapl, uint64(v)}}, "rapl"
	}
	pct := filepath.Join(root, "sys/devices/system/cpu/intel_pstate/max_perf_pct")
	if v, ok := readInt(pct); ok && v > 0 {
		return []knob{{pct, uint64(v)}}, "max-perf-pct"
	}
	var out []knob
	files, _ := filepath.Glob(filepath.Join(root, "sys/devices/system/cpu/cpufreq/policy*/scaling_max_freq"))
	for _, f := range files {
		if v, ok := readInt(f); ok && v > 0 {
			out = append(out, knob{f, uint64(v)})
		}
	}
	if len(out) > 0 {
		return out, "max-freq"
	}
	return nil, ""
}

func (g *Guard) Kind() string { return g.kind }

func (g *Guard) write(share uint64) bool {
	ok := true
	for _, k := range g.knobs {
		v := k.original * share / 1000
		if os.WriteFile(k.path, []byte(strconv.FormatUint(v, 10)), 0) != nil {
			ok = false
		}
	}
	return ok
}

func (g *Guard) SetCeilingWatts(watts float64) {
	ceiling := uint64(1000)
	if watts > 0 && g.kind == "rapl" && g.knobs[0].original > 0 {
		ceiling = min(1000, max(g.floor, uint64(watts*1e6*1000/float64(g.knobs[0].original))))
	}
	if ceiling == g.original {
		return
	}
	full := g.current == g.original
	g.original = ceiling
	if (full || g.current > ceiling) && g.write(ceiling) {
		g.current = ceiling
	}
}

func (g *Guard) Reapply() {
	g.write(g.current)
}

// Target is the temperature, in millidegrees, the guard keeps below.
func (g *Guard) Target() int64 { return g.target }

// Near is the temperature from which the guard reads the sensor every few
// seconds instead of waiting.
func (g *Guard) Near() int64 { return g.target - 10000 }

// Watch moves a free trip point of the package zone to Near and calls warm
// whenever the temperature crosses it, so nothing polls while the processor
// is cool. It reports false when the zone offers no such trip point.
func (g *Guard) Watch(warm func()) bool {
	if g.zone == "" || g.trip != nil {
		return g.trip != nil
	}
	t, err := ArmTrip(g.zone, g.Near())
	if err != nil {
		return false
	}
	g.trip = t
	go t.Run(context.Background(), warm)
	return true
}

// Watched reports whether a trip point wakes the guard.
func (g *Guard) Watched() bool { return g.trip != nil }

// Step reads the temperature once and adjusts the limit. It reports whether
// the processor currently runs below its full power.
func (g *Guard) Step() bool {
	t, ok := g.hottest()
	if !ok {
		return g.current < g.original
	}
	g.last = t
	next := g.current
	switch {
	case t >= g.target && g.current > g.floor:
		next = max(g.floor, g.current*85/100)
	case t <= g.target-g.hysteresis && g.current < g.original:
		next = min(g.original, g.current*110/100+1)
	}
	if next != g.current && g.write(next) {
		g.current = next
	}
	return g.current < g.original
}

// Restore gives the processor its full power back.
func (g *Guard) Restore() {
	if g.write(1000) {
		g.current = 1000
		g.original = 1000
	}
	if g.trip != nil {
		g.trip.Restore()
	}
}

func (g *Guard) Temperature() int64 { return g.last }

func (g *Guard) Share() int {
	return int(g.current / 10)
}
