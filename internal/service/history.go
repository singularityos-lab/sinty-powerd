package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/singularityos-lab/sinty-powerd/internal/supply"
)

type sample struct {
	Time  uint32  `json:"t"`
	Value float64 `json:"v"`
	State uint32  `json:"s"`
}

// history keeps the four series UPower records for a battery.
type history struct {
	Series map[string][]sample `json:"series"`
}

const maxSamples = 4096

func newHistory() *history { return &history{Series: map[string][]sample{}} }

func (h *history) record(d *supply.Device, now time.Time) {
	t := uint32(now.Unix())
	h.add("charge", t, d.Percentage, d.State)
	h.add("rate", t, d.EnergyRate, d.State)
	h.add("time-full", t, float64(d.TimeToFull), d.State)
	h.add("time-empty", t, float64(d.TimeToEmpty), d.State)
}

func (h *history) add(kind string, t uint32, v float64, state uint32) {
	s := h.Series[kind]
	if n := len(s); n > 0 && s[n-1].Value == v && s[n-1].State == state {
		return
	}
	s = append(s, sample{t, v, state})
	if len(s) > maxSamples {
		s = s[len(s)-maxSamples:]
	}
	h.Series[kind] = s
}

// get returns the samples of kind newer than timespan seconds, thinned to at
// most resolution points, newest first as UPower returns them.
func (h *history) get(kind string, timespan, resolution uint32, now time.Time) []sample {
	var picked []sample
	from := uint32(0)
	if timespan > 0 {
		from = uint32(now.Unix()) - timespan
	}
	for _, s := range h.Series[kind] {
		if s.Time >= from {
			picked = append(picked, s)
		}
	}
	if resolution > 0 && uint32(len(picked)) > resolution {
		thin := make([]sample, 0, resolution)
		step := float64(len(picked)) / float64(resolution)
		for i := uint32(0); i < resolution; i++ {
			thin = append(thin, picked[int(float64(i)*step)])
		}
		picked = thin
	}
	out := make([]sample, len(picked))
	for i, s := range picked {
		out[len(picked)-1-i] = s
	}
	return out
}

func loadHistory(path string) *history {
	h := newHistory()
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, h)
		if h.Series == nil {
			h.Series = map[string][]sample{}
		}
	}
	return h
}

func (h *history) save(path string) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
