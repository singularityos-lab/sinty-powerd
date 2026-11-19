package usage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ticksPerSecond = 100
	kernelThread   = 0x00200000
	busyCores      = 0.8
	recentWindow   = 5 * time.Minute
	averageWindow  = 12 * time.Hour
	bucketSpan     = 10 * time.Minute
	gpuRescan      = time.Minute
	significant    = 25
)

const HotAfter = 5 * time.Minute

type App struct {
	ID          string
	Now         float64
	Recent      float64
	Average     float64
	Significant bool
}

type Hot struct {
	PID   int
	Name  string
	App   string
	Cores float64
	Since time.Time
}

type Process struct {
	PID   int
	Name  string
	Cores float64
}

type group struct {
	cpu, io  uint64
	measured bool
	now      float64
	recent   float64
	buckets  map[int64]float64
	seen     bool
}

type proc struct {
	start     uint64
	name      string
	app       string
	scoped    bool
	ticks     uint64
	gpu       uint64
	cores     float64
	busySince time.Time
	seen      bool
}

type Tracker struct {
	root    string
	groups  map[string]*group
	procs   map[int]*proc
	gpuPids map[int]bool
	gpuScan time.Time
	lastAt  time.Time
}

func New(root string) *Tracker {
	return &Tracker{root: root, groups: map[string]*group{}, procs: map[int]*proc{}, gpuPids: map[int]bool{}}
}

func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func AppOf(cgroup string) (string, bool) {
	for _, part := range strings.Split(cgroup, "/") {
		switch {
		case strings.HasPrefix(part, "app-") && strings.HasSuffix(part, ".scope"):
			id := strings.TrimSuffix(strings.TrimPrefix(part, "app-"), ".scope")
			if rest, ok := strings.CutPrefix(id, "cpak-"); ok {
				if _, after, found := strings.Cut(rest, "-"); found {
					id = after
				}
			}
			return id, true
		case strings.HasPrefix(part, "session-") && strings.HasSuffix(part, ".scope"):
			return "session", true
		case part == "machine.slice":
			return "machines", true
		}
	}
	return "", false
}

func (t *Tracker) scopes() map[string]string {
	out := map[string]string{}
	base := filepath.Join(t.root, "sys/fs/cgroup")
	users, _ := filepath.Glob(filepath.Join(base, "user.slice/user-*.slice/*.scope"))
	machines, _ := filepath.Glob(filepath.Join(base, "machine.slice"))
	for _, dir := range append(users, machines...) {
		rel := strings.TrimPrefix(dir, base)
		if id, ok := AppOf(rel); ok {
			out[dir] = id
		}
	}
	return out
}

