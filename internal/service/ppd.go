package service

import (
	"log"
	"slices"
	"strconv"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
	"github.com/singularityos-lab/sinty-powerd/internal/supply"
)

const ppdVersion = "0.30"

func (s *Service) profileList() []map[string]dbus.Variant {
	var out []map[string]dbus.Variant
	for _, p := range s.backend.Profiles() {
		m := map[string]dbus.Variant{"Profile": dbus.MakeVariant(p.Name), "Driver": dbus.MakeVariant(p.Driver)}
		if p.CPUDriver != "" {
			m["CpuDriver"] = dbus.MakeVariant(p.CPUDriver)
		}
		if p.PlatformDriver != "" {
			m["PlatformDriver"] = dbus.MakeVariant(p.PlatformDriver)
		}
		out = append(out, m)
	}
	return out
}

func (s *Service) holdList() []map[string]dbus.Variant {
	out := []map[string]dbus.Variant{}
	for _, h := range s.holds {
		out = append(out, map[string]dbus.Variant{
			"Profile":       dbus.MakeVariant(h.profile),
			"Reason":        dbus.MakeVariant(h.reason),
			"ApplicationId": dbus.MakeVariant(h.app),
		})
	}
	return out
}

func (s *Service) ppdProps() map[string]*prop.Prop {
	return map[string]*prop.Prop{
		"ActiveProfile":        {Value: s.effective(), Writable: true, Emit: prop.EmitTrue, Callback: s.setActive},
		"PerformanceInhibited": {Value: "", Emit: prop.EmitTrue},
		"PerformanceDegraded":  {Value: "", Emit: prop.EmitTrue},
		"Profiles":             {Value: s.profileList(), Emit: prop.EmitTrue},
		"Actions":              {Value: []string{}, Emit: prop.EmitTrue},
		"ActionsInfo":          {Value: []map[string]dbus.Variant{}, Emit: prop.EmitTrue},
		"ActiveProfileHolds":   {Value: s.holdList(), Emit: prop.EmitTrue},
		"Version":              {Value: ppdVersion, Emit: prop.EmitFalse},
		"BatteryAware":         {Value: s.batteryAware, Writable: true, Emit: prop.EmitTrue, Callback: s.setBatteryAware},
	}
}

// effective is the chosen profile, overridden by holds: a power-saver hold
// wins over a performance hold, as in power-profiles-daemon.
func (s *Service) effective() string {
	profile := s.chosen
	for _, h := range s.holds {
		if h.profile == profiles.PowerSaver {
			return profiles.PowerSaver
		}
		if h.profile == profiles.Performance {
			profile = profiles.Performance
		}
	}
	return profile
}

func (s *Service) setPPD(name string, v interface{}) {
	for i, p := range s.ppd {
		iface := ppdEndpoints[i].iface
		if cur, err := p.Get(iface, name); err == nil && equalValue(cur.Value(), v) {
			continue
		}
		p.SetMust(iface, name, v)
	}
}

func equalValue(a, b interface{}) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return false
}

// apply writes the effective profile to the hardware and publishes it.
func (s *Service) apply() {
	p := s.effective()
	if err := s.backend.Apply(p, s.onBattery && s.batteryAware); err != nil {
		log.Printf("profile %s: %v", p, err)
	}
	s.setPPD("ActiveProfile", p)
	s.setPPD("ActiveProfileHolds", s.holdList())
	s.publishReasons()
}

// Start applies the remembered profile.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apply()
}

// setActive runs while the property store is locked, so it only checks the
// value and hands the switch to choose, which may publish properties.
func (s *Service) setActive(c *prop.Change) *dbus.Error {
	profile, ok := c.Value.(string)
	if !ok || !s.backend.Supports(profile) {
		return dbus.MakeFailedError(errInvalidProfile(profile))
	}
	go s.choose(profile)
	return nil
}

// choose makes profile the user's choice and drops every hold, as an explicit
// choice does in power-profiles-daemon.
func (s *Service) choose(profile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chosen = profile
	for _, h := range s.holds {
		for _, ep := range ppdEndpoints {
			s.conn.Emit(ep.path, ep.iface+".ProfileReleased", h.cookie)
		}
	}
	s.holds = nil
	writeState(s.statePath("profile"), []byte(profile))
	s.apply()
}

type invalidProfile string

func (e invalidProfile) Error() string { return "invalid profile " + string(e) }

func errInvalidProfile(p string) error { return invalidProfile(p) }

