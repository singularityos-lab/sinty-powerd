package service

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/singularityos-lab/sinty-powerd/internal/policy"
	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
)

const (
	powerName  = "dev.sinty.Power1"
	powerPath  = dbus.ObjectPath("/dev/sinty/Power1")
	powerIface = "dev.sinty.Power1"
)

type powerObject struct{ s *Service }

func (s *Service) exportPower() error {
	obj := &powerObject{s}
	if err := s.conn.Export(obj, powerPath, powerIface); err != nil {
		return err
	}
	current, booted := "", ""
	if s.mux != nil {
		current, booted = s.mux.Current(), s.mux.Booted()
	}
	props, err := prop.Export(s.conn, powerPath, prop.Map{powerIface: {
		"GpuMux":       {Value: current, Emit: prop.EmitTrue},
		"GpuMuxBooted": {Value: booted, Emit: prop.EmitFalse},
		"Reasons":      {Value: [][]string{}, Emit: prop.EmitTrue},
		"Energy":       {Value: map[string]float64{}, Emit: prop.EmitFalse},
		"Form":         {Value: string(s.form), Emit: prop.EmitTrue},
		"Streaming":    {Value: false, Emit: prop.EmitTrue},
		"Radio":        {Value: map[string]bool{"wireless-save": true, "wake-on-radio": false}, Emit: prop.EmitTrue},
	}})
	if err != nil {
		return err
	}
	s.power = props
	return s.introspect(powerPath, powerIface, obj, props)
}

func (o *powerObject) SetGpuMux(sender dbus.Sender, mode string) *dbus.Error {
	s := o.s
	if uid, ok := s.authorized(sender); !ok {
		return denied("change the GPU mode", uid)
	}
	if s.mux == nil {
		return dbus.NewError(powerIface+".Error.NotSupported", []interface{}{"this computer has no GPU switch"})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mux.Set(mode); err != nil {
		return dbus.NewError(powerIface+".Error.Failed", []interface{}{err.Error()})
	}
	s.power.SetMust(powerIface, "GpuMux", s.mux.Current())
	return nil
}

func (o *powerObject) SetTerminal(sender dbus.Sender, kind string) *dbus.Error {
	s := o.s
	if uid, ok := s.authorized(sender); !ok || uid != 0 {
		return denied("change the device form", uid)
	}
	form := policy.Headless
	if kind != "" {
		f, ok := policy.ParseForm(kind)
		if !ok {
			return dbus.NewError(powerIface+".Error.Failed", []interface{}{"unknown terminal " + kind})
		}
		form = f
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.form = form
	s.power.SetMust(powerIface, "Form", string(form))
	s.apply()
	return nil
}

func (o *powerObject) SetStreaming(sender dbus.Sender, streaming bool) *dbus.Error {
	s := o.s
	if uid, ok := s.authorized(sender); !ok || uid != 0 {
		return denied("change the streaming state", uid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaming = streaming
	s.power.SetMust(powerIface, "Streaming", streaming)
	s.apply()
	return nil
}

func (s *Service) publishRadio() {
	if s.power == nil {
		return
	}
	next := map[string]bool{"wireless-save": s.settings.WirelessSave, "wake-on-radio": s.settings.WakeOnRadio}
	if cur, err := s.power.Get(powerIface, "Radio"); err == nil {
		if old, ok := cur.Value().(map[string]bool); ok && maps.Equal(old, next) {
			return
		}
	}
	s.power.SetMust(powerIface, "Radio", next)
}

func (s *Service) publishEnergy(now time.Time) {
	if s.meter == nil || s.power == nil {
		return
	}
	s.meter.Sample(now)
	s.power.SetMust(powerIface, "Energy", s.meter.Readings())
}

func (s *Service) reasons() [][]string {
	out := [][]string{}
	if s.guard != nil && s.degraded != "" {
		out = append(out, []string{"thermal", strconv.FormatInt(s.guard.Temperature()/1000, 10), strconv.Itoa(s.guard.Share())})
	}
	for _, h := range s.holds {
		out = append(out, []string{"hold-" + h.profile, h.app, h.reason})
	}
	if s.onBattery && s.batteryAware && s.effective() == profiles.Balanced {
		out = append(out, []string{"battery", "", ""})
	}
	if s.idle && s.effective() != profiles.Performance {
		out = append(out, []string{"idle", "", ""})
	}
	return out
}

func (s *Service) publishReasons() {
	if s.power == nil {
		return
	}
	next := s.reasons()
	if cur, err := s.power.Get(powerIface, "Reasons"); err == nil {
		if old, ok := cur.Value().([][]string); ok && slices.EqualFunc(old, next, slices.Equal[[]string]) {
			return
		}
	}
	s.power.SetMust(powerIface, "Reasons", next)
}
