// Package devices lets idle PCI and USB devices and the HDA codec power down
// on their own. It only touches what the kernel left always on, skips drivers
// and device classes known to misbehave when suspended, and puts back every
// value it changed when saving is no longer wanted.
package devices

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

var skipDrivers = []string{"nvidia", "nouveau", "radeon", "amdgpu", "mei_me", "pcieport"}

const nvidiaVendor = "0x10de"

var savingKnobs = map[string]string{
	"proc/sys/vm/dirty_writeback_centisecs": "1500",
	"proc/sys/kernel/nmi_watchdog":          "0",
}

const (
	usbHID   = "03"
	usbAudio = "01"
)

type Manager struct {
	root     string
	changed  map[string]string
	held     string
	heldOrig string
}

func New(root string) *Manager {
	return &Manager{root: root, changed: map[string]string{}}
}

func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (m *Manager) pciCandidates() []string {
	var out []string
	dirs, _ := filepath.Glob(filepath.Join(m.root, "sys/bus/pci/devices/*"))
	for _, d := range dirs {
		driver := filepath.Base(readLink(filepath.Join(d, "driver")))
		if slices.Contains(skipDrivers, driver) {
			continue
		}
		out = append(out, filepath.Join(d, "power/control"))
	}
	return out
}

func readLink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return target
}

func (m *Manager) usbCandidates() []string {
	var out []string
	dirs, _ := filepath.Glob(filepath.Join(m.root, "sys/bus/usb/devices/*"))
	for _, d := range dirs {
		if strings.Contains(filepath.Base(d), ":") || read(filepath.Join(d, "bDeviceClass")) == "09" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "power/control")); err != nil {
			continue
		}
		keep := false
		ifaces, _ := filepath.Glob(filepath.Join(d, filepath.Base(d)+":*"))
		for _, i := range ifaces {
			switch read(filepath.Join(i, "bInterfaceClass")) {
			case usbHID, usbAudio:
				keep = true
			}
		}
		if !keep {
			out = append(out, filepath.Join(d, "power/control"))
		}
	}
	return out
}

func (m *Manager) audioKnobs() []string {
	var out []string
	for _, p := range []string{"sys/module/snd_hda_intel/parameters/power_save", "sys/module/snd_ac97_codec/parameters/power_save"} {
		path := filepath.Join(m.root, p)
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

func (m *Manager) set(path, value string) {
	cur := read(path)
	if cur == "" || cur == value {
		return
	}
	if os.WriteFile(path, []byte(value), 0) != nil {
		return
	}
	if _, ok := m.changed[path]; !ok {
		m.changed[path] = cur
	}
}

func (m *Manager) nvidiaGPUs() []string {
	var out []string
	dirs, _ := filepath.Glob(filepath.Join(m.root, "sys/bus/pci/devices/*"))
	for _, d := range dirs {
		if read(filepath.Join(d, "vendor")) != nvidiaVendor {
			continue
		}
		class := read(filepath.Join(d, "class"))
		if strings.HasPrefix(class, "0x0300") || strings.HasPrefix(class, "0x0302") || strings.HasPrefix(class, "0x0403") {
			out = append(out, filepath.Join(d, "power/control"))
		}
	}
	return out
}

func (m *Manager) radioWakeups() []string {
	var out []string
	adapters, _ := filepath.Glob(filepath.Join(m.root, "sys/class/bluetooth/hci*"))
	for _, a := range adapters {
		dev, err := filepath.EvalSymlinks(filepath.Join(a, "device"))
		if err != nil {
			continue
		}
		for _, d := range []string{dev, filepath.Dir(dev)} {
			path := filepath.Join(d, "power/wakeup")
			if read(path) != "" {
				out = append(out, path)
				break
			}
		}
	}
	return out
}

func (m *Manager) Hold(dir string) {
	if dir == m.held {
		return
	}
	if m.held != "" && m.heldOrig != "" {
		os.WriteFile(filepath.Join(m.held, "power/control"), []byte(m.heldOrig), 0)
	}
	m.held, m.heldOrig = dir, ""
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "power/control")
	if cur := read(path); cur != "" && cur != "on" {
		if os.WriteFile(path, []byte("on"), 0) == nil {
			m.heldOrig = cur
		}
	}
}

func (m *Manager) Held() string { return m.held }

func (m *Manager) Apply(runtimePM, audio, wakeOnRadio bool) {
	for _, p := range m.nvidiaGPUs() {
		if read(p) == "on" {
			os.WriteFile(p, []byte("auto"), 0)
		}
	}
	if runtimePM {
		for _, p := range append(m.pciCandidates(), m.usbCandidates()...) {
			if m.held != "" && p == filepath.Join(m.held, "power/control") {
				continue
			}
			if read(p) == "on" {
				m.set(p, "auto")
			}
		}
		for p, v := range savingKnobs {
			m.set(filepath.Join(m.root, p), v)
		}
	}
	if audio {
		for _, p := range m.audioKnobs() {
			if read(p) == "0" {
				m.set(p, "1")
			}
		}
	}
	if wakeOnRadio {
		for _, p := range m.radioWakeups() {
			m.set(p, "enabled")
		}
	}
	for path, orig := range m.changed {
		wanted := audio
		switch {
		case strings.HasSuffix(path, "power/control"), strings.HasPrefix(path, filepath.Join(m.root, "proc/sys")):
			wanted = runtimePM
		case strings.HasSuffix(path, "power/wakeup"):
			wanted = wakeOnRadio
		}
		if !wanted {
			if os.WriteFile(path, []byte(orig), 0) == nil || read(path) == "" {
				delete(m.changed, path)
			}
		}
	}
}

func (m *Manager) Restore() {
	m.Hold("")
	for path, orig := range m.changed {
		os.WriteFile(path, []byte(orig), 0)
		delete(m.changed, path)
	}
}

type Awake struct {
	Path   string
	Driver string
	Reason string
}

func (m *Manager) Awake() []Awake {
	var out []Awake
	dirs, _ := filepath.Glob(filepath.Join(m.root, "sys/bus/pci/devices/*"))
	for _, d := range dirs {
		if read(filepath.Join(d, "power/runtime_status")) != "active" {
			continue
		}
		reason := "busy"
		if read(filepath.Join(d, "power/control")) == "on" {
			reason = "always-on"
		}
		driver := "-"
		if l := readLink(filepath.Join(d, "driver")); l != "" {
			driver = filepath.Base(l)
		}
		out = append(out, Awake{Path: filepath.Base(d), Driver: driver, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
