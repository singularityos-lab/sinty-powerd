package mirror

import (
	"os"
	"syscall"
	"unsafe"
)

type Kernel struct{}

func (Kernel) Int(name string) (int64, bool) {
	v, err := syscall.SysctlUint32(name)
	if err != nil {
		return 0, false
	}
	return int64(int32(v)), true
}

func (Kernel) Str(name string) (string, bool) {
	v, err := syscall.Sysctl(name)
	return v, err == nil
}

func mib(name string) ([]int32, error) {
	var out [24]int32
	n := uintptr(len(out) * 4)
	query := [2]int32{0, 3}
	p := []byte(name)
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&query[0])), 2,
		uintptr(unsafe.Pointer(&out[0])), uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)))
	if errno != 0 {
		return nil, errno
	}
	return out[:n/4], nil
}

func (Kernel) SetInt(name string, v int64) error {
	m, err := mib(name)
	if err != nil {
		return err
	}
	val := int32(v)
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&m[0])), uintptr(len(m)),
		0, 0, uintptr(unsafe.Pointer(&val)), 4)
	if errno != 0 {
		return errno
	}
	return nil
}

func (m *Mirror) Watch(failed func(path string, err error)) error {
	kq, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	paths := m.Writable()
	files := make([]*os.File, 0, len(paths))
	changes := make([]syscall.Kevent_t, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		files = append(files, f)
		var ev syscall.Kevent_t
		syscall.SetKevent(&ev, int(f.Fd()), syscall.EVFILT_VNODE, syscall.EV_ADD|syscall.EV_CLEAR)
		ev.Fflags = syscall.NOTE_WRITE | syscall.NOTE_EXTEND
		changes = append(changes, ev)
	}
	if _, err := syscall.Kevent(kq, changes, nil, nil); err != nil {
		syscall.Close(kq)
		return err
	}
	go func() {
		events := make([]syscall.Kevent_t, 8)
		for {
			n, err := syscall.Kevent(kq, nil, events, nil)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				return
			}
			for _, ev := range events[:n] {
				for _, f := range files {
					if uint64(f.Fd()) == uint64(ev.Ident) {
						if err := m.Apply(f.Name()); err != nil {
							failed(f.Name(), err)
						}
					}
				}
			}
		}
	}()
	return nil
}
