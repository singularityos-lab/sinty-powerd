// Package service publishes sinty-powerd on the system bus under the names
// and interfaces of UPower and power-profiles-daemon, so their clients keep
// working unchanged.
package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/singularityos-lab/sinty-powerd/internal/devices"
	"github.com/singularityos-lab/sinty-powerd/internal/energy"
	"github.com/singularityos-lab/sinty-powerd/internal/gpumux"
	"github.com/singularityos-lab/sinty-powerd/internal/policy"
	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
	"github.com/singularityos-lab/sinty-powerd/internal/supply"
	"github.com/singularityos-lab/sinty-powerd/internal/thermal"
)

const (
	upowerName    = "org.freedesktop.UPower"
	upowerPath    = dbus.ObjectPath("/org/freedesktop/UPower")
	deviceIface   = "org.freedesktop.UPower.Device"
	devicesPrefix = "/org/freedesktop/UPower/devices/"
	daemonVersion = "1.91.0"
)

var ppdEndpoints = []struct {
	name  string
	path  dbus.ObjectPath
	iface string
}{
	{"org.freedesktop.UPower.PowerProfiles", "/org/freedesktop/UPower/PowerProfiles", "org.freedesktop.UPower.PowerProfiles"},
	{"net.hadess.PowerProfiles", "/net/hadess/PowerProfiles", "net.hadess.PowerProfiles"},
}

// Config locates the system and the daemon state.
type Config struct {
	Root     string
	StateDir string
	Policy   supply.Policy
	Form     policy.Form
}

type exported struct {
	dev   *supply.Device
	props *prop.Properties
}

type hold struct {
	cookie  uint32
	profile string
	reason  string
	app     string
	sender  string
}

// Service is the running daemon.
type Service struct {
	cfg  Config
	conn *dbus.Conn

	mu         sync.Mutex
	devices    map[string]*exported
	display    *exported
	manager    *prop.Properties
	ppd        []*prop.Properties
	backend    *profiles.Backend
	chosen     string
	holds      []hold
	nextCookie uint32
	degraded   string
	guard      *thermal.Guard
	mux        *gpumux.Mux
	meter      *energy.Meter
	devs       *devices.Manager
	idle       bool
	form       policy.Form
	streaming  bool
	settings   policy.Settings
	power      *prop.Properties
	history    map[string]*history
	threshold  map[string]bool
	lidPresent bool
	lidClosed  bool

	onBattery    bool
	batteryAware bool
	lowHeld      bool
	lowCookie    uint32

	criticalFired bool
}

// New connects to the system bus and publishes UPower and the power
// profiles.
func New(cfg Config) (*Service, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	s := &Service{
		cfg:       cfg,
		conn:      conn,
		devices:   map[string]*exported{},
		backend:   profiles.Detect(cfg.Root),
		guard:     thermal.Detect(cfg.Root),
		mux:       gpumux.Detect(cfg.Root),
		meter:     energy.Detect(cfg.Root),
		devs:      devices.New(cfg.Root),
		form:      cfg.Form,
		history:   map[string]*history{},
		threshold: map[string]bool{},
		chosen:    profiles.Balanced,

		batteryAware: true,
	}
	s.loadState()
	if err := s.export(); err != nil {
		conn.Close()
		return nil, err
	}
	for _, name := range []string{upowerName, ppdEndpoints[0].name, ppdEndpoints[1].name, powerName} {
		reply, err := conn.RequestName(name, dbus.NameFlagReplaceExisting|dbus.NameFlagDoNotQueue)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if reply != dbus.RequestNameReplyPrimaryOwner {
			conn.Close()
			return nil, fmt.Errorf("%s is owned by another process", name)
		}
	}
	return s, nil
}

func (s *Service) statePath(name string) string { return filepath.Join(s.cfg.StateDir, name) }

func (s *Service) loadState() {
	if data, err := os.ReadFile(s.statePath("profile")); err == nil {
		if p := string(data); s.backend.Supports(p) {
			s.chosen = p
		}
	}
	if data, err := os.ReadFile(s.statePath("battery-aware")); err == nil {
		s.batteryAware = string(data) != "false"
	}
	if data, err := os.ReadFile(s.statePath("charge-threshold.json")); err == nil {
		json.Unmarshal(data, &s.threshold)
	}
}

func writeState(path string, data []byte) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, path)
	}
}

type upowerObject struct{ s *Service }
type deviceObject struct {
	s  *Service
	id string
}
type ppdObject struct{ s *Service }

