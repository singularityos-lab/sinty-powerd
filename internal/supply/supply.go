// Package supply reads the power supplies the kernel exposes under
// /sys/class/power_supply and turns them into the device model of UPower:
// batteries, line power and the aggregate display device.
package supply

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kinds, as UPower numbers them.
const (
	KindUnknown   = 0
	KindLinePower = 1
	KindBattery   = 2
	KindUPS       = 3
	KindMouse     = 5
	KindKeyboard  = 6
	KindPhone     = 8
)

// States, as UPower numbers them.
const (
	StateUnknown       = 0
	StateCharging      = 1
	StateDischarging   = 2
	StateEmpty         = 3
	StateFullyCharged  = 4
	StatePendingCharge = 5
)

// Warning levels, as UPower numbers them.
const (
	WarningNone     = 1
	WarningLow      = 3
	WarningCritical = 4
	WarningAction   = 5
)

// Policy holds the battery thresholds that raise warnings.
type Policy struct {
	PercentageLow      float64
	PercentageCritical float64
	PercentageAction   float64
	ChargeStart        uint32
	ChargeEnd          uint32
}

// DefaultPolicy matches the defaults UPower ships.
var DefaultPolicy = Policy{PercentageLow: 20, PercentageCritical: 5, PercentageAction: 2, ChargeStart: 75, ChargeEnd: 80}

// Device is one UPower device with every property UPower publishes.
type Device struct {
	ID                               string
	NativePath                       string
	Vendor, Model, Serial            string
	UpdateTime                       uint64
	Type                             uint32
	PowerSupply                      bool
	HasHistory, HasStatistics        bool
	Online                           bool
	Energy, EnergyEmpty, EnergyFull  float64
	EnergyFullDesign, EnergyRate     float64
	Voltage                          float64
	ChargeCycles                     int32
	Luminosity                       float64
	TimeToEmpty, TimeToFull          int64
	Percentage, Temperature          float64
	IsPresent                        bool
	State                            uint32
	IsRechargeable                   bool
	Capacity                         float64
	Technology                       uint32
	WarningLevel, BatteryLevel       uint32
	IconName                         string
	ChargeStartThreshold             uint32
	ChargeEndThreshold               uint32
	ChargeThresholdEnabled           bool
	ChargeThresholdSupported         bool
	ChargeThresholdSettingsSupported uint32
	VoltageMinDesign                 float64
	VoltageMaxDesign                 float64
	CapacityLevel                    string
}

type attrs struct {
	dir  string
	vals map[string]string
}

func readAttrs(dir string) attrs {
	a := attrs{dir: dir, vals: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(dir, "uevent"))
	if err != nil {
		return a
	}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			a.vals[strings.TrimPrefix(k, "POWER_SUPPLY_")] = strings.TrimSpace(v)
		}
	}
	return a
}

