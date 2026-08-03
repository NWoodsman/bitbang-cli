//go:build windows

package client

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type synchronizedBuffer struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	changed chan struct{}
}

func newSynchronizedBuffer() *synchronizedBuffer {
	return &synchronizedBuffer{changed: make(chan struct{}, 1)}
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buffer.Write(p)
	b.mu.Unlock()
	select {
	case b.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *synchronizedBuffer) waitFor(t *testing.T, text string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for !strings.Contains(b.String(), text) {
		select {
		case <-b.changed:
		case <-timer.C:
			t.Fatalf("terminal output %q never contained %q", b.String(), text)
		}
	}
}

// This is the browser symptom's regression test: ConPTY must echo a partial
// line before Enter is pressed. A plain stdin/stdout pipe cannot do that.
func TestSession_ShellCommand_WindowsConPTYEchoesInput(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: spins up real pion peer connections and ConPTY")
	}
	sess := shellSession(t)
	stdinReader, stdinWriter := io.Pipe()
	defer stdinWriter.Close()
	out := newSynchronizedBuffer()
	argv := shellHelperArgv(t, "line")

	done := make(chan error, 1)
	go func() {
		_, err := sess.Shell(ShellOptions{
			Argv:   argv,
			Env:    map[string]string{shellHelperEnv: "1"},
			PTY:    true,
			Cols:   80,
			Rows:   24,
			Stdin:  stdinReader,
			Stdout: out,
		})
		done <- err
	}()

	const typed = "typed-before-enter"
	if _, err := io.WriteString(stdinWriter, typed); err != nil {
		t.Fatalf("write partial input: %v", err)
	}
	out.waitFor(t, typed)
	if _, err := io.WriteString(stdinWriter, "\r\n"); err != nil {
		t.Fatalf("write Enter: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shell did not exit after Enter")
	}
	if !strings.Contains(out.String(), "RECEIVED:"+typed) {
		t.Fatalf("terminal output %q missing received command", out.String())
	}
}