func cpuUsec(dir string) (uint64, bool) {
	for _, line := range strings.Split(read(filepath.Join(dir, "cpu.stat")), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			n, err := strconv.ParseUint(v, 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

func ioBytes(dir string) uint64 {
	var total uint64
	for _, line := range strings.Split(read(filepath.Join(dir, "io.stat")), "\n") {
		for _, f := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(f, "rbytes="); ok {
				n, _ := strconv.ParseUint(v, 10, 64)
				total += n
			} else if v, ok := strings.CutPrefix(f, "wbytes="); ok {
				n, _ := strconv.ParseUint(v, 10, 64)
				total += n
			}
		}
	}
	return total
}

func parseStat(data string) (name string, flags, ticks, start uint64, ok bool) {
	open, end := strings.IndexByte(data, '('), strings.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return "", 0, 0, 0, false
	}
	name = data[open+1 : end]
	f := strings.Fields(data[end+1:])
	if len(f) < 20 {
		return "", 0, 0, 0, false
	}
	flags, _ = strconv.ParseUint(f[6], 10, 64)
	utime, _ := strconv.ParseUint(f[11], 10, 64)
	stime, _ := strconv.ParseUint(f[12], 10, 64)
	start, _ = strconv.ParseUint(f[19], 10, 64)
	return name, flags, utime + stime, start, true
}

func (t *Tracker) gpuTime(pid int) (uint64, bool) {
	fds, _ := filepath.Glob(filepath.Join(t.root, "proc", strconv.Itoa(pid), "fdinfo/*"))
	clients := map[string]uint64{}
	for _, fd := range fds {
		data := read(fd)
		if !strings.Contains(data, "drm-engine-") {
			continue
		}
		client := ""
		var busy uint64
		for _, line := range strings.Split(data, "\n") {
			k, v, _ := strings.Cut(line, ":")
			v = strings.TrimSpace(v)
			if k == "drm-client-id" {
				client = v
			} else if strings.HasPrefix(k, "drm-engine-") && !strings.HasPrefix(k, "drm-engine-capacity-") {
				n, _ := strconv.ParseUint(strings.TrimSuffix(v, " ns"), 10, 64)
				busy += n
			}
		}
		clients[client] = busy
	}
	var total uint64
	for _, v := range clients {
		total += v
	}
	return total, len(clients) > 0
}

func (t *Tracker) group(id string) *group {
	g := t.groups[id]
	if g == nil {
		g = &group{buckets: map[int64]float64{}}
		t.groups[id] = g
	}
	return g
}

func (t *Tracker) Sample(now time.Time) {
	elapsed := 0.0
	if !t.lastAt.IsZero() {
		elapsed = now.Sub(t.lastAt).Seconds()
	}
	rescanGPU := now.Sub(t.gpuScan) >= gpuRescan
	if rescanGPU {
		t.gpuScan = now
		t.gpuPids = map[int]bool{}
	}
	impact := map[string]float64{}

	for _, p := range t.procs {
		p.seen = false
	}
	dirs, _ := filepath.Glob(filepath.Join(t.root, "proc/[0-9]*"))
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		name, flags, ticks, start, ok := parseStat(read(filepath.Join(d, "stat")))
		if !ok {
			continue
		}
		p := t.procs[pid]
		if p == nil || p.start != start {
			p = &proc{start: start, name: name, ticks: ticks}
			if flags&kernelThread != 0 {
				p.app = "kernel"
			} else {
				cg := read(filepath.Join(d, "cgroup"))
				_, path, _ := strings.Cut(cg, "::")
				if id, ok := AppOf(path); ok {
					p.app, p.scoped = id, true
				} else {
					p.app = "system/" + name
				}
			}
			t.procs[pid] = p
		}
		p.seen = true
		p.name = name
		if elapsed > 0 {
			p.cores = float64(ticks-min(ticks, p.ticks)) / ticksPerSecond / elapsed
			if !p.scoped {
				impact[p.app] += 100 * p.cores
			}
			if p.cores >= busyCores {
				if p.busySince.IsZero() {
					p.busySince = t.lastAt
				}
			} else {
				p.busySince = time.Time{}
			}
		}
		p.ticks = ticks
		if rescanGPU || t.gpuPids[pid] {
			if busy, ok := t.gpuTime(pid); ok {
				t.gpuPids[pid] = true
				if elapsed > 0 && p.gpu > 0 && busy >= p.gpu {
					impact[p.app] += 100 * float64(busy-p.gpu) / 1e9 / elapsed
				}
				p.gpu = busy
			}
		}
	}
	for pid, p := range t.procs {
		if !p.seen {
			delete(t.procs, pid)
			delete(t.gpuPids, pid)
		}
	}

	for _, g := range t.groups {
		g.seen = false
	}
	for dir, id := range t.scopes() {
		g := t.group(dir)
		g.seen = true
		usec, ok := cpuUsec(dir)
		if !ok {
			continue
		}
		io := ioBytes(dir)
		if g.measured && elapsed > 0 {
			impact[id] += 100*float64(usec-min(usec, g.cpu))/1e6/elapsed + float64(io-min(io, g.io))/1e6/elapsed
		}
		g.cpu, g.io, g.measured = usec, io, true
	}
	for key, g := range t.groups {
		if !g.seen && strings.HasPrefix(key, "/") {
			delete(t.groups, key)
		}
	}

	if elapsed > 0 {
		keep := now.Add(-averageWindow).Unix() / int64(bucketSpan.Seconds())
		bucket := now.Unix() / int64(bucketSpan.Seconds())
		weight := 1 - math.Exp(-elapsed/recentWindow.Seconds())
		for id := range impact {
			t.group("app:" + id)
		}
		for key, g := range t.groups {
			id, ok := strings.CutPrefix(key, "app:")
			if !ok {
				continue
			}
			g.now = impact[id]
			g.recent += (g.now - g.recent) * weight
			if g.now > 0 {
				g.buckets[bucket] += g.now * elapsed
			}
			for b := range g.buckets {
				if b <= keep {
					delete(g.buckets, b)
				}
			}
			if len(g.buckets) == 0 && g.recent < 0.01 {
				delete(t.groups, key)
			}
		}
	}
	t.lastAt = now
}

func (t *Tracker) Apps() []App {
	var out []App
	for key, g := range t.groups {
		id, ok := strings.CutPrefix(key, "app:")
		if !ok {
			continue
		}
		var total float64
		for _, v := range g.buckets {
			total += v
		}
		out = append(out, App{
			ID:          id,
			Now:         g.now,
			Recent:      g.recent,
			Average:     total / averageWindow.Seconds(),
			Significant: g.recent >= significant && id != "kernel",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Recent != out[j].Recent {
			return out[i].Recent > out[j].Recent
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (t *Tracker) Hot(now time.Time) []Hot {
	var out []Hot
	for pid, p := range t.procs {
		if p.busySince.IsZero() || now.Sub(p.busySince) < HotAfter || p.app == "kernel" {
			continue
		}
		out = append(out, Hot{PID: pid, Name: p.name, App: p.app, Cores: p.cores, Since: p.busySince})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func (t *Tracker) Processes(app string) []Process {
	var out []Process
	for pid, p := range t.procs {
		if p.app == app {
			out = append(out, Process{PID: pid, Name: p.name, Cores: p.cores})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cores != out[j].Cores {
			return out[i].Cores > out[j].Cores
		}
		return out[i].PID < out[j].PID
	})
	return out
}

type saved struct {
	Buckets map[string]map[int64]float64 `json:"buckets"`
}

func (t *Tracker) Save(path string) error {
	s := saved{Buckets: map[string]map[int64]float64{}}
	for key, g := range t.groups {
		if id, ok := strings.CutPrefix(key, "app:"); ok && len(g.buckets) > 0 {
			s.Buckets[id] = g.buckets
		}
	}
	data, err := json.Marshal(s)
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

func (t *Tracker) Load(path string, now time.Time) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var s saved
	if json.Unmarshal(data, &s) != nil {
		return
	}
	keep := now.Add(-averageWindow).Unix() / int64(bucketSpan.Seconds())
	for id, buckets := range s.Buckets {
		for b, v := range buckets {
			if b > keep && v > 0 {
				t.group("app:" + id).buckets[b] += v
			}
		}
	}
}
