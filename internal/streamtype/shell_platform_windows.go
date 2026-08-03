//go:build windows

package streamtype

import (
	"os"
	"time"

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
const platformSupportsPTY = true

func terminateShellProcess(process *os.Process) error { return process.Kill() }

func finishPTY(terminal ptylib.Pty, output *shellOutput, timeout time.Duration) bool {
	conpty, ok := terminal.(ptylib.ConPty)
	if !ok {
		if output.wait(timeout) {
			_ = terminal.Close()
			return true
		}
		output.cancel()
		_ = terminal.Close()
		_ = output.wait(shellOutputCloseGrace)
		return false
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
	drained := output.wait(timeout)
	if !drained {
		output.cancel()
	}
	// Closing our pipe handles force-unblocks both a stalled reader and older
	// ClosePseudoConsole implementations after the bounded drain period.
	_ = conpty.InputPipe().Close()
	_ = conpty.OutputPipe().Close()
	if !drained {
		_ = output.wait(shellOutputCloseGrace)
	}
	select {
	case <-closed:
	case <-time.After(shellOutputCloseGrace):
		drained = false
	}
	return drained
}

func signalFromName(name string) os.Signal {
	switch name {
	case "INT", "SIGINT", "TERM", "SIGTERM", "KILL", "SIGKILL":
		// os.Process.Signal only implements Kill on Windows, so explicit INT
		// and TERM requests terminate the whole shell session rather than
		// returning to its prompt. True Ctrl-C process-group delivery via
		// GenerateConsoleCtrlEvent is intentionally deferred.
		return os.Kill
	}
	return nil
}