func (s *Service) export() error {
	u := &upowerObject{s}
	if err := s.conn.Export(u, upowerPath, upowerName); err != nil {
		return err
	}
	props, err := prop.Export(s.conn, upowerPath, prop.Map{upowerName: {
		"DaemonVersion": {Value: daemonVersion, Emit: prop.EmitFalse},
		"OnBattery":     {Value: false, Emit: prop.EmitTrue},
		"LidIsClosed":   {Value: false, Emit: prop.EmitTrue},
		"LidIsPresent":  {Value: false, Emit: prop.EmitTrue},
	}})
	if err != nil {
		return err
	}
	s.manager = props
	if err := s.introspect(upowerPath, upowerName, u, props); err != nil {
		return err
	}
	display, err := s.exportDevice(supply.Display(nil, s.cfg.Policy))
	if err != nil {
		return err
	}
	s.display = display
	for _, ep := range ppdEndpoints {
		obj := &ppdObject{s}
		if err := s.conn.Export(obj, ep.path, ep.iface); err != nil {
			return err
		}
		p, err := prop.Export(s.conn, ep.path, prop.Map{ep.iface: s.ppdProps()})
		if err != nil {
			return err
		}
		s.ppd = append(s.ppd, p)
		if err := s.introspect(ep.path, ep.iface, obj, p); err != nil {
			return err
		}
	}
	return s.exportPower()
}

func (s *Service) introspect(path dbus.ObjectPath, iface string, obj interface{}, props *prop.Properties) error {
	node := &introspect.Node{
		Name: string(path),
		Interfaces: []introspect.Interface{introspect.IntrospectData, prop.IntrospectData, {
			Name:       iface,
			Methods:    introspect.Methods(obj),
			Properties: props.Introspection(iface),
		}},
	}
	return s.conn.Export(introspect.NewIntrospectable(node), path, "org.freedesktop.DBus.Introspectable")
}

func deviceProps(d *supply.Device) map[string]interface{} {
	return map[string]interface{}{
		"NativePath": d.NativePath, "Vendor": d.Vendor, "Model": d.Model, "Serial": d.Serial,
		"UpdateTime": d.UpdateTime, "Type": d.Type, "PowerSupply": d.PowerSupply,
		"HasHistory": d.HasHistory, "HasStatistics": d.HasStatistics, "Online": d.Online,
		"Energy": d.Energy, "EnergyEmpty": d.EnergyEmpty, "EnergyFull": d.EnergyFull,
		"EnergyFullDesign": d.EnergyFullDesign, "EnergyRate": d.EnergyRate, "Voltage": d.Voltage,
		"ChargeCycles": d.ChargeCycles, "Luminosity": d.Luminosity, "TimeToEmpty": d.TimeToEmpty,
		"TimeToFull": d.TimeToFull, "Percentage": d.Percentage, "Temperature": d.Temperature,
		"IsPresent": d.IsPresent, "State": d.State, "IsRechargeable": d.IsRechargeable,
		"Capacity": d.Capacity, "Technology": d.Technology, "WarningLevel": d.WarningLevel,
		"BatteryLevel": d.BatteryLevel, "IconName": d.IconName,
		"ChargeStartThreshold": d.ChargeStartThreshold, "ChargeEndThreshold": d.ChargeEndThreshold,
		"ChargeThresholdEnabled": d.ChargeThresholdEnabled, "ChargeThresholdSupported": d.ChargeThresholdSupported,
		"ChargeThresholdSettingsSupported": d.ChargeThresholdSettingsSupported,
		"VoltageMinDesign":                 d.VoltageMinDesign, "VoltageMaxDesign": d.VoltageMaxDesign,
		"CapacityLevel": d.CapacityLevel,
	}
}

func devicePath(id string) dbus.ObjectPath { return dbus.ObjectPath(devicesPrefix + id) }

func (s *Service) exportDevice(d *supply.Device) (*exported, error) {
	path := devicePath(d.ID)
	obj := &deviceObject{s, d.ID}
	if err := s.conn.Export(obj, path, deviceIface); err != nil {
		return nil, err
	}
	m := map[string]*prop.Prop{}
	for k, v := range deviceProps(d) {
		m[k] = &prop.Prop{Value: v, Emit: prop.EmitTrue}
	}
	props, err := prop.Export(s.conn, path, prop.Map{deviceIface: m})
	if err != nil {
		return nil, err
	}
	if err := s.introspect(path, deviceIface, obj, props); err != nil {
		return nil, err
	}
	return &exported{dev: d, props: props}, nil
}

func (s *Service) unexportDevice(id string) {
	path := devicePath(id)
	s.conn.Export(nil, path, deviceIface)
	s.conn.Export(nil, path, "org.freedesktop.DBus.Properties")
	s.conn.Export(nil, path, "org.freedesktop.DBus.Introspectable")
}

func update(e *exported, d *supply.Device) {
	old := deviceProps(e.dev)
	for k, v := range deviceProps(d) {
		if old[k] != v {
			e.props.SetMust(deviceIface, k, v)
		}
	}
	e.dev = d
}

