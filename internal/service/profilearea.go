package service

import (
	"encoding/json"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/singularityos-lab/sinty-powerd/internal/profiles"
)

const (
	areaIface = "dev.sinty.ProfileArea1"
	areaPath  = dbus.ObjectPath("/dev/sinty/ProfileArea/energy")
	areaKey   = "mode"
)

type energyArea struct{ s *Service }

type areaChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type areaOption struct {
	Key      string       `json:"key"`
	Label    string       `json:"label"`
	Subtitle string       `json:"subtitle"`
	Kind     string       `json:"kind"`
	Min      float64      `json:"min"`
	Max      float64      `json:"max"`
	ReadOnly bool         `json:"read-only"`
	Choices  []areaChoice `json:"choices"`
}

type areaInfo struct {
	ID      string       `json:"id"`
	Label   string       `json:"label"`
	Icon    string       `json:"icon"`
	Options []areaOption `json:"options"`
}

func (s *Service) energyChoices() []areaChoice {
	all := []areaChoice{
		{profiles.PowerSaver, "Power Saver"},
		{profiles.Balanced, "Balanced"},
		{profiles.Performance, "Performance"},
	}
	var out []areaChoice
	for _, c := range all {
		if s.backend.Supports(c.Value) {
			out = append(out, c)
		}
	}
	return out
}

func (s *Service) exportEnergyArea() error {
	obj := &energyArea{s}
	if err := s.conn.Export(obj, areaPath, areaIface); err != nil {
		return err
	}
	node := &introspect.Node{
		Name: string(areaPath),
		Interfaces: []introspect.Interface{introspect.IntrospectData, {
			Name:    areaIface,
			Methods: introspect.Methods(obj),
			Signals: []introspect.Signal{{Name: "Changed", Args: []introspect.Arg{{Name: "keys", Type: "as"}}}},
		}},
	}
	return s.conn.Export(introspect.NewIntrospectable(node), areaPath, "org.freedesktop.DBus.Introspectable")
}

func (s *Service) energyChanged() {
	if s.conn != nil {
		s.conn.Emit(areaPath, areaIface+".Changed", []string{areaKey})
	}
}

func (o *energyArea) Describe() (string, *dbus.Error) {
	info := areaInfo{ID: "energy", Label: "Energy", Icon: "power-profile-balanced-symbolic", Options: []areaOption{{
		Key: areaKey, Label: "Energy Mode", Kind: "choice", Choices: o.s.energyChoices(),
	}}}
	data, err := json.Marshal(info)
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	return string(data), nil
}

func (o *energyArea) Snapshot(keys []string) (map[string]string, *dbus.Error) {
	o.s.mu.Lock()
	chosen := o.s.chosen
	o.s.mu.Unlock()
	out := map[string]string{}
	for _, k := range keys {
		if k == areaKey {
			v, _ := json.Marshal(chosen)
			out[k] = string(v)
		}
	}
	return out, nil
}

func (o *energyArea) Apply(sender dbus.Sender, values map[string]string) *dbus.Error {
	s := o.s
	if uid, ok := s.authorized(sender); !ok {
		return denied("change the energy mode", uid)
	}
	raw, ok := values[areaKey]
	if !ok {
		return nil
	}
	var mode string
	if err := json.Unmarshal([]byte(raw), &mode); err != nil || !s.backend.Supports(mode) {
		return dbus.MakeFailedError(errInvalidProfile(raw))
	}
	go s.choose(mode)
	return nil
}
