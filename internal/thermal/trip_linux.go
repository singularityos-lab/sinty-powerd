package thermal

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/singularityos-lab/libsingularity/src/system/core/go/kernfs"
)

const (
	genlFamily     = "thermal"
	genlEventGroup = "event"
	eventTripUp    = 5
	eventTripDown  = 6
	attrZoneID     = 2
	nlaHeader      = 4
	genlHeader     = 4
)

// Trip holds a writable trip point of a thermal zone at a chosen temperature
// and reports every crossing of it, so the daemon sleeps until the processor
// actually warms up.
type Trip struct {
	zone  uint32
	path  string
	saved string
	fd    int
}

// ArmTrip sets a writable passive trip point of zoneDir to at, in
// millidegrees, and subscribes to the kernel thermal events.
func ArmTrip(zoneDir string, at int64) (*Trip, error) {
	zone, err := strconv.ParseUint(strings.TrimPrefix(filepath.Base(zoneDir), "thermal_zone"), 10, 32)
	if err != nil {
		return nil, err
	}
	path := writableTrip(zoneDir)
	if path == "" {
		return nil, errors.New("no writable trip point")
	}
	saved, err := kernfs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fd, err := subscribe()
	if err != nil {
		return nil, err
	}
	t := &Trip{zone: uint32(zone), path: path, saved: strings.TrimSpace(string(saved)), fd: fd}
	if err := t.Move(at); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return t, nil
}

func writableTrip(zoneDir string) string {
	if cdevs, _ := filepath.Glob(filepath.Join(zoneDir, "cdev*")); len(cdevs) > 0 {
		return ""
	}
	types, _ := filepath.Glob(filepath.Join(zoneDir, "trip_point_*_type"))
	for _, p := range types {
		kind, err := kernfs.ReadFile(p)
		if err != nil || strings.TrimSpace(string(kind)) != "passive" {
			continue
		}
		temp := strings.TrimSuffix(p, "_type") + "_temp"
		if unix.Access(temp, unix.W_OK) == nil {
			return temp
		}
	}
	return ""
}

// Move places the trip point at a new temperature.
func (t *Trip) Move(at int64) error {
	return os.WriteFile(t.path, []byte(strconv.FormatInt(at, 10)), 0)
}

// Restore gives the trip point back its original temperature.
func (t *Trip) Restore() {
	os.WriteFile(t.path, []byte(t.saved), 0)
}

// Run calls crossed for every crossing of a trip point of the zone, and when
// events were lost, until ctx ends.
func (t *Trip) Run(ctx context.Context, crossed func()) error {
	defer unix.Close(t.fd)
	stop, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return err
	}
	defer unix.Close(stop)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			one := [8]byte{1}
			_, _ = unix.Write(stop, one[:])
		case <-done:
		}
	}()
	buf := make([]byte, 16<<10)
	fds := []unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}, {Fd: int32(stop), Events: unix.POLLIN}}
	for ctx.Err() == nil {
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if fds[1].Revents != 0 {
			break
		}
		for {
			n, _, err := unix.Recvfrom(t.fd, buf, 0)
			if err == unix.EAGAIN || err == unix.EINTR {
				break
			}
			if err == unix.ENOBUFS {
				crossed()
				continue
			}
			if err != nil {
				return err
			}
			if tripCrossed(buf[:n], t.zone) {
				crossed()
			}
		}
	}
	return ctx.Err()
}

func tripCrossed(msg []byte, zone uint32) bool {
	for len(msg) >= unix.NLMSG_HDRLEN {
		size := int(binary.NativeEndian.Uint32(msg))
		if size < unix.NLMSG_HDRLEN || size > len(msg) {
			return false
		}
		body := msg[unix.NLMSG_HDRLEN:size]
		if len(body) >= genlHeader && (body[0] == eventTripUp || body[0] == eventTripDown) {
			if id, ok := attrU32(body[genlHeader:], attrZoneID); ok && id == zone {
				return true
			}
		}
		msg = msg[align(size):]
	}
	return false
}

func subscribe() (int, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_GENERIC)
	if err != nil {
		return -1, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return -1, err
	}
	group, err := eventGroup(fd)
	if err == nil {
		err = unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_ADD_MEMBERSHIP, int(group))
	}
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	if err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func eventGroup(fd int) (uint32, error) {
	name := append([]byte(genlFamily), 0)
	attr := make([]byte, align(nlaHeader+len(name)))
	binary.NativeEndian.PutUint16(attr, uint16(nlaHeader+len(name)))
	binary.NativeEndian.PutUint16(attr[2:], unix.CTRL_ATTR_FAMILY_NAME)
	copy(attr[nlaHeader:], name)
	req := make([]byte, unix.NLMSG_HDRLEN+genlHeader, unix.NLMSG_HDRLEN+genlHeader+len(attr))
	req = append(req, attr...)
	binary.NativeEndian.PutUint32(req, uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:], unix.GENL_ID_CTRL)
	binary.NativeEndian.PutUint16(req[6:], unix.NLM_F_REQUEST)
	binary.NativeEndian.PutUint32(req[8:], 1)
	req[unix.NLMSG_HDRLEN] = unix.CTRL_CMD_GETFAMILY
	req[unix.NLMSG_HDRLEN+1] = 2
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, err
	}
	buf := make([]byte, 8<<10)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return 0, err
	}
	return groupFromReply(buf[:n])
}

func groupFromReply(msg []byte) (uint32, error) {
	if len(msg) < unix.NLMSG_HDRLEN+genlHeader {
		return 0, errors.New("short reply from the generic netlink controller")
	}
	if binary.NativeEndian.Uint16(msg[4:]) == unix.NLMSG_ERROR {
		return 0, errors.New("the kernel has no thermal netlink family")
	}
	size := min(int(binary.NativeEndian.Uint32(msg)), len(msg))
	groups, ok := attr(msg[unix.NLMSG_HDRLEN+genlHeader:size], unix.CTRL_ATTR_MCAST_GROUPS)
	if !ok {
		return 0, errors.New("the thermal family has no multicast groups")
	}
	for len(groups) >= nlaHeader {
		l := int(binary.NativeEndian.Uint16(groups))
		if l < nlaHeader || l > len(groups) {
			break
		}
		g := groups[nlaHeader:l]
		name, _ := attr(g, unix.CTRL_ATTR_MCAST_GRP_NAME)
		if strings.TrimRight(string(name), "\x00") == genlEventGroup {
			if id, ok := attrU32(g, unix.CTRL_ATTR_MCAST_GRP_ID); ok {
				return id, nil
			}
		}
		groups = groups[min(align(l), len(groups)):]
	}
	return 0, errors.New("the thermal family has no event group")
}

func attr(b []byte, kind uint16) ([]byte, bool) {
	for len(b) >= nlaHeader {
		l := int(binary.NativeEndian.Uint16(b))
		if l < nlaHeader || l > len(b) {
			return nil, false
		}
		if binary.NativeEndian.Uint16(b[2:])&^unix.NLA_F_NESTED == kind {
			return b[nlaHeader:l], true
		}
		b = b[min(align(l), len(b)):]
	}
	return nil, false
}

func attrU32(b []byte, kind uint16) (uint32, bool) {
	v, ok := attr(b, kind)
	if !ok || len(v) < 4 {
		return 0, false
	}
	return binary.NativeEndian.Uint32(v), true
}

func align(n int) int { return (n + 3) &^ 3 }
