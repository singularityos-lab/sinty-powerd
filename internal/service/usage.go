package service

import (
	"log"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/singularityos-lab/sinty-powerd/internal/usage"
)

const UsageInterval = 10 * time.Second

type hotProcess struct {
	PID   uint32
	Name  string
	App   string
	Cores float64
	Since int64
}

func (s *Service) SampleUsage(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil || s.power == nil {
		return
	}
	s.usage.Sample(now)
	significant := []string{}
	for _, a := range s.usage.Apps() {
		if a.Significant {
			significant = append(significant, a.ID)
		}
	}
	slices.Sort(significant)
	if cur, err := s.power.Get(powerIface, "Significant"); err != nil || !slices.Equal(cur.Value().([]string), significant) {
		s.power.SetMust(powerIface, "Significant", significant)
	}
	hot := []hotProcess{}
	for _, h := range s.usage.Hot(now) {
		hot = append(hot, hotProcess{uint32(h.PID), h.Name, h.App, h.Cores, h.Since.Unix()})
	}
	if cur, err := s.power.Get(powerIface, "Hot"); err != nil || !samePids(cur.Value().([]hotProcess), hot) {
		s.power.SetMust(powerIface, "Hot", hot)
	}
}

func samePids(a, b []hotProcess) bool {
	return slices.EqualFunc(a, b, func(x, y hotProcess) bool { return x.PID == y.PID && x.Since == y.Since })
}

func (s *Service) SaveUsage() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		return
	}
	if err := s.usage.Save(s.statePath("usage.json")); err != nil {
		log.Printf("usage: %v", err)
	}
}

type appUsage struct {
	ID          string
	Now         float64
	Recent      float64
	Average     float64
	Significant bool
}

func (o *powerObject) Usage() ([]appUsage, *dbus.Error) {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	out := []appUsage{}
	if o.s.usage == nil {
		return out, nil
	}
	for _, a := range o.s.usage.Apps() {
		out = append(out, appUsage{a.ID, a.Now, a.Recent, a.Average, a.Significant})
	}
	return out, nil
}

type processUsage struct {
	PID   uint32
	Name  string
	Cores float64
}

func (o *powerObject) Processes(app string) ([]processUsage, *dbus.Error) {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	out := []processUsage{}
	if o.s.usage == nil {
		return out, nil
	}
	for _, p := range o.s.usage.Processes(app) {
		out = append(out, processUsage{uint32(p.PID), p.Name, p.Cores})
	}
	return out, nil
}

func newUsage(root, path string, now time.Time) *usage.Tracker {
	t := usage.New(root)
	t.Load(path, now)
	return t
}
