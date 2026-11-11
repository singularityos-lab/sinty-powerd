// Package profiles applies the power profiles power-saver, balanced and
// performance to the hardware: the ACPI platform profile and the energy
// performance preference of the CPU frequency driver.
package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	PowerSaver  = "power-saver"
	Balanced    = "balanced"
	Performance = "performance"
)

// Backend is the set of knobs found on this machine.
type Backend struct {
	root       string
	platform   []string
	cpuDriver  string
	eppFiles   []string
	eppChoices []string
	boost      string
	boostOn    string
	governors  []string
	eas        string
	clamp      string
	devfreq    []string
}

func (b *Backend) path(p string) string { return filepath.Join(b.root, p) }

func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Detect looks for the knobs under root, which is "/" on a running system.
func Detect(root string) *Backend {
	b := &Backend{root: root}
	if choices := read(b.path("sys/firmware/acpi/platform_profile_choices")); choices != "" {
		b.platform = strings.Fields(choices)
	}
	files, _ := filepath.Glob(b.path("sys/devices/system/cpu/cpufreq/policy*/energy_performance_preference"))
	if len(files) > 0 {
		b.eppFiles = files
		b.eppChoices = strings.Fields(read(filepath.Join(filepath.Dir(files[0]), "energy_performance_available_preferences")))
		b.cpuDriver = read(filepath.Join(filepath.Dir(files[0]), "scaling_driver"))
		if b.cpuDriver == "" {
			b.cpuDriver = "intel_pstate"
		}
	}
	if len(b.eppFiles) == 0 {
		files, _ := filepath.Glob(b.path("sys/devices/system/cpu/cpufreq/policy*/scaling_available_governors"))
		for _, f := range files {
			if slices.Contains(strings.Fields(read(f)), "schedutil") {
				b.governors = append(b.governors, filepath.Join(filepath.Dir(f), "scaling_governor"))
			}
		}
		if len(b.governors) > 0 {
			b.cpuDriver = read(filepath.Join(filepath.Dir(b.governors[0]), "scaling_driver"))
			if b.cpuDriver == "" {
				b.cpuDriver = "cpufreq"
			}
			if f := b.path("proc/sys/kernel/sched_energy_aware"); read(f) != "" {
				b.eas = f
			}
			if f := b.path("proc/sys/kernel/sched_util_clamp_max"); read(f) != "" {
				b.clamp = f
			}
			dirs, _ := filepath.Glob(b.path("sys/class/devfreq/*"))
			for _, d := range dirs {
				if read(filepath.Join(d, "available_governors")) != "" {
					b.devfreq = append(b.devfreq, d)
				}
			}
		}
	}
	if f := b.path("sys/devices/system/cpu/intel_pstate/no_turbo"); read(f) != "" {
		b.boost, b.boostOn = f, "0"
	} else if f := b.path("sys/devices/system/cpu/cpufreq/boost"); read(f) != "" {
		b.boost, b.boostOn = f, "1"
	}
	return b
}

func (b *Backend) platformChoice(profile string) string {
	var want []string
	switch profile {
	case PowerSaver:
		want = []string{"low-power", "quiet", "cool"}
	case Balanced:
		want = []string{"balanced"}
	case Performance:
		want = []string{"performance"}
	}
	for _, w := range want {
		if slices.Contains(b.platform, w) {
			return w
		}
	}
	return ""
}

func (b *Backend) eppChoice(profile string, onBattery bool) string {
	switch profile {
	case PowerSaver:
		return b.eppFor("power")
	case Performance:
		return b.eppFor("performance")
	case Balanced:
		if onBattery {
			return b.eppFor("balance-power")
		}
		return b.eppFor("balance-performance")
	}
	return ""
}

func (b *Backend) eppFor(bias string) string {
	var want []string
	switch bias {
	case "power":
		want = []string{"power", "balance_power"}
	case "balance-power":
		want = []string{"balance_power", "balance_performance", "default"}
	case "balance-performance":
		want = []string{"balance_performance", "default"}
	case "performance":
		want = []string{"performance"}
	}
	for _, w := range want {
		if len(b.eppChoices) == 0 || slices.Contains(b.eppChoices, w) {
			return w
		}
	}
	return ""
}

// Profile describes one available profile the way power-profiles-daemon
// lists it.
type Profile struct {
	Name           string
	CPUDriver      string
	PlatformDriver string
	Driver         string
}

