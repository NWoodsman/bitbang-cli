//go:build unix

package streamtype

import (
	"os"
	"sync"
	"syscall"

	ptylib "github.com/aymanbagabas/go-pty"
)

func defaultShellArgv() []string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return []string{shell}
	}
	return []string{"/bin/sh"}
}

func usePTY(requested bool) bool { return requested }

func terminateShellProcess(process *os.Process) error {
	return process.Signal(syscall.SIGHUP)
}

func finishPTY(terminal ptylib.Pty, output *sync.WaitGroup) {
	output.Wait()
	_ = terminal.Close()
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
