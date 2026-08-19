//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyReload delivers SIGHUP, the reload trigger that works when
// nobody is at a terminal -- under systemd, nohup, or a container.
func notifyReload(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGHUP)
}