// Profiles lists the profiles this machine supports. Power saver and
// balanced always exist; performance needs a driver that offers it.
func (b *Backend) Profiles() []Profile {
	var out []Profile
	for _, name := range []string{PowerSaver, Balanced, Performance} {
		p := Profile{Name: name}
		if b.platformChoice(name) != "" {
			p.PlatformDriver = "platform_profile"
		}
		if len(b.eppFiles) > 0 && b.eppChoice(name, false) != "" || len(b.governors) > 0 {
			p.CPUDriver = b.cpuDriver
		}
		switch {
		case p.PlatformDriver != "" && p.CPUDriver != "":
			p.Driver = "multiple"
		case p.PlatformDriver != "":
			p.Driver = p.PlatformDriver
		case p.CPUDriver != "":
			p.Driver = p.CPUDriver
		default:
			if name == Performance {
				continue
			}
			p.Driver = "placeholder"
		}
		out = append(out, p)
	}
	return out
}

// Supports reports whether profile is among Profiles.
func (b *Backend) Supports(profile string) bool {
	for _, p := range b.Profiles() {
		if p.Name == profile {
			return true
		}
	}
	return false
}

// Apply writes profile to every knob that has a setting for it.
func (b *Backend) Apply(profile string, onBattery bool) error {
	if !b.Supports(profile) {
		return errors.New("profile not available: " + profile)
	}
	var errs []error
	if c := b.platformChoice(profile); c != "" {
		if err := os.WriteFile(b.path("sys/firmware/acpi/platform_profile"), []byte(c), 0); err != nil {
			errs = append(errs, err)
		}
	}
	if c := b.eppChoice(profile, onBattery); c != "" {
		for _, f := range b.eppFiles {
			if err := os.WriteFile(f, []byte(c), 0); err != nil {
				errs = append(errs, err)
			}
		}
	}
	bias := map[string]string{PowerSaver: "power", Balanced: "balance-performance", Performance: "performance"}[profile]
	if profile == Balanced && onBattery {
		bias = "balance-power"
	}
	errs = append(errs, b.applyScheduler(bias))
	return errors.Join(errs...)
}

const fullClamp = 1024

func writeIfChanged(path, value string) error {
	if read(path) == value {
		return nil
	}
	return os.WriteFile(path, []byte(value), 0)
}

func (b *Backend) applyScheduler(bias string) error {
	if len(b.governors) == 0 {
		return nil
	}
	var errs []error
	for _, f := range b.governors {
		errs = append(errs, writeIfChanged(f, "schedutil"))
	}
	if b.eas != "" {
		v := "1"
		if bias == "performance" {
			v = "0"
		}
		errs = append(errs, writeIfChanged(b.eas, v))
	}
	if b.clamp != "" {
		clamp := fullClamp
		switch bias {
		case "power":
			clamp = fullClamp * 5 / 8
		case "balance-power":
			clamp = fullClamp * 7 / 8
		}
		errs = append(errs, writeIfChanged(b.clamp, strconv.Itoa(clamp)))
	}
	for _, d := range b.devfreq {
		choices := strings.Fields(read(filepath.Join(d, "available_governors")))
		want := []string{"simple_ondemand"}
		if bias == "performance" {
			want = []string{"performance", "simple_ondemand"}
		}
		for _, w := range want {
			if slices.Contains(choices, w) {
				errs = append(errs, writeIfChanged(filepath.Join(d, "governor"), w))
				break
			}
		}
	}
	return errors.Join(errs...)
}

func (b *Backend) ApplySettings(platform, bias string, boost bool) error {
	if !b.Supports(platform) {
		return errors.New("profile not available: " + platform)
	}
	var errs []error
	if c := b.platformChoice(platform); c != "" {
		if err := os.WriteFile(b.path("sys/firmware/acpi/platform_profile"), []byte(c), 0); err != nil {
			errs = append(errs, err)
		}
	}
	if c := b.eppFor(bias); c != "" {
		for _, f := range b.eppFiles {
			if err := os.WriteFile(f, []byte(c), 0); err != nil {
				errs = append(errs, err)
			}
		}
	}
	errs = append(errs, b.applyScheduler(bias))
	if b.boost != "" {
		v := b.boostOn
		if !boost {
			v = map[string]string{"0": "1", "1": "0"}[b.boostOn]
		}
		if read(b.boost) != v {
			if err := os.WriteFile(b.boost, []byte(v), 0); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
