package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	cg      = "sys/fs/cgroup"
	nmd     = cg + "/system.slice/sinty-nmd.service"
	indexer = cg + "/background.slice/indexer.service"
	user    = cg + "/user.slice/user-1001.slice"
	app     = user + "/app-dev.sinty.leafs.scope"
	session = user + "/session-2.scope"
)

type tree map[string]uint64

func (tr tree) write(t *testing.T, root string) {
	t.Helper()
	for dir, usec := range tr {
		put(t, root, dir+"/cpu.stat", fmt.Sprintf("usage_usec %d\nuser_usec 0\nsystem_usec 0", usec))
	}
}

func layout(nmdUsec, indexerUsec, appUsec, sessionUsec, kernelUsec uint64) tree {
	users := appUsec + sessionUsec
	return tree{
		cg:                       nmdUsec + indexerUsec + users + kernelUsec,
		cg + "/system.slice":     nmdUsec,
		nmd:                      nmdUsec,
		cg + "/background.slice": indexerUsec,
		indexer:                  indexerUsec,
		cg + "/user.slice":       users,
		user:                     users,
		app:                      appUsec,
		session:                  sessionUsec,
	}
}

func impact(tr *Tracker) map[string]float64 {
	out := map[string]float64{}
	for _, a := range tr.Apps() {
		out[a.ID] = a.Now
	}
	return out
}

