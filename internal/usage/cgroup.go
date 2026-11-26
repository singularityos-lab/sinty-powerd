package usage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	watchFor    = 2 * time.Minute
	minResidual = 0.001
)

type member struct {
	dir       string
	recursive bool
}

type client struct {
	app   string
	files []string
	busy  uint64
}

func (t *Tracker) cgroupRoot() string { return filepath.Join(t.root, "sys/fs/cgroup") }

func (t *Tracker) cgroups() bool {
	_, err := os.Stat(filepath.Join(t.cgroupRoot(), "cpu.stat"))
	return err == nil
}

func trimUnit(name string) string {
	for _, suffix := range []string{".service", ".scope", ".slice"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

func classify(rel string) (id string, scoped, branch bool) {
	if rel == "" {
		return "kernel", false, true
	}
	if id, ok := AppOf(rel); ok {
		return id, true, false
	}
	base := filepath.Base(rel)
	branch = strings.HasSuffix(base, ".slice") || (strings.HasPrefix(base, "user@") && strings.HasSuffix(base, ".service"))
	return "system/" + trimUnit(base), false, branch
}

type walk struct {
	now     time.Time
	elapsed float64
	impact  map[string]float64
	busy    []string
	scoped  map[string][]string
	dirs    map[string][]member
}

func (t *Tracker) sampleCgroups(now time.Time, elapsed float64) map[string]float64 {
	w := &walk{now: now, elapsed: elapsed, impact: map[string]float64{}, scoped: map[string][]string{}, dirs: map[string][]member{}}
	for _, g := range t.groups {
		g.seen = false
	}
	t.visit(w, t.cgroupRoot(), "")
	for key, g := range t.groups {
		if !g.seen && strings.HasPrefix(key, "/") {
			delete(t.groups, key)
		}
	}
	t.dirs = w.dirs

	for _, p := range t.procs {
		p.seen = false
	}
	descended := map[string]bool{}
	for _, id := range w.busy {
		descended[id] = true
	}
	for id, until := range t.watched {
		if now.After(until) {
			delete(t.watched, id)
		} else {
			descended[id] = true
		}
	}
	for id := range descended {
		t.descend(id, now)
	}
	for pid, p := range t.procs {
		if !p.seen {
			delete(t.procs, pid)
		}
	}

	t.sampleGPU(w)
	return w.impact
}

func (t *Tracker) visit(w *walk, dir, rel string) float64 {
	id, scoped, branch := classify(rel)
	usec, ok := cpuUsec(dir)
	if !ok {
		return 0
	}
	g := t.group(dir)
	fresh := !g.measured && !t.lastAt.IsZero()
	g.seen = true
	var delta float64
	if g.measured || fresh {
		delta = float64(usec - min(usec, g.cpu))
	}
	io := uint64(0)
	if scoped {
		io = ioBytes(dir)
	}
	var ioDelta float64
	if g.measured || fresh {
		ioDelta = float64(io - min(io, g.io))
	}
	g.cpu, g.io, g.measured = usec, io, true

	if !branch {
		w.dirs[id] = append(w.dirs[id], member{dir, true})
		if scoped {
			w.scoped[id] = append(w.scoped[id], dir)
		}
		if w.elapsed > 0 {
			cores := delta / 1e6 / w.elapsed
			w.impact[id] += 100*cores + ioDelta/1e6/w.elapsed
			if cores >= busyCores {
				w.busy = append(w.busy, id)
			}
		}
		return delta
	}

	w.dirs[id] = append(w.dirs[id], member{dir, false})
	var children float64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			children += t.visit(w, filepath.Join(dir, e.Name()), filepath.Join(rel, e.Name()))
		}
	}
	if w.elapsed > 0 {
		if cores := (delta - children) / 1e6 / w.elapsed; cores >= minResidual {
			w.impact[id] += 100 * cores
		}
	}
	return delta
}

