// Package events watches the kernel for what changes power state: power
// supply uevents and the lid switch.
package events

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// PowerSupplyChanges calls changed for every uevent of the power_supply
// subsystem. It returns when the uevent socket cannot be opened.
func PowerSupplyChanges(changed func()) error {
	return uevents(func(ev []byte) bool {
		return bytes.Contains(ev, []byte("\x00SUBSYSTEM=power_supply\x00"))
	}, changed)
}

func DevicesAdded(added func()) error {
	return uevents(func(ev []byte) bool {
		return bytes.HasPrefix(ev, []byte("add@")) &&
			(bytes.Contains(ev, []byte("\x00SUBSYSTEM=pci\x00")) ||
				bytes.Contains(ev, []byte("\x00SUBSYSTEM=usb\x00")) && bytes.Contains(ev, []byte("\x00DEVTYPE=usb_device\x00")))
	}, added)
}

func uevents(match func([]byte) bool, cb func()) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: 1}); err != nil {
		syscall.Close(fd)
		return err
	}
	go func() {
		defer syscall.Close(fd)
		buf := make([]byte, 16384)
		for {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				return
			}
			if match(buf[:n]) {
				cb()
			}
		}
	}()
	return nil
}

const (
	evSW      = 0x05
	swLID     = 0x00
	eventSize = 24
	iocRead   = 2
)

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
		go func() {
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
					if typ == evSW && code == swLID {
						changed(value != 0)
					}
				}
			}
		}()
		return true
	}
	return false
}
