package events

import (
	"net"
	"testing"
	"time"
)

func TestDevdLines(t *testing.T) {
	cases := []struct {
		line          string
		supply, added bool
		lid           string
	}{
		{`!system=ACPI subsystem=ACAD type=\_SB_.AC__ notify=0x00`, true, false, ""},
		{`!system=ACPI subsystem=CMBAT type=\_SB_.BAT0 notify=0x80`, true, false, ""},
		{`!system=ACPI subsystem=Lid type=\_SB_.LID_ notify=0x00`, false, false, "closed"},
		{`!system=ACPI subsystem=Lid type=\_SB_.LID_ notify=0x01`, false, false, "open"},
		{`!system=USB subsystem=DEVICE type=ATTACH ugen=ugen0.3 vendor=0x046d`, false, true, ""},
		{`!system=USB subsystem=DEVICE type=DETACH ugen=ugen0.3`, false, false, ""},
		{`+iwlwifi0 at slot=0 function=0 on pci2`, false, true, ""},
		{`-iwlwifi0 at slot=0 function=0 on pci2`, false, false, ""},
		{`!system=IFNET subsystem=wlan0 type=LINK_UP`, false, false, ""},
	}
	for _, c := range cases {
		if devdPowerSupply(c.line) != c.supply || devdDeviceAdded(c.line) != c.added {
			t.Fatalf("%s: supply %v added %v", c.line, devdPowerSupply(c.line), devdDeviceAdded(c.line))
		}
		closed, ok := devdLid(c.line)
		got := ""
		if ok && closed {
			got = "closed"
		} else if ok {
			got = "open"
		}
		if got != c.lid {
			t.Fatalf("%s: lid %q", c.line, got)
		}
	}
}

func TestDevdSocket(t *testing.T) {
	t.Chdir(t.TempDir())
	l, err := net.Listen("unixpacket", "devd.pipe")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	got := make(chan string, 4)
	if err := devdWatch("devd.pipe", func(line string) { got <- line }); err != nil {
		t.Fatal(err)
	}
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("!system=ACPI subsystem=ACAD notify=0x01\n"))
	c.Write([]byte("!system=ACPI subsystem=Lid notify=0x00\n"))
	for _, want := range []string{"!system=ACPI subsystem=ACAD notify=0x01", "!system=ACPI subsystem=Lid notify=0x00"} {
		select {
		case line := <-got:
			if line != want {
				t.Fatalf("line %q", line)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no event")
		}
	}
}
