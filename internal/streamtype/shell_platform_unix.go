//go:build unix

package streamtype

import (
	"os"
	"syscall"
	"time"

	ptylib "github.com/aymanbagabas/go-pty"
)

func defaultShellArgv() []string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return []string{shell}
	}
	return []string{"/bin/sh"}
}

const platformSupportsPTY = true

func terminateShellProcess(process *os.Process) error {
	return process.Signal(syscall.SIGHUP)
}

func finishPTY(terminal ptylib.Pty, output *shellOutput, timeout time.Duration) bool {
	if output.wait(timeout) {
		_ = terminal.Close()
		return true
	}
	output.cancel()
	_ = terminal.Close()
	_ = output.wait(shellOutputCloseGrace)
	return false
}

func signalFromName(name string) os.Signal {
	switch name {
	case "INT", "SIGINT":
		return syscall.SIGINT
	case "TERM", "SIGTERM":
		return syscall.SIGTERM
	case "QUIT", "SIGQUIT":
		return syscall.SIGQUIT
	case "HUP", "SIGHUP":
		return syscall.SIGHUP
	case "USR1", "SIGUSR1":
		return syscall.SIGUSR1
	case "USR2", "SIGUSR2":
		return syscall.SIGUSR2
	case "KILL", "SIGKILL":
		return syscall.SIGKILL
	}
	return nil
}
