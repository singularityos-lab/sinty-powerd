package usage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func put(t *testing.T, root, path, v string) {
	t.Helper()
	p := filepath.Join(root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(v+"\n"), 0o644)
}

func stat(t *testing.T, root string, pid int, name string, flags, ticks, start uint64) {
	t.Helper()
	put(t, root, fmt.Sprintf("proc/%d/stat", pid),
		fmt.Sprintf("%d (%s) R 1 1 1 0 -1 %d 0 0 0 0 %d 0 0 0 20 0 1 0 %d 0 0", pid, name, flags, ticks, start))
}

const leafs = "sys/fs/cgroup/user.slice/user-1001.slice/app-dev.sinty.leafs.scope"

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAppOf(t *testing.T) {
	cases := map[string]string{
		"/user.slice/user-1001.slice/app-dev.sinty.leafs.scope/init/init":    "dev.sinty.leafs",
		"/user.slice/user-1001.slice/app-cpak-c7298-com.google.Chrome.scope": "com.google.Chrome",
		"/user.slice/user-1001.slice/session-2.scope":                        "session",
		"/machine.slice/vm-1.scope":                                          "machines",
	}
	for path, want := range cases {
		if got, ok := AppOf(path); !ok || got != want {
			t.Errorf("%s: %q %v", path, got, ok)
		}
	}
	if _, ok := AppOf("/"); ok {
		t.Error("the root cgroup is an app")
	}
}

func TestBusyLoopInsideAVisibleAppBecomesHot(t *testing.T) {
	root := t.TempDir()
	put(t, root, leafs+"/cpu.stat", "usage_usec 0")
	put(t, root, "proc/40/cgroup", "0::/user.slice/user-1001.slice/app-dev.sinty.leafs.scope/init/init")
	put(t, root, "proc/41/cgroup", "0::/user.slice/user-1001.slice/app-dev.sinty.leafs.scope/init/init")
	tr := New(root)
	start := time.Unix(100000, 0)
	var ticks, usec uint64
	for i := 0; i <= 36; i++ {
		now := start.Add(time.Duration(i) * 10 * time.Second)
		stat(t, root, 40, "bash", 0, ticks, 5)
		stat(t, root, 41, "make", 0, min(uint64(i), 12)*100, 6)
		put(t, root, leafs+"/cpu.stat", fmt.Sprintf("usage_usec %d", usec))
		tr.Sample(now)
		ticks += 870
		usec += 9700000
	}
	now := start.Add(36 * 10 * time.Second)
	hot := tr.Hot(now)
	if len(hot) != 1 || hot[0].PID != 40 || hot[0].Name != "bash" || hot[0].App != "dev.sinty.leafs" {
		t.Fatalf("hot %+v", hot)
	}
	if !near(hot[0].Cores, 0.87) || !hot[0].Since.Equal(start) {
		t.Fatalf("hot %+v", hot[0])
	}
	apps := tr.Apps()
	if len(apps) == 0 || apps[0].ID != "dev.sinty.leafs" || !apps[0].Significant {
		t.Fatalf("apps %+v", apps)
	}
	if !near(apps[0].Now, 97) {
		t.Fatalf("leafs now %v", apps[0].Now)
	}
	procs := tr.Processes("dev.sinty.leafs")
	if len(procs) != 2 || procs[0].PID != 40 {
		t.Fatalf("processes %+v", procs)
	}
}

func TestShortBurstIsNotHot(t *testing.T) {
	root := t.TempDir()
	put(t, root, "proc/50/cgroup", "0::/")
	tr := New(root)
	start := time.Unix(200000, 0)
	var ticks uint64
	for i := 0; i <= 40; i++ {
		stat(t, root, 50, "cc1", 0, ticks, 9)
		tr.Sample(start.Add(time.Duration(i) * 10 * time.Second))
		if i < 20 || i > 22 {
			ticks += 1000
		}
	}
	if hot := tr.Hot(start.Add(400 * time.Second)); len(hot) != 0 {
		t.Fatalf("a busy process that paused counted as hot: %+v", hot)
	}
}

