// sinty-powerd is the power daemon of SintyOS. It publishes the batteries and
// power supplies as UPower does, switches the power profiles as
// power-profiles-daemon does and keeps the processor out of thermal
// throttling as thermald does, in one process.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/singularityos-lab/sinty-powerd/internal/devices"
	"github.com/singularityos-lab/sinty-powerd/internal/diagnose"
	"github.com/singularityos-lab/sinty-powerd/internal/events"
	"github.com/singularityos-lab/sinty-powerd/internal/policy"
	"github.com/singularityos-lab/sinty-powerd/internal/service"
	"github.com/singularityos-lab/sinty-powerd/internal/supply"
)

func main() {
	root := flag.String("root", "/", "root of the sysfs tree to read")
	diag := flag.Int("diagnose", 0, "print what keeps the machine awake over this many seconds and exit")
	formFlag := flag.String("form", "", "kind of device: laptop, desktop, phone, tv or headless; detected when empty")
	state := flag.String("state", "/var/lib/sinty-powerd", "directory for the profile, charge threshold and battery history")
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("sinty-powerd: ")
	var syncKernel func()
	*root, syncKernel = platform(*root)
	if os.Getenv("DBUS_SYSTEM_BUS_ADDRESS") == "" {
		os.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/run/dbus/system_bus_socket")
	}

	if *diag > 0 {
		var awake []string
		for _, d := range devices.New(*root).Awake() {
			awake = append(awake, d.Path+" "+d.Driver+" "+d.Reason)
		}
		report := diagnose.Report(*root, time.Duration(*diag)*time.Second, awake)
		for _, k := range []string{"wakers", "wakeup-sources", "idle", "devices-awake", "suspend"} {
			fmt.Println(k + ":")
			for _, line := range report[k] {
				fmt.Println("  " + line)
			}
		}
		return
	}
	form, ok := policy.ParseForm(*formFlag)
	if !ok {
		form = policy.DetectForm(*root)
	}
	log.Printf("device form: %s", form)

	var s *service.Service
	var err error
	for i := 0; ; i++ {
		s, err = service.New(service.Config{Root: *root, StateDir: *state, Policy: supply.DefaultPolicy, Form: form})
		if err == nil {
			break
		}
		if i%10 == 0 {
			log.Printf("system bus not ready: %v", err)
		}
		time.Sleep(time.Second)
	}
	if n := s.RestoreBrightness(); n > 0 {
		log.Printf("restored the brightness of %d panels", n)
	}
	s.Refresh()
	s.Start()
	go s.WatchHolders()
	go s.WatchLogin()

	refresh := make(chan struct{}, 1)
	poke := func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	}
	if err := events.PowerSupplyChanges(poke); err != nil {
		log.Printf("power supply events: %v", err)
	}
	added := make(chan struct{}, 1)
	if err := events.DevicesAdded(func() {
		select {
		case added <- struct{}{}:
		default:
		}
	}); err != nil {
		log.Printf("device events: %v", err)
	}
	if !events.Lid(func(closed bool) { s.SetLid(true, closed) }) {
		s.SetLid(false, false)
	}
	log.Print("serving org.freedesktop.UPower and the power profiles")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	poll := time.NewTimer(s.PollInterval())
	var thermal <-chan time.Time
	thermalTimer := func() {
		if d := s.ThermalInterval(); d > 0 {
			thermal = time.After(d)
		}
	}
	thermalTimer()
	save := time.NewTicker(10 * time.Minute)
	for {
		select {
		case <-refresh:
			time.Sleep(200 * time.Millisecond)
			syncKernel()
			s.Refresh()
		case <-added:
			time.Sleep(time.Second)
			s.DevicesAdded()
		case <-poll.C:
			syncKernel()
			s.Refresh()
			poll.Reset(s.PollInterval())
		case <-thermal:
			syncKernel()
			s.ThermalStep()
			thermalTimer()
		case <-save.C:
			s.SaveHistory()
		case <-sigs:
			s.Stop()
			s.SaveHistory()
			return
		}
	}
}
