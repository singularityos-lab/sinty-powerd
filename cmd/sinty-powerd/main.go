// sinty-powerd is the power daemon of SintyOS. It publishes the batteries and
// power supplies as UPower does, switches the power profiles as
// power-profiles-daemon does and keeps the processor out of thermal
// throttling as thermald does, in one process.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/singularityos-lab/sinty-powerd/internal/events"
	"github.com/singularityos-lab/sinty-powerd/internal/service"
	"github.com/singularityos-lab/sinty-powerd/internal/supply"
)

func main() {
	root := flag.String("root", "/", "root of the sysfs tree to read")
	state := flag.String("state", "/var/lib/sinty-powerd", "directory for the profile, charge threshold and battery history")
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("sinty-powerd: ")
	if os.Getenv("DBUS_SYSTEM_BUS_ADDRESS") == "" {
		os.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/run/dbus/system_bus_socket")
	}

	var s *service.Service
	var err error
	for i := 0; ; i++ {
		s, err = service.New(service.Config{Root: *root, StateDir: *state, Policy: supply.DefaultPolicy})
		if err == nil {
			break
		}
		if i%10 == 0 {
			log.Printf("system bus not ready: %v", err)
		}
		time.Sleep(time.Second)
	}
	s.Refresh()
	s.Start()
	go s.WatchHolders()

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
	if !events.Lid(func(closed bool) { s.SetLid(true, closed) }) {
		s.SetLid(false, false)
	}
	log.Print("serving org.freedesktop.UPower and the power profiles")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	poll := time.NewTicker(30 * time.Second)
	thermal := time.NewTicker(2 * time.Second)
	save := time.NewTicker(10 * time.Minute)
	for {
		select {
		case <-refresh:
			time.Sleep(200 * time.Millisecond)
			s.Refresh()
		case <-poll.C:
			s.Refresh()
		case <-thermal.C:
			s.ThermalStep()
		case <-save.C:
			s.SaveHistory()
		case <-sigs:
			s.Stop()
			s.SaveHistory()
			return
		}
	}
}
