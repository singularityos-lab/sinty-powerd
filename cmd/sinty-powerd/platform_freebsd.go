package main

import (
	"log"

	"github.com/singularityos-lab/sinty-powerd/internal/mirror"
)

const mirrorRoot = "/var/run/sinty-powerd/sys-mirror"

func platform(root string) (string, func()) {
	if root != "/" {
		return root, func() {}
	}
	m := mirror.New(mirrorRoot, mirror.Kernel{})
	sync := func() {
		if err := m.Sync(); err != nil {
			log.Print(err)
		}
	}
	sync()
	if err := m.Watch(func(path string, err error) { log.Printf("%s not applied: %v", path, err) }); err != nil {
		log.Printf("settings will not reach the kernel: %v", err)
	}
	return mirrorRoot, sync
}
