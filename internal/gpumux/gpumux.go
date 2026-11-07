package gpumux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	Hybrid    = "hybrid"
	Dedicated = "dedicated"
)

var ErrNoDriver = errors.New("the dedicated GPU has no working driver, the screen would stay dark after the restart")

var candidates = []string{
	"sys/class/firmware-attributes/asus-armoury/attributes/gpu_mux_mode/current_value",
	"sys/devices/platform/asus-nb-wmi/gpu_mux_mode",
}

type Mux struct {
	root   string
	path   string
	booted string
}

func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func Detect(root string) *Mux {
	for _, c := range candidates {
		path := filepath.Join(root, c)
		if _, err := os.Stat(path); err == nil {
			m := &Mux{root: root, path: path}
			m.booted = m.Current()
			if m.booted == "" {
				return nil
			}
			return m
		}
	}
	return nil
}

func (m *Mux) Current() string {
	switch read(m.path) {
	case "0":
		return Dedicated
	case "1":
		return Hybrid
	}
	return ""
}

func (m *Mux) Booted() string { return m.booted }

func (m *Mux) Set(mode string) error {
	value := ""
	switch mode {
	case Hybrid:
		value = "1"
	case Dedicated:
		if !DedicatedReady(m.root) {
			return ErrNoDriver
		}
		value = "0"
	default:
		return errors.New("unknown GPU mode " + mode)
	}
	return os.WriteFile(m.path, []byte(value), 0)
}

func DedicatedReady(root string) bool {
	devices, _ := filepath.Glob(filepath.Join(root, "sys/bus/pci/devices/*"))
	for _, d := range devices {
		class := read(filepath.Join(d, "class"))
		if !strings.HasPrefix(class, "0x0300") && !strings.HasPrefix(class, "0x0302") {
			continue
		}
		if read(filepath.Join(d, "boot_vga")) == "1" {
			continue
		}
		link, err := os.Readlink(filepath.Join(d, "driver"))
		if err != nil {
			continue
		}
		switch filepath.Base(link) {
		case "vfio-pci", "pci-stub":
			continue
		}
		return true
	}
	return false
}
