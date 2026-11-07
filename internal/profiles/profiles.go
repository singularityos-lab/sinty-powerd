// Package profiles applies the power profiles power-saver, balanced and
// performance to the hardware: the ACPI platform profile and the energy
// performance preference of the CPU frequency driver.
package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	var want []string
	switch profile {
	case PowerSaver:
		want = []string{"power", "balance_power"}
	case Balanced:
		want = []string{"balance_performance", "default"}
		if onBattery {
			want = []string{"balance_power", "balance_performance", "default"}
		}
	case Performance:
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
		if len(b.eppFiles) > 0 && b.eppChoice(name, false) != "" {
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
	return errors.Join(errs...)
}