// Refresh rereads the power supplies and publishes what changed.
func (s *Service) Refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	devs := supply.Scan(filepath.Join(s.cfg.Root, "sys/class/power_supply"), s.cfg.Policy, s.threshold)
	seen := map[string]bool{}
	now := time.Now()
	for _, d := range devs {
		seen[d.ID] = true
		if e, ok := s.devices[d.ID]; ok {
			update(e, d)
		} else if e, err := s.exportDevice(d); err == nil {
			s.devices[d.ID] = e
			s.conn.Emit(upowerPath, upowerName+".DeviceAdded", devicePath(d.ID))
		}
		if d.HasHistory {
			h := s.history[d.ID]
			if h == nil {
				h = loadHistory(s.statePath("history-" + d.ID + ".json"))
				s.history[d.ID] = h
			}
			h.record(d, now)
		}
	}
	for id := range s.devices {
		if !seen[id] {
			s.unexportDevice(id)
			delete(s.devices, id)
			s.conn.Emit(upowerPath, upowerName+".DeviceRemoved", devicePath(id))
		}
	}
	display := supply.Display(devs, s.cfg.Policy)
	update(s.display, display)
	onBattery := supply.OnBattery(devs)
	s.setManager("OnBattery", onBattery)
	s.govern(onBattery, display.Percentage)
	s.publishEnergy(now)
}

func (s *Service) setManager(name string, v interface{}) {
	if cur, err := s.manager.Get(upowerName, name); err == nil && cur.Value() == v {
		return
	}
	s.manager.SetMust(upowerName, name, v)
}

// SetLid publishes the state of the lid switch.
func (s *Service) SetLid(present, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setManager("LidIsPresent", present)
	s.setManager("LidIsClosed", closed)
}

// SaveHistory writes the battery history to the state directory.
func (s *Service) SaveHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, h := range s.history {
		if err := h.save(s.statePath("history-" + id + ".json")); err != nil {
			log.Printf("history %s: %v", id, err)
		}
	}
}

func (u *upowerObject) EnumerateDevices() ([]dbus.ObjectPath, *dbus.Error) {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	ids := make([]string, 0, len(u.s.devices))
	for id := range u.s.devices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]dbus.ObjectPath, 0, len(ids))
	for _, id := range ids {
		out = append(out, devicePath(id))
	}
	return out, nil
}

func (u *upowerObject) EnumerateKbdBacklights() ([]dbus.ObjectPath, *dbus.Error) {
	return []dbus.ObjectPath{}, nil
}

func (u *upowerObject) GetDisplayDevice() (dbus.ObjectPath, *dbus.Error) {
	return devicePath("DisplayDevice"), nil
}

func (u *upowerObject) GetCriticalAction() (string, *dbus.Error) {
	return "PowerOff", nil
}

func (d *deviceObject) Refresh() *dbus.Error {
	d.s.Refresh()
	return nil
}

func (d *deviceObject) GetHistory(kind string, timespan, resolution uint32) ([]sample, *dbus.Error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	h := d.s.history[d.id]
	if h == nil {
		return []sample{}, nil
	}
	switch kind {
	case "charge", "rate", "time-full", "time-empty":
	default:
		return nil, dbus.NewError("org.freedesktop.UPower.GeneralError", []interface{}{"unknown history type " + kind})
	}
	out := h.get(kind, timespan, resolution, time.Now())
	if out == nil {
		out = []sample{}
	}
	return out, nil
}

type statPoint struct {
	Value    float64
	Accuracy float64
}

func (d *deviceObject) GetStatistics(kind string) ([]statPoint, *dbus.Error) {
	return []statPoint{}, nil
}

func (d *deviceObject) EnableChargeThreshold(sender dbus.Sender, enable bool) *dbus.Error {
	s := d.s
	if uid, ok := s.authorized(sender); !ok {
		return denied("change the charge threshold", uid)
	}
	s.mu.Lock()
	e := s.devices[d.id]
	if e == nil || !e.dev.ChargeThresholdSupported {
		s.mu.Unlock()
		return dbus.NewError("org.freedesktop.UPower.GeneralError", []interface{}{"charge threshold not supported"})
	}
	start, end := uint32(0), uint32(100)
	if enable {
		start, end = s.cfg.Policy.ChargeStart, s.cfg.Policy.ChargeEnd
	}
	dir := filepath.Join(s.cfg.Root, "sys/class/power_supply", e.dev.NativePath)
	var err error
	if e.dev.ChargeThresholdSettingsSupported&2 != 0 {
		err = os.WriteFile(filepath.Join(dir, "charge_control_end_threshold"), []byte(strconv.Itoa(int(end))), 0)
	}
	if err == nil && e.dev.ChargeThresholdSettingsSupported&1 != 0 {
		err = os.WriteFile(filepath.Join(dir, "charge_control_start_threshold"), []byte(strconv.Itoa(int(start))), 0)
	}
	if err != nil {
		s.mu.Unlock()
		return dbus.NewError("org.freedesktop.UPower.GeneralError", []interface{}{err.Error()})
	}
	s.threshold[e.dev.NativePath] = enable
	data, _ := json.Marshal(s.threshold)
	writeState(s.statePath("charge-threshold.json"), data)
	s.mu.Unlock()
	s.Refresh()
	return nil
}

func (s *Service) authorized(sender dbus.Sender) (uint32, bool) {
	var uid uint32
	if err := s.conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, string(sender)).Store(&uid); err != nil {
		return 0, false
	}
	return uid, uid == 0 || uid >= 1000
}

func denied(action string, uid uint32) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []interface{}{fmt.Sprintf("not authorized to %s (caller uid %d)", action, uid)})
}
