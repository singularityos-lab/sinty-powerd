package events

import "syscall"

func Watch(supply, added func()) error {
	return devdWatch(DevdSocket, func(line string) {
		switch {
		case devdPowerSupply(line):
			supply()
		case devdDeviceAdded(line):
			added()
		}
	})
}

func Lid(changed func(closed bool)) bool {
	if _, err := syscall.Sysctl("dev.acpi_lid.0.%driver"); err != nil {
		return false
	}
	changed(false)
	return devdWatch(DevdSocket, func(line string) {
		if closed, ok := devdLid(line); ok {
			changed(closed)
		}
	}) == nil
}
