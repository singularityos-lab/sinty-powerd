package gpumux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, root, path, value string) {
	t.Helper()
	p := filepath.Join(root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gpu(t *testing.T, root, slot, class, bootVGA, driver string) {
	t.Helper()
	dir := "sys/bus/pci/devices/" + slot + "/"
	put(t, root, dir+"class", class)
	put(t, root, dir+"boot_vga", bootVGA)
	if driver != "" {
		os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers", driver), 0o755)
		if err := os.Symlink("../../../../bus/pci/drivers/"+driver, filepath.Join(root, dir, "driver")); err != nil {
			t.Fatal(err)
		}
	}
}

const armoury = "sys/class/firmware-attributes/asus-armoury/attributes/gpu_mux_mode/current_value"

func TestDetectReadsTheBootedMode(t *testing.T) {
	root := t.TempDir()
	if Detect(root) != nil {
		t.Fatal("a mux was found on a machine without one")
	}
	put(t, root, armoury, "1")
	m := Detect(root)
	if m == nil || m.Current() != Hybrid || m.Booted() != Hybrid {
		t.Fatalf("mux %+v", m)
	}
	put(t, root, armoury, "0")
	if m.Current() != Dedicated || m.Booted() != Hybrid {
		t.Errorf("current %s booted %s", m.Current(), m.Booted())
	}
}

func TestLegacyAttributeIsFound(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sys/devices/platform/asus-nb-wmi/gpu_mux_mode", "0")
	if m := Detect(root); m == nil || m.Current() != Dedicated {
		t.Fatalf("mux %+v", m)
	}
}

func TestDedicatedNeedsADriverOnTheOtherGPU(t *testing.T) {
	root := t.TempDir()
	put(t, root, armoury, "1")
	gpu(t, root, "0000:00:02.0", "0x030000", "1", "i915")
	gpu(t, root, "0000:01:00.0", "0x030000", "0", "")
	m := Detect(root)
	if err := m.Set(Dedicated); !errors.Is(err, ErrNoDriver) {
		t.Fatalf("dedicated without a driver: %v", err)
	}
	if m.Current() != Hybrid {
		t.Fatalf("the mux moved to %s", m.Current())
	}
	gpu(t, root, "0000:01:00.1", "0x030200", "0", "vfio-pci")
	if err := m.Set(Dedicated); !errors.Is(err, ErrNoDriver) {
		t.Fatalf("dedicated with vfio only: %v", err)
	}
	os.RemoveAll(filepath.Join(root, "sys/bus/pci/devices/0000:01:00.0"))
	gpu(t, root, "0000:01:00.0", "0x030000", "0", "nvidia")
	if err := m.Set(Dedicated); err != nil {
		t.Fatal(err)
	}
	if m.Current() != Dedicated {
		t.Fatalf("mux %s after choosing dedicated", m.Current())
	}
	if err := m.Set(Hybrid); err != nil || m.Current() != Hybrid {
		t.Fatalf("back to hybrid: %v %s", err, m.Current())
	}
	if err := m.Set("integrated"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
