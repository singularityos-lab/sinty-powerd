package service

import (
	"log"

	"github.com/godbus/dbus/v5"
)

const (
	login1Name    = "org.freedesktop.login1"
	login1Path    = dbus.ObjectPath("/org/freedesktop/login1")
	login1Manager = "org.freedesktop.login1.Manager"
)

func (s *Service) WatchLogin() {
	for _, opts := range [][]dbus.MatchOption{
		{dbus.WithMatchInterface(login1Manager), dbus.WithMatchMember("PrepareForSleep")},
		{dbus.WithMatchObjectPath(login1Path), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")},
	} {
		if err := s.conn.AddMatchSignal(opts...); err != nil {
			log.Printf("watching login1: %v", err)
			return
		}
	}
	ch := make(chan *dbus.Signal, 16)
	s.conn.Signal(ch)
	if v, err := s.conn.Object(login1Name, login1Path).GetProperty(login1Manager + ".IdleHint"); err == nil {
		if idle, ok := v.Value().(bool); ok {
			s.SetIdle(idle)
		}
	}
	for sig := range ch {
		switch sig.Name {
		case login1Manager + ".PrepareForSleep":
			if len(sig.Body) == 1 {
				if sleeping, ok := sig.Body[0].(bool); ok && !sleeping {
					log.Print("resumed, applying the power settings again")
					s.Resumed()
				}
			}
		case "org.freedesktop.DBus.Properties.PropertiesChanged":
			if sig.Path != login1Path || len(sig.Body) < 2 {
				continue
			}
			if iface, _ := sig.Body[0].(string); iface != login1Manager {
				continue
			}
			changed, _ := sig.Body[1].(map[string]dbus.Variant)
			if v, ok := changed["IdleHint"]; ok {
				if idle, ok := v.Value().(bool); ok {
					s.SetIdle(idle)
				}
			}
		}
	}
}
