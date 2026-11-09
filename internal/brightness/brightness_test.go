package brightness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRejectsJunk(t *testing.T) {
	got := Parse("abcdef0123456789 400 software\nbad key 1 x\neDP-1 2000 backlight\nDP-2 500 ddc\nx 1 laser\n")
	if len(got) != 2 || got["abcdef0123456789"].Level != 400 || got["DP-2"].Method != "ddc" {
		t.Fatalf("unexpected %v", got)
	}
}

func TestEdidIDMatchesCompositor(t *testing.T) {
	edid := make([]byte, 128)
	copy(edid[8:], []byte{0x4C, 0x2D, 0x34, 0x12, 0x01, 0x02, 0x03, 0x04, 0x10, 0x22})
	if got := EdidID(edid); got != "7a8f58b1ea549f22" {
		t.Fatalf("id %q", got)
	}
	if EdidID(edid[:10]) != "" {
		t.Fatal("short edid should have no id")
	}
}

func TestMergeAndRestore(t *testing.T) {
	root := t.TempDir()
	drm := filepath.Join(root, "sys/class/drm/card0-eDP-1")
	light := filepath.Join(root, "sys/class/backlight/intel_backlight")
	os.MkdirAll(drm, 0o755)
	os.MkdirAll(light, 0o755)
	edid := make([]byte, 128)
	copy(edid[8:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	os.WriteFile(filepath.Join(drm, "edid"), edid, 0o644)
	os.Symlink("../../drm/card0-eDP-1", filepath.Join(light, "device"))
	os.WriteFile(filepath.Join(light, "max_brightness"), []byte("19200\n"), 0o644)
	os.WriteFile(filepath.Join(light, "brightness"), []byte("19200"), 0o644)

	path := filepath.Join(root, "state/brightness")
	s := Open(path)
	if err := s.Merge(EdidID(edid) + " 250 backlight\nDP-3 700 software\n"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "DP-3 700 software") {
		t.Fatalf("file %q", data)
	}
	if n := Open(path).Restore(root); n != 1 {
		t.Fatalf("restored %d", n)
	}
	got, _ := os.ReadFile(filepath.Join(light, "brightness"))
	if string(got) != "4800" {
		t.Fatalf("brightness %q", got)
	}
}