func TestEveryCgroupIsChargedOnce(t *testing.T) {
	root := t.TempDir()
	tr := New(root)
	start := time.Unix(200000, 0)
	layout(0, 0, 0, 0, 0).write(t, root)
	tr.Sample(start)
	layout(1e6, 5e6, 4e6, 2e6, 3e6).write(t, root)
	tr.Sample(start.Add(10 * time.Second))
	got := impact(tr)
	want := map[string]float64{"system/sinty-nmd": 10, "system/indexer": 50, "dev.sinty.leafs": 40, "session": 20, "kernel": 30}
	for id, v := range want {
		if !near(got[id], v) {
			t.Errorf("%s: %v, want %v", id, got[id], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("charged %v", got)
	}
}

func TestOnlyBusyCgroupsAreOpenedPerProcess(t *testing.T) {
	root := t.TempDir()
	put(t, root, app+"/cgroup.procs", "40\n41")
	put(t, root, nmd+"/cgroup.procs", "77")
	stat(t, root, 77, "sinty-nmd", 0, 0, 3)
	tr := New(root)
	start := time.Unix(300000, 0)
	var ticks, usec uint64
	for i := 0; i <= 36; i++ {
		stat(t, root, 40, "bash", 0, ticks, 5)
		stat(t, root, 41, "make", 0, 0, 6)
		layout(uint64(i)*1000, 0, usec, 0, 0).write(t, root)
		tr.Sample(start.Add(time.Duration(i) * 10 * time.Second))
		ticks += 870
		usec += 8.8e6
	}
	if _, ok := tr.procs[77]; ok {
		t.Fatal("an idle service was read process by process")
	}
	hot := tr.Hot(start.Add(360 * time.Second))
	if len(hot) != 1 || hot[0].PID != 40 || hot[0].App != "dev.sinty.leafs" || !near(hot[0].Cores, 0.87) {
		t.Fatalf("hot %+v", hot)
	}
	if !hot[0].Since.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("busy since %v", hot[0].Since)
	}
}

func TestACgroupBornBetweenSamplesKeepsItsWork(t *testing.T) {
	root := t.TempDir()
	tr := New(root)
	start := time.Unix(400000, 0)
	layout(0, 0, 0, 0, 0).write(t, root)
	os.RemoveAll(filepath.Join(root, app))
	tr.Sample(start)
	layout(0, 0, 3e6, 0, 0).write(t, root)
	tr.Sample(start.Add(10 * time.Second))
	got := impact(tr)
	if !near(got["dev.sinty.leafs"], 30) || got["system/user-1001"] != 0 || got["kernel"] != 0 {
		t.Fatalf("charged %v", got)
	}
}

func TestSystemdUserManagersAreWalked(t *testing.T) {
	root := t.TempDir()
	mgr := user + "/user@1001.service"
	editor := mgr + "/app.slice/app-gnome-org.gnome.TextEditor-4242.scope"
	pipewire := mgr + "/session.slice/pipewire.service"
	write := func(e, p uint64) {
		tree{cg: e + p, cg + "/user.slice": e + p, user: e + p, mgr: e + p, mgr + "/app.slice": e, editor: e, mgr + "/session.slice": p, pipewire: p}.write(t, root)
	}
	tr := New(root)
	start := time.Unix(500000, 0)
	write(0, 0)
	tr.Sample(start)
	write(2e6, 1e6)
	tr.Sample(start.Add(10 * time.Second))
	got := impact(tr)
	if !near(got["gnome-org.gnome.TextEditor-4242"], 20) || !near(got["system/pipewire"], 10) || got["system/user@1001"] != 0 {
		t.Fatalf("charged %v", got)
	}
}

func TestGPUIsReadOnlyFromRenderNodesOfApps(t *testing.T) {
	root := t.TempDir()
	put(t, root, app+"/cgroup.procs", "60")
	put(t, root, nmd+"/cgroup.procs", "61")
	for _, pid := range []string{"60", "61"} {
		os.MkdirAll(filepath.Join(root, "proc", pid, "fd"), 0o755)
		os.Symlink("/dev/dri/renderD128", filepath.Join(root, "proc", pid, "fd", "5"))
		os.Symlink("/dev/null", filepath.Join(root, "proc", pid, "fd", "6"))
	}
	gpu := func(ns uint64) {
		for _, pid := range []string{"60", "61"} {
			put(t, root, "proc/"+pid+"/fdinfo/5", fmt.Sprintf("pos:\t0\ndrm-client-id:\t3\ndrm-engine-render:\t%d ns", ns))
		}
	}
	tr := New(root)
	start := time.Unix(600000, 0)
	layout(0, 0, 0, 0, 0).write(t, root)
	gpu(1)
	tr.Sample(start)
	gpu(5e8 + 1)
	tr.Sample(start.Add(10 * time.Second))
	if got := impact(tr); !near(got["dev.sinty.leafs"], 5) || got["system/sinty-nmd"] != 0 {
		t.Fatalf("charged %v", got)
	}
	if c := tr.clients[60]; c == nil || len(c.files) != 1 {
		t.Fatalf("render files %+v", c)
	}
	if _, ok := tr.clients[61]; ok {
		t.Fatal("a service was scanned for the GPU")
	}
}

func TestAskingForAnAppsProcessesMeasuresThem(t *testing.T) {
	root := t.TempDir()
	put(t, root, app+"/cgroup.procs", "40")
	tr := New(root)
	start := time.Unix(700000, 0)
	layout(0, 0, 0, 0, 0).write(t, root)
	stat(t, root, 40, "leafs", 0, 0, 5)
	tr.Sample(start)
	if procs := tr.Processes("dev.sinty.leafs"); len(procs) != 1 || procs[0].Name != "leafs" || procs[0].Cores != 0 {
		t.Fatalf("processes %+v", procs)
	}
	for i := 1; i <= 2; i++ {
		stat(t, root, 40, "leafs", 0, uint64(i)*200, 5)
		layout(0, 0, uint64(i)*2e6, 0, 0).write(t, root)
		tr.Sample(start.Add(time.Duration(i) * 10 * time.Second))
	}
	if procs := tr.Processes("dev.sinty.leafs"); len(procs) != 1 || !near(procs[0].Cores, 0.2) {
		t.Fatalf("processes %+v", procs)
	}
	tr.Sample(start.Add(10 * time.Minute))
	if len(tr.procs) != 0 {
		t.Fatalf("still watching %+v", tr.procs)
	}
}
