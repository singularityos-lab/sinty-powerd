package service

import (
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-powerd/internal/diagnose"
)

func (o *powerObject) Diagnose(seconds uint32) (map[string][]string, *dbus.Error) {
	seconds = min(max(seconds, 1), 60)
	o.s.mu.Lock()
	awake := o.s.awakeDevices()
	o.s.mu.Unlock()
	return diagnose.Report(o.s.cfg.Root, time.Duration(seconds)*time.Second, awake), nil
}

func (s *Service) awakeDevices() []string {
	out := []string{}
	for _, d := range s.devs.Awake() {
		out = append(out, d.Path+" "+d.Driver+" "+d.Reason)
	}
	return out
}
