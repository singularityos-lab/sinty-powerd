package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/sinty-powerd/internal/devices"
)

func TestEncoderHoldFollowsStreaming(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "sys/devices/platform/codec")
	os.MkdirAll(filepath.Join(dev, "power"), 0o755)
	os.WriteFile(filepath.Join(dev, "power/control"), []byte("auto\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "sys/class/video4linux/video11"), 0o755)
	os.Symlink(dev, filepath.Join(root, "sys/class/video4linux/video11/device"))
	s := &Service{cfg: Config{Root: root}, devs: devices.New(root)}
	if err := s.holdEncoder("/dev/video11"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dev, "power/control")); string(data) != "on" {
		t.Fatalf("encoder power while streaming: %q", data)
	}
	if err := s.holdEncoder("/dev/video12"); err == nil {
		t.Fatal("a missing video device was accepted")
	}
	s.holdEncoder("")
	if data, _ := os.ReadFile(filepath.Join(dev, "power/control")); string(data) != "auto" || s.encoder != "" {
		t.Fatalf("encoder power after streaming: %q", data)
	}
}

func TestVideoDevicePattern(t *testing.T) {
	for dev, ok := range map[string]bool{"/dev/video0": true, "/dev/video11": true, "/dev/sda": false, "/dev/video0/../sda": false, "video0": false} {
		if videoDevice.MatchString(dev) != ok {
			t.Errorf("%s", dev)
		}
	}
}