func (a attrs) str(key string) string {
	if v, ok := a.vals[key]; ok {
		return v
	}
	data, err := os.ReadFile(filepath.Join(a.dir, strings.ToLower(key)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (a attrs) num(key string) (float64, bool) {
	v, err := strconv.ParseFloat(a.str(key), 64)
	return v, err == nil
}

func (a attrs) micro(key string) float64 {
	v, _ := a.num(key)
	return v / 1e6
}

func (a attrs) has(key string) bool {
	if _, ok := a.vals[key]; ok {
		return true
	}
	_, err := os.Stat(filepath.Join(a.dir, strings.ToLower(key)))
	return err == nil
}

func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// Scan reads every power supply under root that UPower would show.
func Scan(root string, policy Policy, thresholdOn map[string]bool) []*Device {
	entries, _ := os.ReadDir(root)
	var out []*Device
	now := uint64(time.Now().Unix())
	for _, e := range entries {
		a := readAttrs(filepath.Join(root, e.Name()))
		name := e.Name()
		typ := a.str("TYPE")
		scope := a.str("SCOPE")
		var d *Device
		switch {
		case typ == "Mains" || (typ == "USB" && scope != "Device"):
			d = linePower(name, a)
		case typ == "Battery" && scope != "Device":
			d = battery(name, a, policy, thresholdOn[name])
		case typ == "Battery" && scope == "Device":
			d = peripheral(name, a)
		default:
			continue
		}
		d.UpdateTime = now
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func base(native string) *Device {
	return &Device{NativePath: native, ChargeCycles: -1, WarningLevel: WarningNone, BatteryLevel: 1, ChargeEndThreshold: 100}
}

func linePower(name string, a attrs) *Device {
	d := base(name)
	d.ID = "line_power_" + sanitize(name)
	d.Type = KindLinePower
	d.PowerSupply = true
	d.Online = a.str("ONLINE") == "1"
	d.IconName = "ac-adapter-symbolic"
	return d
}

var technologies = map[string]uint32{"Li-ion": 1, "Li-poly": 2, "LiFe": 3, "Pb": 4, "NiCd": 5, "NiMH": 6}

func battery(name string, a attrs, policy Policy, thresholdOn bool) *Device {
	d := base(name)
	d.ID = "battery_" + sanitize(name)
	d.Type = KindBattery
	d.PowerSupply = true
	d.HasHistory, d.HasStatistics = true, true
	d.Vendor = a.str("MANUFACTURER")
	d.Model = a.str("MODEL_NAME")
	d.Serial = strings.TrimSpace(a.str("SERIAL_NUMBER"))
	d.IsPresent = a.str("PRESENT") != "0"
	d.IsRechargeable = true
	d.Technology = technologies[a.str("TECHNOLOGY")]
	d.CapacityLevel = a.str("CAPACITY_LEVEL")
	if v, ok := a.num("CYCLE_COUNT"); ok {
		d.ChargeCycles = int32(v)
	}
	d.VoltageMinDesign = a.micro("VOLTAGE_MIN_DESIGN")
	d.VoltageMaxDesign = a.micro("VOLTAGE_MAX_DESIGN")
	d.Voltage = a.micro("VOLTAGE_NOW")
	if v, ok := a.num("TEMP"); ok {
		d.Temperature = v / 10
	}
	if !d.IsPresent {
		d.IconName = "battery-missing-symbolic"
		return d
	}

	designVoltage := d.VoltageMinDesign
	if designVoltage == 0 {
		designVoltage = d.VoltageMaxDesign
	}
	if designVoltage == 0 {
		designVoltage = d.Voltage
	}
	if a.has("ENERGY_NOW") || a.has("ENERGY_FULL") {
		d.Energy = a.micro("ENERGY_NOW")
		d.EnergyFull = a.micro("ENERGY_FULL")
		d.EnergyFullDesign = a.micro("ENERGY_FULL_DESIGN")
	} else {
		d.Energy = a.micro("CHARGE_NOW") * designVoltage
		d.EnergyFull = a.micro("CHARGE_FULL") * designVoltage
		d.EnergyFullDesign = a.micro("CHARGE_FULL_DESIGN") * designVoltage
	}
	if d.EnergyFull == 0 {
		d.EnergyFull = d.EnergyFullDesign
	}
	if d.Energy > d.EnergyFull && d.EnergyFull > 0 {
		d.EnergyFull = d.Energy
	}
	if a.has("POWER_NOW") {
		d.EnergyRate = math.Abs(a.micro("POWER_NOW"))
	} else if a.has("CURRENT_NOW") {
		v := d.Voltage
		if v == 0 {
			v = designVoltage
		}
		d.EnergyRate = math.Abs(a.micro("CURRENT_NOW") * v)
	}
	if p, ok := a.num("CAPACITY"); ok && p >= 0 && p <= 100 {
		d.Percentage = p
	} else if d.EnergyFull > 0 {
		d.Percentage = d.Energy / d.EnergyFull * 100
	}
	d.Percentage = math.Max(0, math.Min(100, d.Percentage))
	if d.EnergyFullDesign > 0 && d.EnergyFull > 0 {
		d.Capacity = math.Min(100, d.EnergyFull/d.EnergyFullDesign*100)
	}

	switch a.str("STATUS") {
	case "Charging":
		d.State = StateCharging
	case "Discharging":
		d.State = StateDischarging
	case "Full":
		d.State = StateFullyCharged
	case "Not charging":
		d.State = StatePendingCharge
		if d.Percentage >= 99 {
			d.State = StateFullyCharged
		}
	default:
		d.State = StateUnknown
		if d.Percentage >= 99 {
			d.State = StateFullyCharged
		}
	}
	if d.State == StateDischarging && d.Percentage == 0 {
		d.State = StateEmpty
	}
	estimate(d)
	d.WarningLevel = warning(d, policy)

	start, end := a.has("CHARGE_CONTROL_START_THRESHOLD"), a.has("CHARGE_CONTROL_END_THRESHOLD")
	if start {
		d.ChargeThresholdSettingsSupported |= 1
	}
	if end {
		d.ChargeThresholdSettingsSupported |= 2
	}
	d.ChargeThresholdSupported = start || end
	if d.ChargeThresholdSupported {
		d.ChargeStartThreshold, d.ChargeEndThreshold = policy.ChargeStart, policy.ChargeEnd
		d.ChargeThresholdEnabled = thresholdOn
	}
	d.IconName = icon(d)
	return d
}

func peripheral(name string, a attrs) *Device {
	d := base(name)
	d.ID = "battery_" + sanitize(name)
	d.Type = KindBattery
	d.Model = a.str("MODEL_NAME")
	d.Vendor = a.str("MANUFACTURER")
	d.Serial = a.str("SERIAL_NUMBER")
	d.IsPresent = a.str("PRESENT") != "0"
	d.IsRechargeable = true
	lower := strings.ToLower(d.Model)
	switch {
	case strings.Contains(lower, "mouse"):
		d.Type = KindMouse
	case strings.Contains(lower, "keyboard"):
		d.Type = KindKeyboard
	}
	if p, ok := a.num("CAPACITY"); ok {
		d.Percentage = math.Max(0, math.Min(100, p))
	}
	switch a.str("STATUS") {
	case "Charging":
		d.State = StateCharging
	case "Discharging":
		d.State = StateDischarging
	case "Full":
		d.State = StateFullyCharged
	}
	d.IconName = icon(d)
	return d
}

const maxEstimate = 240 * 3600

func estimate(d *Device) {
	d.TimeToEmpty, d.TimeToFull = 0, 0
	if d.EnergyRate < 0.01 {
		return
	}
	switch d.State {
	case StateDischarging:
		if t := int64(math.Round(d.Energy / d.EnergyRate * 3600)); t > 0 && t < maxEstimate {
			d.TimeToEmpty = t
		}
	case StateCharging:
		if t := int64(math.Round((d.EnergyFull - d.Energy) / d.EnergyRate * 3600)); t > 0 && t < maxEstimate {
			d.TimeToFull = t
		}
	}
}

func warning(d *Device, p Policy) uint32 {
	if d.State != StateDischarging && d.State != StateEmpty {
		return WarningNone
	}
	switch {
	case d.Percentage <= p.PercentageAction:
		return WarningAction
	case d.Percentage <= p.PercentageCritical:
		return WarningCritical
	case d.Percentage <= p.PercentageLow:
		return WarningLow
	}
	return WarningNone
}

func icon(d *Device) string {
	if !d.IsPresent {
		return "battery-missing-symbolic"
	}
	if d.State == StateFullyCharged {
		return "battery-full-charged-symbolic"
	}
	level := "full"
	switch {
	case d.Percentage < 10:
		level = "caution"
	case d.Percentage < 30:
		level = "low"
	case d.Percentage < 60:
		level = "good"
	}
	if d.State == StateCharging || d.State == StatePendingCharge {
		return "battery-" + level + "-charging-symbolic"
	}
	return "battery-" + level + "-symbolic"
}

// Display combines the system batteries into the device the desktop shows.
func Display(devices []*Device, policy Policy) *Device {
	d := base("")
	d.ID = "DisplayDevice"
	d.Type = KindBattery
	d.PowerSupply = true
	d.ChargeCycles = 0
	d.ChargeEndThreshold = 0
	d.UpdateTime = uint64(time.Now().Unix())
	charging, discharging, full, n := false, false, 0, 0
	for _, b := range devices {
		if b.Type != KindBattery || !b.PowerSupply || !b.IsPresent {
			continue
		}
		n++
		d.Energy += b.Energy
		d.EnergyFull += b.EnergyFull
		d.EnergyRate += b.EnergyRate
		switch b.State {
		case StateCharging:
			charging = true
		case StateDischarging, StateEmpty:
			discharging = true
		case StateFullyCharged:
			full++
		}
		if n == 1 {
			d.Percentage = b.Percentage
		}
	}
	if n == 0 {
		d.Type = KindUnknown
		d.PowerSupply = false
		d.IconName = ""
		return d
	}
	d.IsPresent = true
	if n > 1 && d.EnergyFull > 0 {
		d.Percentage = d.Energy / d.EnergyFull * 100
	}
	switch {
	case charging:
		d.State = StateCharging
	case discharging:
		d.State = StateDischarging
	case full == n:
		d.State = StateFullyCharged
	default:
		d.State = StatePendingCharge
	}
	estimate(d)
	d.WarningLevel = warning(d, policy)
	d.IconName = icon(d)
	return d
}

// OnBattery reports whether the system runs from its batteries: no line
// power is online and a system battery is present.
func OnBattery(devices []*Device) bool {
	battery := false
	for _, d := range devices {
		if d.Type == KindLinePower && d.Online {
			return false
		}
		if d.Type == KindBattery && d.PowerSupply && d.IsPresent {
			battery = true
		}
	}
	return battery
}