func (o *ppdObject) HoldProfile(sender dbus.Sender, profile, reason, app string) (uint32, *dbus.Error) {
	s := o.s
	if uid, ok := s.authorized(sender); !ok {
		return 0, denied("hold a power profile", uid)
	}
	if profile != profiles.PowerSaver && profile != profiles.Performance || !s.backend.Supports(profile) {
		return 0, dbus.MakeFailedError(errInvalidProfile(profile))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextCookie++
	s.holds = append(s.holds, hold{cookie: s.nextCookie, profile: profile, reason: reason, app: app, sender: string(sender)})
	s.apply()
	return s.nextCookie, nil
}

func (o *ppdObject) ReleaseProfile(sender dbus.Sender, cookie uint32) *dbus.Error {
	s := o.s
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.holds, func(h hold) bool { return h.cookie == cookie })
	if i < 0 {
		return dbus.MakeFailedError(errInvalidProfile("cookie"))
	}
	s.release(i)
	return nil
}

func (o *ppdObject) SetActionEnabled(action string, enabled bool) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []interface{}{"unknown action " + action})
}

func (s *Service) release(i int) {
	cookie := s.holds[i].cookie
	s.holds = slices.Delete(s.holds, i, i+1)
	for _, ep := range ppdEndpoints {
		s.conn.Emit(ep.path, ep.iface+".ProfileReleased", cookie)
	}
	s.apply()
}

// WatchHolders drops the holds of programs that leave the bus.
func (s *Service) WatchHolders() {
	if err := s.conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged")); err != nil {
		log.Printf("watching hold owners: %v", err)
		return
	}
	ch := make(chan *dbus.Signal, 32)
	s.conn.Signal(ch)
	for sig := range ch {
		if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
			continue
		}
		name, _ := sig.Body[0].(string)
		owner, _ := sig.Body[2].(string)
		if owner != "" {
			continue
		}
		s.mu.Lock()
		for i := len(s.holds) - 1; i >= 0; i-- {
			if s.holds[i].sender == name {
				s.release(i)
			}
		}
		s.mu.Unlock()
	}
}

// ThermalStep runs the thermal guard once and publishes whether performance
// is held back by temperature.
func (s *Service) ThermalStep() {
	if s.guard == nil {
		return
	}
	limited := s.guard.Step()
	s.mu.Lock()
	defer s.mu.Unlock()
	degraded := ""
	if limited {
		degraded = "high-operating-temperature"
	}
	if degraded != s.degraded {
		s.degraded = degraded
		s.setPPD("PerformanceDegraded", degraded)
	}
	s.publishReasons()
}

// Stop gives the processor its full power back.
func (s *Service) Stop() {
	if s.guard != nil {
		s.guard.Restore()
	}
}

const lowBatteryReason = "Battery low"

func batteryState(policy supply.Policy, onBattery bool, percentage float64) (low, recovered bool) {
	low = onBattery && percentage > 0 && percentage <= policy.PercentageLow
	recovered = !onBattery || percentage >= policy.PercentageLow+5
	return low, recovered
}

func batteryCritical(policy supply.Policy, onBattery bool, percentage float64) bool {
	return onBattery && percentage > 0 && percentage <= policy.PercentageAction
}

func (s *Service) govern(onBattery bool, percentage float64) {
	changed := onBattery != s.onBattery
	s.onBattery = onBattery
	switch {
	case batteryCritical(s.cfg.Policy, onBattery, percentage) && !s.criticalFired:
		s.criticalFired = true
		log.Printf("battery at %.0f%%, powering off", percentage)
		go s.powerOff()
	case !onBattery:
		s.criticalFired = false
	}
	low, recovered := batteryState(s.cfg.Policy, onBattery, percentage)
	switch {
	case low && !s.lowHeld:
		s.lowHeld = true
		if s.backend.Supports(profiles.PowerSaver) {
			s.nextCookie++
			s.lowCookie = s.nextCookie
			s.holds = append(s.holds, hold{cookie: s.lowCookie, profile: profiles.PowerSaver, reason: lowBatteryReason, app: "sinty-powerd"})
			s.apply()
			return
		}
	case recovered && s.lowHeld:
		s.lowHeld = false
		cookie := s.lowCookie
		s.lowCookie = 0
		if i := slices.IndexFunc(s.holds, func(h hold) bool { return h.cookie == cookie }); i >= 0 {
			s.release(i)
			return
		}
	}
	if changed {
		s.apply()
	}
}

func (s *Service) powerOff() {
	call := s.conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").Call("org.freedesktop.login1.Manager.PowerOff", 0, false)
	if call.Err != nil {
		log.Printf("power off on critical battery: %v", call.Err)
	}
}

func (s *Service) setBatteryAware(c *prop.Change) *dbus.Error {
	aware, ok := c.Value.(bool)
	if !ok {
		return dbus.MakeFailedError(errInvalidProfile("battery awareness"))
	}
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.batteryAware = aware
		writeState(s.statePath("battery-aware"), []byte(strconv.FormatBool(aware)))
		s.apply()
	}()
	return nil
}
