package events

import "syscall"

func PowerSupplyChanges(changed func()) error {
	return devdWatch(DevdSocket, func(line string) {
		if devdPowerSupply(line) {
			changed()
		}
	})
}

func DevicesAdded(added func()) error {
	return devdWatch(DevdSocket, func(line string) {
		if devdDeviceAdded(line) {
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
