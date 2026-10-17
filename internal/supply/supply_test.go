package supply

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeSupply(t *testing.T, root, name, uevent string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "uevent"), []byte(uevent), 0o644)
	for k, v := range files {
		os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644)
	}
}

// The fixture is a real laptop, and the expectations are what UPower 1.91
// reported for it at the same moment.
func laptop(t *testing.T, status, powerNow string) string {
	root := t.TempDir()
	writeSupply(t, root, "AC", "DEVTYPE=power_supply\nPOWER_SUPPLY_NAME=AC\nPOWER_SUPPLY_TYPE=Mains\nPOWER_SUPPLY_ONLINE=1\n", nil)
	writeSupply(t, root, "BAT0", "DEVTYPE=power_supply\nPOWER_SUPPLY_NAME=BAT0\nPOWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_STATUS="+status+
		"\nPOWER_SUPPLY_PRESENT=1\nPOWER_SUPPLY_TECHNOLOGY=Li-poly\nPOWER_SUPPLY_CYCLE_COUNT=334\nPOWER_SUPPLY_VOLTAGE_MIN_DESIGN=11520000\nPOWER_SUPPLY_VOLTAGE_NOW=12937000\nPOWER_SUPPLY_POWER_NOW="+powerNow+
		"\nPOWER_SUPPLY_ENERGY_FULL_DESIGN=57000000\nPOWER_SUPPLY_ENERGY_FULL=49660000\nPOWER_SUPPLY_ENERGY_NOW=49800000\nPOWER_SUPPLY_CAPACITY=100\nPOWER_SUPPLY_CAPACITY_LEVEL=Normal\nPOWER_SUPPLY_MODEL_NAME=L22M3PG5\nPOWER_SUPPLY_MANUFACTURER=SMP\nPOWER_SUPPLY_SERIAL_NUMBER= 3093\n",
		map[string]string{"charge_control_start_threshold": "95", "charge_control_end_threshold": "100"})
	writeSupply(t, root, "ucsi-source-psy-USBC000:001", "DEVTYPE=power_supply\nPOWER_SUPPLY_TYPE=USB\nPOWER_SUPPLY_ONLINE=1\nPOWER_SUPPLY_SCOPE=Device\n", nil)
	return root
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestMatchesUPowerOnARealLaptop(t *testing.T) {
	devs := Scan(laptop(t, "Not charging", "0"), DefaultPolicy, nil)
	if len(devs) != 2 || devs[0].ID != "battery_BAT0" || devs[1].ID != "line_power_AC" {
		t.Fatalf("devices: %v", ids(devs))
	}
	b, ac := devs[0], devs[1]
	checks := []struct {
		name string
		ok   bool
	}{
		{"Energy", near(b.Energy, 49.8)},
		{"EnergyFull", near(b.EnergyFull, 49.8)},
		{"EnergyFullDesign", near(b.EnergyFullDesign, 57)},
		{"EnergyRate", b.EnergyRate == 0},
		{"Voltage", near(b.Voltage, 12.937)},
		{"ChargeCycles", b.ChargeCycles == 334},
		{"Percentage", b.Percentage == 100},
		{"State", b.State == StateFullyCharged},
		{"Capacity", near(b.Capacity, 87.368421052631575)},
		{"Technology", b.Technology == 2},
		{"WarningLevel", b.WarningLevel == WarningNone},
		{"BatteryLevel", b.BatteryLevel == 1},
		{"IconName", b.IconName == "battery-full-charged-symbolic"},
		{"Thresholds", b.ChargeStartThreshold == 75 && b.ChargeEndThreshold == 80 && !b.ChargeThresholdEnabled && b.ChargeThresholdSupported && b.ChargeThresholdSettingsSupported == 3},
		{"VoltageMinDesign", near(b.VoltageMinDesign, 11.52)},
		{"CapacityLevel", b.CapacityLevel == "Normal"},
		{"Identity", b.Vendor == "SMP" && b.Model == "L22M3PG5" && b.Serial == "3093" && b.NativePath == "BAT0"},
		{"Battery flags", b.PowerSupply && b.HasHistory && b.HasStatistics && b.IsPresent && b.IsRechargeable && !b.Online},
		{"AC", ac.Type == KindLinePower && ac.Online && ac.PowerSupply && !ac.IsPresent && ac.ChargeCycles == -1 && ac.IconName == "ac-adapter-symbolic" && ac.ChargeEndThreshold == 100},
		{"OnBattery", !OnBattery(devs)},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s differs from UPower: %+v", c.name, b)
		}
	}
	d := Display(devs, DefaultPolicy)
	if !near(d.Energy, 49.8) || !near(d.EnergyFull, 49.8) || d.Percentage != 100 || d.State != StateFullyCharged ||
		d.IsRechargeable || d.Capacity != 0 || d.ChargeCycles != 0 || d.ChargeEndThreshold != 0 || !d.PowerSupply || !d.IsPresent ||
		d.IconName != "battery-full-charged-symbolic" || d.Type != KindBattery {
		t.Errorf("display device differs from UPower: %+v", d)
	}
}

func TestDischargingEstimatesAndWarnings(t *testing.T) {
	root := laptop(t, "Discharging", "8300000")
	os.Remove(filepath.Join(root, "AC", "uevent"))
	writeSupply(t, root, "AC", "POWER_SUPPLY_TYPE=Mains\nPOWER_SUPPLY_ONLINE=0\n", nil)
	devs := Scan(root, DefaultPolicy, nil)
	b := devs[0]
	if b.State != StateDischarging || b.TimeToEmpty != int64(49.8/8.3*3600) || b.TimeToFull != 0 {
		t.Errorf("state %d empty %d full %d", b.State, b.TimeToEmpty, b.TimeToFull)
	}
	if b.IconName != "battery-full-symbolic" || !OnBattery(devs) {
		t.Errorf("icon %q onbattery %v", b.IconName, OnBattery(devs))
	}
	for pct, want := range map[float64]uint32{50: WarningNone, 20: WarningLow, 5: WarningCritical, 2: WarningAction} {
		b.Percentage = pct
		if got := warning(b, DefaultPolicy); got != want {
			t.Errorf("warning at %v%% = %d, want %d", pct, got, want)
		}
	}
}

func TestChargeFromChargeCounters(t *testing.T) {
	root := t.TempDir()
	writeSupply(t, root, "BAT1", "POWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_STATUS=Charging\nPOWER_SUPPLY_PRESENT=1\nPOWER_SUPPLY_VOLTAGE_MIN_DESIGN=10000000\nPOWER_SUPPLY_VOLTAGE_NOW=10000000\nPOWER_SUPPLY_CHARGE_NOW=2000000\nPOWER_SUPPLY_CHARGE_FULL=4000000\nPOWER_SUPPLY_CHARGE_FULL_DESIGN=5000000\nPOWER_SUPPLY_CURRENT_NOW=1000000\n", nil)
	b := Scan(root, DefaultPolicy, nil)[0]
	if !near(b.Energy, 20) || !near(b.EnergyFull, 40) || !near(b.EnergyRate, 10) || !near(b.Percentage, 50) || b.TimeToFull != 7200 || !near(b.Capacity, 80) {
		t.Errorf("battery from charge counters: %+v", b)
	}
	if b.IconName != "battery-good-charging-symbolic" {
		t.Errorf("icon %q", b.IconName)
	}
}

func ids(devs []*Device) []string {
	var out []string
	for _, d := range devs {
		out = append(out, d.ID)
	}
	return out
}
