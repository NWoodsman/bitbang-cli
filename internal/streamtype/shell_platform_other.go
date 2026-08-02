//go:build !unix && !windows

package streamtype

import (
	"os"
	"time"

	ptylib "github.com/aymanbagabas/go-pty"
)

const platformSupportsPTY = false

func defaultShellArgv() []string                      { return []string{"sh"} }
func terminateShellProcess(process *os.Process) error { return process.Kill() }
func finishPTY(terminal ptylib.Pty, output *shellOutput, timeout time.Duration) bool {
	_ = terminal.Close()
	if output.wait(timeout) {
		return true
	}
	output.cancel()
	_ = output.wait(shellOutputCloseGrace)
	return false
}
func signalFromName(name string) os.Signal {
	if name == "KILL" || name == "SIGKILL" {
		return os.Kill
	}
	if name == "INT" || name == "SIGINT" {
		return os.Interrupt
	}
	return nil
}
