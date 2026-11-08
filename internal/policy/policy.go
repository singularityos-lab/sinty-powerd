// Package policy decides how the hardware should run from the state of the
// machine: the chosen profile, the power source, whether anybody is using
// the session and what kind of device this is. It knows no kernel
// interface; a backend turns its settings into knob writes.
package policy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
)

type Form string

const (
	Laptop   Form = "laptop"
	Desktop  Form = "desktop"
	Phone    Form = "phone"
	TV       Form = "tv"
	Headless Form = "headless"
)

func ParseForm(s string) (Form, bool) {
	switch f := Form(s); f {
	case Laptop, Desktop, Phone, TV, Headless:
		return f, true
	}
	return "", false
}

func DetectForm(root string) Form {
	data, _ := os.ReadFile(filepath.Join(root, "sys/class/dmi/id/chassis_type"))
	switch strings.TrimSpace(string(data)) {
	case "8", "9", "10", "11", "14", "30", "31", "32":
		return Laptop
	}
	if batteries, _ := filepath.Glob(filepath.Join(root, "sys/class/power_supply/BAT*")); len(batteries) > 0 {
		return Laptop
	}
	return Desktop
}

type Bias string

const (
	BiasPerformance        Bias = "performance"
	BiasBalancePerformance Bias = "balance-performance"
	BiasBalancePower       Bias = "balance-power"
	BiasPower              Bias = "power"
)

type State struct {
	Profile      string
	OnBattery    bool
	BatteryAware bool
	Idle         bool
	Form         Form
}

type Settings struct {
	Platform     string
	Bias         Bias
	Boost        bool
	RuntimePM    bool
	AudioSaving  bool
	PackageWatts float64
}

type Budget struct {
	PackageWatts float64
}

var Budgets = map[Form]Budget{
	Phone:    {PackageWatts: 6},
	Headless: {PackageWatts: 4},
}

func Decide(s State) Settings {
	out := Settings{Platform: s.Profile, Boost: true}
	switch s.Profile {
	case profiles.PowerSaver:
		out.Bias = BiasPower
		out.Boost = false
		out.RuntimePM = true
		out.AudioSaving = true
	case profiles.Performance:
		out.Bias = BiasPerformance
	default:
		out.Bias = BiasBalancePerformance
		if s.OnBattery && s.BatteryAware {
			out.Bias = BiasBalancePower
		}
		out.RuntimePM = s.OnBattery
		out.AudioSaving = s.OnBattery
	}
	if s.Profile != profiles.Performance && (s.Idle || s.Form == Headless) {
		out.Bias = BiasPower
		out.RuntimePM = true
		out.AudioSaving = true
	}
	if s.Form == Phone || s.Form == TV {
		out.RuntimePM = true
		out.AudioSaving = s.Form == Phone || s.OnBattery
	}
	if b, ok := Budgets[s.Form]; ok && s.Profile != profiles.Performance {
		out.PackageWatts = b.PackageWatts
	}
	return out
}
