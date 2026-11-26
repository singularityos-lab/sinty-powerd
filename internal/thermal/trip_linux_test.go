package thermal

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func nla(kind uint16, value []byte) []byte {
	b := make([]byte, align(nlaHeader+len(value)))
	binary.NativeEndian.PutUint16(b, uint16(nlaHeader+len(value)))
	binary.NativeEndian.PutUint16(b[2:], kind)
	copy(b[nlaHeader:], value)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, v)
	return b
}

func genl(kind uint16, cmd byte, attrs ...[]byte) []byte {
	msg := make([]byte, unix.NLMSG_HDRLEN+genlHeader)
	for _, a := range attrs {
		msg = append(msg, a...)
	}
	binary.NativeEndian.PutUint32(msg, uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:], kind)
	msg[unix.NLMSG_HDRLEN] = cmd
	return msg
}

func TestOnlyCrossingsOfTheZoneCount(t *testing.T) {
	up := genl(30, eventTripUp, nla(attrZoneID, u32(9)), nla(4, u32(0)))
	down := genl(30, eventTripDown, nla(attrZoneID, u32(9)))
	other := genl(30, eventTripUp, nla(attrZoneID, u32(3)))
	sampling := genl(30, 1, nla(attrZoneID, u32(9)))
	if !tripCrossed(up, 9) || !tripCrossed(down, 9) {
		t.Fatal("a crossing was missed")
	}
	if tripCrossed(other, 9) || tripCrossed(sampling, 9) || tripCrossed(up[:10], 9) {
		t.Fatal("another event counted as a crossing")
	}
	if !tripCrossed(append(append([]byte(nil), other...), up...), 9) {
		t.Fatal("a crossing after another message was missed")
	}
}

func TestEventGroupIsFoundInTheControllerReply(t *testing.T) {
	group := func(id uint32, name string) []byte {
		return nla(1, append(nla(unix.CTRL_ATTR_MCAST_GRP_NAME, append([]byte(name), 0)), nla(unix.CTRL_ATTR_MCAST_GRP_ID, u32(id))...))
	}
	reply := genl(unix.GENL_ID_CTRL, unix.CTRL_CMD_NEWFAMILY,
		nla(unix.CTRL_ATTR_FAMILY_ID, []byte{30, 0}),
		nla(unix.CTRL_ATTR_MCAST_GROUPS|unix.NLA_F_NESTED, append(group(7, "sampling"), group(8, "event")...)))
	if id, err := groupFromReply(reply); err != nil || id != 8 {
		t.Fatalf("%d %v", id, err)
	}
	if _, err := groupFromReply(genl(unix.NLMSG_ERROR, 0)); err == nil {
		t.Fatal("an error reply gave a group")
	}
}

func TestTripsWithCoolingDevicesAreLeftAlone(t *testing.T) {
	zone := filepath.Join(t.TempDir(), "thermal_zone9")
	root := filepath.Dir(zone)
	put(t, root, "thermal_zone9/trip_point_0_type", "critical")
	put(t, root, "thermal_zone9/trip_point_0_temp", "105000")
	put(t, root, "thermal_zone9/trip_point_1_type", "passive")
	put(t, root, "thermal_zone9/trip_point_1_temp", "0")
	if got := writableTrip(zone); got != filepath.Join(zone, "trip_point_1_temp") {
		t.Fatalf("picked %q", got)
	}
	os.Symlink(zone, filepath.Join(zone, "cdev0"))
	if got := writableTrip(zone); got != "" {
		t.Fatalf("moved a trip that drives cooling: %q", got)
	}
}

func TestKernelThermalEventsCanBeJoined(t *testing.T) {
	fd, err := subscribe()
	if err != nil {
		t.Skipf("thermal netlink unavailable here: %v", err)
	}
	unix.Close(fd)
}
