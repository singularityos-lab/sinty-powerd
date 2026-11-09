package events

import (
	"bufio"
	"net"
	"strings"
	"time"
)

const DevdSocket = "/var/run/devd.seqpacket.pipe"

func devdField(line, key string) string {
	for _, f := range strings.Fields(line) {
		if k, v, ok := strings.Cut(f, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func devdPowerSupply(line string) bool {
	if !strings.HasPrefix(line, "!system=ACPI") {
		return false
	}
	s := devdField(line, "subsystem")
	return s == "ACAD" || s == "CMBAT"
}

func devdDeviceAdded(line string) bool {
	if strings.HasPrefix(line, "!system=USB") {
		return devdField(line, "subsystem") == "DEVICE" && devdField(line, "type") == "ATTACH"
	}
	return strings.HasPrefix(line, "+pci") || strings.HasPrefix(line, "+") && strings.Contains(line, " on pci")
}

func devdLid(line string) (closed, ok bool) {
	if !strings.HasPrefix(line, "!system=ACPI") || devdField(line, "subsystem") != "Lid" {
		return false, false
	}
	return devdField(line, "notify") == "0x00", true
}

func devdWatch(path string, handle func(line string)) error {
	conn, err := net.Dial("unixpacket", path)
	if err != nil {
		return err
	}
	go func() {
		for {
			r := bufio.NewReader(conn)
			for {
				line, err := r.ReadString('\n')
				if line = strings.TrimSpace(line); line != "" {
					handle(line)
				}
				if err != nil {
					break
				}
			}
			conn.Close()
			for {
				time.Sleep(5 * time.Second)
				if conn, err = net.Dial("unixpacket", path); err == nil {
					break
				}
			}
		}
	}()
	return nil
}
