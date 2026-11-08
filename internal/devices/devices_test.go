package devices

import (
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, root, path, v string) {
	t.Helper()
	p := filepath.Join(root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(v+"\n"), 0o644)
}

func get(t *testing.T, root, path string) string {
	t.Helper()
	return read(filepath.Join(root, path))
}

func link(t *testing.T, root, dev, driver string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers", driver), 0o755)
	os.Symlink(filepath.Join(root, "sys/bus/pci/drivers", driver), filepath.Join(root, "sys/bus/pci/devices", dev, "driver"))
}

func TestApplyAndRestore(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/bus/pci/devices/0000:01:00.0/power/control", "on")
	link(t, root, "0000:01:00.0", "nvme")
	put(t, root, "sys/bus/pci/devices/0000:02:00.0/power/control", "on")
	link(t, root, "0000:02:00.0", "nvidia")
	put(t, root, "sys/bus/pci/devices/0000:00:14.0/power/control", "auto")
	put(t, root, "sys/bus/usb/devices/1-1/power/control", "on")
	put(t, root, "sys/bus/usb/devices/1-1/1-1:1.0/bInterfaceClass", "0e")
	put(t, root, "sys/bus/usb/devices/1-2/power/control", "on")
	put(t, root, "sys/bus/usb/devices/1-2/1-2:1.0/bInterfaceClass", "03")
	put(t, root, "sys/module/snd_hda_intel/parameters/power_save", "0")

	m := New(root)
	m.Apply(true, true)
	for path, want := range map[string]string{
		"sys/bus/pci/devices/0000:01:00.0/power/control": "auto",
		"sys/bus/pci/devices/0000:02:00.0/power/control": "on",
		"sys/bus/pci/devices/0000:00:14.0/power/control": "auto",
		"sys/bus/usb/devices/1-1/power/control":          "auto",
		"sys/bus/usb/devices/1-2/power/control":          "on",
		"sys/module/snd_hda_intel/parameters/power_save": "1",
	} {
		if got := get(t, root, path); got != want {
			t.Errorf("saving: %s = %s, want %s", path, got, want)
		}
	}

	m.Apply(false, true)
	if got := get(t, root, "sys/bus/pci/devices/0000:01:00.0/power/control"); got != "on" {
		t.Errorf("runtime PM not given back: %s", got)
	}
	if got := get(t, root, "sys/module/snd_hda_intel/parameters/power_save"); got != "1" {
		t.Errorf("audio saving dropped with runtime PM: %s", got)
	}

	m.Apply(true, false)
	m.Restore()
	for path, want := range map[string]string{
		"sys/bus/pci/devices/0000:01:00.0/power/control": "on",
		"sys/bus/pci/devices/0000:00:14.0/power/control": "auto",
		"sys/bus/usb/devices/1-1/power/control":          "on",
		"sys/module/snd_hda_intel/parameters/power_save": "0",
	} {
		if got := get(t, root, path); got != want {
			t.Errorf("restored: %s = %s, want %s", path, got, want)
		}
	}
}

func TestAwakeListsDevicesKeptOn(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/bus/pci/devices/0000:01:00.0/power/control", "on")
	put(t, root, "sys/bus/pci/devices/0000:01:00.0/power/runtime_status", "active")
	link(t, root, "0000:01:00.0", "nvme")
	put(t, root, "sys/bus/pci/devices/0000:00:14.0/power/control", "auto")
	put(t, root, "sys/bus/pci/devices/0000:00:14.0/power/runtime_status", "suspended")
	got := New(root).Awake()
	if len(got) != 1 || got[0].Driver != "nvme" || got[0].Reason != "always-on" {
		t.Fatalf("awake %+v", got)
	}
}

func TestNvidiaGPUAlwaysSuspends(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/bus/pci/devices/0000:01:00.0/power/control", "on")
	put(t, root, "sys/bus/pci/devices/0000:01:00.0/vendor", "0x10de")
	put(t, root, "sys/bus/pci/devices/0000:01:00.0/class", "0x030000")
	link(t, root, "0000:01:00.0", "nvidia")
	put(t, root, "sys/bus/pci/devices/0000:01:00.1/power/control", "on")
	put(t, root, "sys/bus/pci/devices/0000:01:00.1/vendor", "0x10de")
	put(t, root, "sys/bus/pci/devices/0000:01:00.1/class", "0x040300")
	m := New(root)
	m.Apply(false, false)
	for _, d := range []string{"0000:01:00.0", "0000:01:00.1"} {
		if got := get(t, root, "sys/bus/pci/devices/"+d+"/power/control"); got != "auto" {
			t.Errorf("%s: %s", d, got)
		}
	}
	m.Restore()
	if got := get(t, root, "sys/bus/pci/devices/0000:01:00.0/power/control"); got != "auto" {
		t.Errorf("the GPU went back to always on: %s", got)
	}
}
