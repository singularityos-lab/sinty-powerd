package brightness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Entry struct {
	Level  int
	Method string
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

type Store struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
}

func Open(path string) *Store {
	s := &Store{path: path, entries: map[string]Entry{}}
	if data, err := os.ReadFile(path); err == nil {
		for k, e := range Parse(string(data)) {
			s.entries[k] = e
		}
	}
	return s
}
func Parse(text string) map[string]Entry {
	out := map[string]Entry{}
	for i, line := range strings.Split(text, "\n") {
		if i >= 64 {
			break
		}
		f := strings.Fields(line)
		if len(f) != 3 || !keyPattern.MatchString(f[0]) {
			continue
		}
		level, err := strconv.Atoi(f[1])
		if err != nil || level < 0 || level > 1000 {
			continue
		}
		switch f[2] {
		case "backlight", "ddc", "software":
		default:
			continue
		}
		out[f[0]] = Entry{Level: level, Method: f[2]}
	}
	return out
}
func (s *Store) Get(key string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return e, ok
}
func (s *Store) Merge(text string) error {
	incoming := Parse(text)
	if len(incoming) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range incoming {
		s.entries[k] = e
	}
	if len(s.entries) > 64 {
		return fmt.Errorf("too many displays")
	}
	return s.write()
}

func (s *Store) write() error {
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		e := s.entries[k]
		fmt.Fprintf(&b, "%s %d %s\n", k, e.Level, e.Method)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
func EdidID(edid []byte) string {
	if len(edid) < 18 {
		return ""
	}
	h := uint64(0xcbf29ce484222325)
	for _, b := range edid[8:18] {
		h ^= uint64(b)
		h *= 0x100000001b3
	}
	return fmt.Sprintf("%016x", h)
}
func (s *Store) Restore(root string) int {
	restored := 0
	lights, _ := filepath.Glob(filepath.Join(root, "sys/class/backlight/*"))
	for _, light := range lights {
		target, err := os.Readlink(filepath.Join(light, "device"))
		if err != nil {
			continue
		}
		connector := filepath.Base(target)
		edid, err := os.ReadFile(filepath.Join(root, "sys/class/drm", connector, "edid"))
		key := EdidID(edid)
		if err != nil || key == "" {
			if i := strings.Index(connector, "-"); i > 0 {
				key = connector[i+1:]
			}
		}
		e, ok := s.Get(key)
		if !ok || e.Method != "backlight" {
			continue
		}
		maxData, err := os.ReadFile(filepath.Join(light, "max_brightness"))
		if err != nil {
			continue
		}
		max, err := strconv.Atoi(strings.TrimSpace(string(maxData)))
		if err != nil || max <= 0 {
			continue
		}
		raw := e.Level * max / 1000
		if raw < 1 {
			raw = 1
		}
		if os.WriteFile(filepath.Join(light, "brightness"), []byte(strconv.Itoa(raw)), 0o644) == nil {
			restored++
		}
	}
	return restored
}
