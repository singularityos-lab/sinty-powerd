// Package events watches the kernel for what changes power state: power
// supply uevents and the lid switch.
package events

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/singularityos-lab/libsingularity/src/system/core/go/uevent"
)

// Watch calls supply for every power supply event and added when a PCI or
// USB device appears. Other device events do not wake the daemon.
func Watch(supply, added func()) error {
	m, err := uevent.Open(
		uevent.Match{Subsystem: "power_supply"},
		uevent.Match{Subsystem: "pci"},
		uevent.Match{Subsystem: "usb", DevType: "usb_device"},
	)
	if err != nil {
		return err
	}
	go m.Run(context.Background(), func(ev uevent.Event, err error) {
		switch {
		case err != nil:
			supply()
			added()
		case ev.Subsystem() == "power_supply":
			supply()
		case ev.Action() == "add":
			added()
		}
	})
	return nil
}

const (
	evSW        = 0x05
	swLID       = 0x00
	swHeadphone = 0x02
	swLineOut   = 0x06
	swVideoOut  = 0x08
	eventSize   = 24
	iocRead     = 2
)

func hasBit(bits []byte, code int) bool {
	return bits[code/8]&(1<<(code%8)) != 0
}

func eviocgbit(ev, size uintptr) uintptr {
	return iocRead<<30 | size<<16 | 'E'<<8 | (0x20 + ev)
}

func eviocgsw(size uintptr) uintptr {
	return iocRead<<30 | size<<16 | 'E'<<8 | 0x1b
}

func ioctl(fd, req uintptr, buf []byte) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return errno
	}
	return nil
}

// Lid finds the input device that reports the lid switch. It calls changed
// with the current state, then on every change. It returns false when the
// machine has no lid switch.
func Lid(changed func(closed bool)) bool {
	nodes, _ := filepath.Glob("/dev/input/event*")
	for _, node := range nodes {
		f, err := os.OpenFile(node, os.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		bits := make([]byte, 8)
		if ioctl(f.Fd(), eviocgbit(evSW, uintptr(len(bits))), bits) != nil || bits[0]&(1<<swLID) == 0 {
			f.Close()
			continue
		}
		state := make([]byte, 8)
		if ioctl(f.Fd(), eviocgsw(uintptr(len(state))), state) == nil {
			changed(state[0]&(1<<swLID) != 0)
		}
		go switchEvents(f, func(code uint16, on bool) {
			if code == swLID {
				changed(on)
			}
		})
		return true
	}
	return false
}

func switchEvents(f *os.File, on func(code uint16, value bool)) {
	defer f.Close()
	buf := make([]byte, eventSize*16)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for i := 0; i+eventSize <= n; i += eventSize {
			typ := binary.LittleEndian.Uint16(buf[i+16:])
			code := binary.LittleEndian.Uint16(buf[i+18:])
			value := int32(binary.LittleEndian.Uint32(buf[i+20:]))
			if typ == evSW {
				on(code, value != 0)
			}
		}
	}
}

func AnalogJacks(changed func(plugged bool)) {
	var mu sync.Mutex
	jacks := map[string]map[uint16]bool{}
	report := func(node string, code uint16, on bool) {
		mu.Lock()
		defer mu.Unlock()
		jacks[node][code] = on
		plugged := false
		for _, codes := range jacks {
			for _, v := range codes {
				plugged = plugged || v
			}
		}
		changed(plugged)
	}
	nodes, _ := filepath.Glob("/dev/input/event*")
	for _, node := range nodes {
		f, err := os.OpenFile(node, os.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		bits := make([]byte, 8)
		if ioctl(f.Fd(), eviocgbit(evSW, uintptr(len(bits))), bits) != nil || hasBit(bits, swVideoOut) ||
			!(hasBit(bits, swHeadphone) || hasBit(bits, swLineOut)) {
			f.Close()
			continue
		}
		state := make([]byte, 8)
		ioctl(f.Fd(), eviocgsw(uintptr(len(state))), state)
		mu.Lock()
		jacks[node] = map[uint16]bool{}
		mu.Unlock()
		for _, code := range []uint16{swHeadphone, swLineOut} {
			if hasBit(bits, int(code)) {
				report(node, code, hasBit(state, int(code)))
			}
		}
		go switchEvents(f, func(code uint16, on bool) {
			if code == swHeadphone || code == swLineOut {
				report(node, code, on)
			}
		})
	}
}
