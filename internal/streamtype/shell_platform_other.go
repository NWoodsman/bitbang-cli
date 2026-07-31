//go:build !unix && !windows

package streamtype

import (
	"os"
	"sync"

	ptylib "github.com/aymanbagabas/go-pty"
)

func defaultShellArgv() []string                      { return []string{"sh"} }
func usePTY(bool) bool                                { return false }
func terminateShellProcess(process *os.Process) error { return process.Kill() }
func finishPTY(terminal ptylib.Pty, output *sync.WaitGroup) {
	_ = terminal.Close()
	output.Wait()
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
