package service

import (
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/singularityos-lab/sinty-powerd/internal/brightness"
)

const (
	brightnessName  = "dev.sinty.Brightness"
	brightnessPath  = dbus.ObjectPath("/dev/sinty/Brightness")
	brightnessIface = "dev.sinty.Brightness1"
)

type brightnessObject struct{ s *Service }

func (s *Service) exportBrightness() error {
	s.bright = brightness.Open(s.statePath("brightness"))
	obj := &brightnessObject{s}
	if err := s.conn.Export(obj, brightnessPath, brightnessIface); err != nil {
		return err
	}
	node := &introspect.Node{
		Name: string(brightnessPath),
		Interfaces: []introspect.Interface{introspect.IntrospectData, {
			Name:    brightnessIface,
			Methods: introspect.Methods(obj),
		}},
	}
	return s.conn.Export(introspect.NewIntrospectable(node), brightnessPath, "org.freedesktop.DBus.Introspectable")
}

func (s *Service) RestoreBrightness() int {
	if s.bright == nil {
		return 0
	}
	return s.bright.Restore(s.cfg.Root)
}

func (o *brightnessObject) Store(sender dbus.Sender, lines string) *dbus.Error {
	if uid, ok := o.s.authorized(sender); !ok {
		return denied("remember display brightness", uid)
	}
	if len(lines) > 8192 {
		return dbus.NewError(brightnessIface+".Error.Invalid", []interface{}{"too much data"})
	}
	if err := o.s.bright.Merge(lines); err != nil {
		return dbus.NewError(brightnessIface+".Error.Failed", []interface{}{err.Error()})
	}
	return nil
}