func pids(dir string, recursive bool, out []int) []int {
	for _, line := range strings.Split(read(filepath.Join(dir, "cgroup.procs")), "\n") {
		if pid, err := strconv.Atoi(line); err == nil {
			out = append(out, pid)
		}
	}
	if !recursive {
		return out
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			out = pids(filepath.Join(dir, e.Name()), true, out)
		}
	}
	return out
}

func (t *Tracker) descend(id string, now time.Time) {
	var list []int
	for _, m := range t.dirs[id] {
		list = pids(m.dir, m.recursive, list)
	}
	for _, pid := range list {
		name, flags, ticks, start, ok := parseStat(read(filepath.Join(t.root, "proc", strconv.Itoa(pid), "stat")))
		if !ok || (id != "kernel" && flags&kernelThread != 0) {
			continue
		}
		p := t.procs[pid]
		if p == nil || p.start != start || p.app != id {
			p = &proc{start: start, app: id, scoped: true, ticks: ticks, at: now}
			t.procs[pid] = p
		}
		p.name = name
		p.seen = true
		if now.IsZero() {
			continue
		}
		if span := now.Sub(p.at).Seconds(); !p.at.IsZero() && span > 0 {
			p.cores = float64(ticks-min(ticks, p.ticks)) / ticksPerSecond / span
			if p.cores >= busyCores {
				if p.busySince.IsZero() {
					p.busySince = p.at
				}
			} else {
				p.busySince = time.Time{}
			}
		}
		p.ticks, p.at = ticks, now
	}
}

func (t *Tracker) watch(app string) {
	if _, ok := t.watched[app]; !ok {
		t.descend(app, time.Time{})
	}
	t.watched[app] = t.lastAt.Add(watchFor)
}

func drmFiles(procDir string) []string {
	fds, err := os.ReadDir(filepath.Join(procDir, "fd"))
	if err != nil {
		return nil
	}
	var out []string
	for _, fd := range fds {
		if target, err := os.Readlink(filepath.Join(procDir, "fd", fd.Name())); err == nil && strings.HasPrefix(target, "/dev/dri/") {
			out = append(out, filepath.Join(procDir, "fdinfo", fd.Name()))
		}
	}
	return out
}

func drmBusy(files []string) (uint64, bool) {
	clients := map[string]uint64{}
	for _, f := range files {
		data := read(f)
		if !strings.Contains(data, "drm-engine-") {
			continue
		}
		id := ""
		var busy uint64
		for _, line := range strings.Split(data, "\n") {
			k, v, _ := strings.Cut(line, ":")
			v = strings.TrimSpace(v)
			if k == "drm-client-id" {
				id = v
			} else if strings.HasPrefix(k, "drm-engine-") && !strings.HasPrefix(k, "drm-engine-capacity-") {
				n, _ := strconv.ParseUint(strings.TrimSuffix(v, " ns"), 10, 64)
				busy += n
			}
		}
		clients[id] = busy
	}
	var total uint64
	for _, v := range clients {
		total += v
	}
	return total, len(clients) > 0
}

func (t *Tracker) sampleGPU(w *walk) {
	if w.now.Sub(t.gpuScan) >= gpuRescan {
		t.gpuScan = w.now
		found := map[int]*client{}
		for id, dirs := range w.scoped {
			var list []int
			for _, dir := range dirs {
				list = pids(dir, true, list)
			}
			for _, pid := range list {
				files := drmFiles(filepath.Join(t.root, "proc", strconv.Itoa(pid)))
				if len(files) == 0 {
					continue
				}
				c := t.clients[pid]
				if c == nil || c.app != id {
					c = &client{app: id}
				}
				c.files = files
				found[pid] = c
			}
		}
		t.clients = found
	}
	for pid, c := range t.clients {
		busy, ok := drmBusy(c.files)
		if !ok {
			delete(t.clients, pid)
			continue
		}
		if w.elapsed > 0 && c.busy > 0 && busy >= c.busy {
			w.impact[c.app] += 100 * float64(busy-c.busy) / 1e9 / w.elapsed
		}
		c.busy = busy
	}
}
