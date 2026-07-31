//go:build windows

package streamtype

import (
	"os"
	"sync"

	ptylib "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

func defaultShellArgv() []string {
	if shell := os.Getenv("COMSPEC"); shell != "" {
		return []string{shell}
	}
	return []string{"cmd.exe"}
}

// Browser and CLI interactive sessions use the Windows ConPTY API through
// go-pty, providing terminal echo, line editing, VT output, and resize events.
func usePTY(requested bool) bool { return requested }

func terminateShellProcess(process *os.Process) error { return process.Kill() }

func finishPTY(terminal ptylib.Pty, output *sync.WaitGroup) {
	conpty, ok := terminal.(ptylib.ConPty)
	if !ok {
		_ = terminal.Close()
		output.Wait()
		return
	}
	// ClosePseudoConsole can wait for its output to be drained on Windows
	// versions before 11 24H2. Close it concurrently with the output reader,
	// but keep our read pipe open until it reaches EOF. go-pty's Close closes
	// that pipe immediately and can discard the child's final buffered bytes.
	closed := make(chan struct{})
	go func() {
		windows.ClosePseudoConsole(windows.Handle(conpty.Fd()))
		close(closed)
	}()
	output.Wait()
	<-closed
	_ = conpty.InputPipe().Close()
	_ = conpty.OutputPipe().Close()
}

func signalFromName(name string) os.Signal {
	switch name {
	case "INT", "SIGINT", "TERM", "SIGTERM", "KILL", "SIGKILL":
		// os.Process.Signal only implements Kill on Windows. Returning
		// os.Interrupt would silently leave the remote process running.
		return os.Kill
	}
	return nil
}
