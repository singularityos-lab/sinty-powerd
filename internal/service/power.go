package service

import (
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
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