func TestSystemProcessesAndKernelThreads(t *testing.T) {
	root := t.TempDir()
	put(t, root, "proc/7/cgroup", "0::/")
	put(t, root, "proc/8/cgroup", "0::/")
	tr := New(root)
	start := time.Unix(300000, 0)
	for i := 0; i <= 60; i++ {
		stat(t, root, 7, "sinty-nmd", 0, uint64(i)*500, 3)
		stat(t, root, 8, "kworker/0:1", kernelThread, uint64(i)*1000, 2)
		tr.Sample(start.Add(time.Duration(i) * 10 * time.Second))
	}
	got := map[string]App{}
	for _, a := range tr.Apps() {
		got[a.ID] = a
	}
	if !near(got["system/sinty-nmd"].Now, 50) || !near(got["kernel"].Now, 100) {
		t.Fatalf("apps %+v", got)
	}
	if !got["system/sinty-nmd"].Significant {
		t.Fatalf("a system service busy for ten minutes not flagged: %+v", got["system/sinty-nmd"])
	}
	if got["kernel"].Significant {
		t.Fatal("kernel threads flagged")
	}
}

func TestPidReuseStartsOver(t *testing.T) {
	root := t.TempDir()
	put(t, root, "proc/9/cgroup", "0::/")
	tr := New(root)
	start := time.Unix(400000, 0)
	stat(t, root, 9, "old", 0, 90000, 1)
	tr.Sample(start)
	stat(t, root, 9, "new", 0, 10, 77)
	tr.Sample(start.Add(10 * time.Second))
	p := tr.Processes("system/new")
	if len(p) != 1 || !near(p[0].Cores, 0) {
		t.Fatalf("processes %+v", p)
	}
}

func TestGPUTimeCountsOncePerClient(t *testing.T) {
	root := t.TempDir()
	put(t, root, "proc/60/cgroup", "0::/")
	tr := New(root)
	start := time.Unix(500000, 0)
	fdinfo := func(ns uint64) {
		for _, fd := range []string{"5", "6"} {
			put(t, root, "proc/60/fdinfo/"+fd, fmt.Sprintf("pos:\t0\ndrm-driver:\ti915\ndrm-client-id:\t12\ndrm-engine-render:\t%d ns\ndrm-engine-capacity-render:\t1", ns))
		}
		put(t, root, "proc/60/fdinfo/1", "pos:\t0\nflags:\t02")
	}
	stat(t, root, 60, "labwc", 0, 0, 4)
	fdinfo(1000)
	tr.Sample(start)
	fdinfo(2000)
	tr.Sample(start.Add(10 * time.Second))
	stat(t, root, 60, "labwc", 0, 0, 4)
	fdinfo(5000000000 + 2000)
	tr.Sample(start.Add(20 * time.Second))
	if a := tr.Apps(); len(a) != 1 || !near(a[0].Now, 50) {
		t.Fatalf("apps %+v", a)
	}
}

func TestTwelveHourAverageSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	put(t, root, leafs+"/cpu.stat", "usage_usec 0")
	tr := New(root)
	start := time.Unix(600000, 0)
	tr.Sample(start)
	put(t, root, leafs+"/cpu.stat", fmt.Sprintf("usage_usec %d", 3600*1000000))
	tr.Sample(start.Add(time.Hour))
	path := filepath.Join(root, "state/usage.json")
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("unchanged usage was written again")
	}
	tr.dirty = true
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	again := New(root)
	again.Load(path, start.Add(2*time.Hour))
	a := again.Apps()
	if len(a) != 1 || !near(a[0].Average, 100.0/12) {
		t.Fatalf("apps %+v", a)
	}
	gone := New(root)
	gone.Load(path, start.Add(14*time.Hour))
	if a := gone.Apps(); len(a) != 0 {
		t.Fatalf("older than twelve hours kept: %+v", a)
	}
}
