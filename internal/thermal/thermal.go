// Package thermal keeps the processor below its throttling temperature by
// lowering its sustained power limit while it runs hot and giving the power
// back once it has cooled, which is what thermald does for the package. The
// kernel still owns the hard trip points; this only acts before them, so the
// machine slows down gradually instead of hitting the firmware clamp.
package thermal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Guard watches one temperature and steers one power limit.
type Guard struct {
	temp       string
	limit      string
	original   uint64
	current    uint64
	floor      uint64
	target     int64
	hysteresis int64
}

func readInt(path string) (int64, bool) {
	data, err := os.ReadFile(path)
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
	var temp string
	critical := int64(0)
	for _, pref := range []string{"x86_pkg_temp", "TCPU", "acpitz"} {
		for _, z := range zones {
			data, _ := os.ReadFile(filepath.Join(z, "type"))
			if strings.TrimSpace(string(data)) != pref {
				continue
			}
			if temp == "" {
				temp = filepath.Join(z, "temp")
			}
		}
	}
	for _, z := range zones {
		trips, _ := filepath.Glob(filepath.Join(z, "trip_point_*_type"))
		for _, t := range trips {
			data, _ := os.ReadFile(t)
			if strings.TrimSpace(string(data)) != "critical" {
				continue
			}
			if v, ok := readInt(strings.TrimSuffix(t, "_type") + "_temp"); ok && v > 0 && (critical == 0 || v < critical) {
				critical = v
			}
		}
	}
	limit := filepath.Join(root, "sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw")
	original, ok := readInt(limit)
	if temp == "" || !ok || original <= 0 {
		return nil
	}
	target := int64(95000)
	if critical > 0 && critical-15000 < target {
		target = critical - 15000
	}
	return &Guard{
		temp:       temp,
		limit:      limit,
		original:   uint64(original),
		current:    uint64(original),
		floor:      uint64(original) * 4 / 10,
		target:     target,
		hysteresis: 8000,
	}
}

// Target is the temperature, in millidegrees, the guard keeps below.
func (g *Guard) Target() int64 { return g.target }

// Step reads the temperature once and adjusts the limit. It reports whether
// the processor currently runs below its full power.
func (g *Guard) Step() bool {
	t, ok := readInt(g.temp)
	if !ok {
		return g.current < g.original
	}
	next := g.current
	switch {
	case t >= g.target && g.current > g.floor:
		next = max(g.floor, g.current*85/100)
	case t <= g.target-g.hysteresis && g.current < g.original:
		next = min(g.original, g.current*110/100+1)
	}
	if next != g.current && os.WriteFile(g.limit, []byte(strconv.FormatUint(next, 10)), 0) == nil {
		g.current = next
	}
	return g.current < g.original
}

// Restore gives the processor its full power back.
func (g *Guard) Restore() {
	if g.current != g.original && os.WriteFile(g.limit, []byte(strconv.FormatUint(g.original, 10)), 0) == nil {
		g.current = g.original
	}
}
