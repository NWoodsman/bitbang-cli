package streamtype

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ptylib "github.com/aymanbagabas/go-pty"
)

type shellLifecycleStream struct {
	id       uint32
	buffered atomic.Uint64
	writes   atomic.Int32
	fin      chan []byte
}

func (s *shellLifecycleStream) ID() uint32            { return s.id }
func (*shellLifecycleStream) ConnectPath() string     { return "/" }
func (*shellLifecycleStream) WriteSYN([]byte) error   { return nil }
func (s *shellLifecycleStream) WriteDAT([]byte) error { s.writes.Add(1); return nil }
func (s *shellLifecycleStream) WriteFIN(payload []byte) error {
	if s.fin != nil {
		s.fin <- append([]byte(nil), payload...)
	}
	return nil
}
func (*shellLifecycleStream) SendRaw(uint16, []byte) error { return nil }
func (s *shellLifecycleStream) BufferedAmount() uint64     { return s.buffered.Load() }

type synchronizedLifecyclePTY struct {
	resizeStarted chan struct{}
	resizeRelease chan struct{}
	resizeOnce    sync.Once
	resizeCount   atomic.Int32
}

func (*synchronizedLifecyclePTY) Read([]byte) (int, error)              { return 0, nil }
func (*synchronizedLifecyclePTY) Write(p []byte) (int, error)           { return len(p), nil }
func (*synchronizedLifecyclePTY) Close() error                          { return nil }
func (*synchronizedLifecyclePTY) Name() string                          { return "test-pty" }
func (*synchronizedLifecyclePTY) Command(string, ...string) *ptylib.Cmd { return nil }
func (*synchronizedLifecyclePTY) CommandContext(context.Context, string, ...string) *ptylib.Cmd {
	return nil
}
func (p *synchronizedLifecyclePTY) Resize(int, int) error {
	p.resizeCount.Add(1)
	p.resizeOnce.Do(func() { close(p.resizeStarted) })
	<-p.resizeRelease
	return nil
}
func (*synchronizedLifecyclePTY) Fd() uintptr { return 0 }

func TestPumpReaderCancellationBreaksBackpressure(t *testing.T) {
	stream := &shellLifecycleStream{id: 1}
	stream.buffered.Store(maxShellBuffered + 1)
	output := newShellOutput()
	output.Add(1)
	go func() {
		defer output.Done()
		NewShell(nil, false).pumpReader(stream, strings.NewReader("blocked"), shellTagStdout, output.cancelled())
	}()

	if output.wait(20 * time.Millisecond) {
		t.Fatal("output pump completed while the data channel remained backpressured")
	}
	output.cancel()
	if !output.wait(time.Second) {
		t.Fatal("output pump did not stop after cancellation")
	}
	if got := stream.writes.Load(); got != 0 {
		t.Fatalf("WriteDAT calls = %d, want 0 while backpressured", got)
	}
}

func TestFinishOutputBoundsBackpressureWait(t *testing.T) {
	stream := &shellLifecycleStream{id: 2}
	stream.buffered.Store(maxShellBuffered + 1)
	output := newShellOutput()
	output.Add(1)
	go func() {
		defer output.Done()
		NewShell(nil, false).pumpReader(stream, strings.NewReader("blocked"), shellTagStdout, output.cancelled())
	}()

	started := time.Now()
	if finishOutput(output, 20*time.Millisecond) {
		t.Fatal("finishOutput reported a clean drain while backpressure was pinned")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("finishOutput took %v, want a bounded wait", elapsed)
	}
	if !output.wait(time.Second) {
		t.Fatal("timed-out output pump did not exit after cancellation")
	}
}

func TestWaitAndFinishBackpressureStillCompletesSession(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	stream := &shellLifecycleStream{id: 3, fin: make(chan []byte, 1)}
	stream.buffered.Store(maxShellBuffered + 1)
	output := newShellOutput()
	output.Add(1)
	h := NewShell(nil, false)
	h.outputDrainTimeout = 20 * time.Millisecond
	terminal := &synchronizedLifecyclePTY{}
	sess := &shellSession{cmd: cmd, process: cmd.Process, ptyFile: terminal, output: output, done: make(chan struct{})}
	h.streams[stream.id] = sess
	adm, _ := liveShells.admit(0)
	defer liveShells.release(adm)

	go func() {
		defer output.Done()
		h.pumpReader(stream, strings.NewReader("blocked"), shellTagStdout, output.cancelled())
	}()
	go h.waitAndFinish(stream, sess, []string{"helper"}, func() { liveShells.release(adm) })

	select {
	case <-stream.fin:
	case <-time.After(2 * time.Second):
		t.Fatal("waitAndFinish did not send FIN after bounded output drain")
	}
	if got := liveShells.count(); got != 0 {
		t.Fatalf("live shell count = %d, want 0 -- the admission was not released", got)
	}
	h.mu.Lock()
	_, stillMapped := h.streams[stream.id]
	h.mu.Unlock()
	if stillMapped {
		t.Fatal("completed shell remained in the dispatch map")
	}
	if !output.wait(time.Second) {
		t.Fatal("backpressured output pump remained after session completion")
	}
}

func TestDetachSessionSerializesPTYUse(t *testing.T) {
	const streamID = 9
	terminal := &synchronizedLifecyclePTY{
		resizeStarted: make(chan struct{}),
		resizeRelease: make(chan struct{}),
	}
	sess := &shellSession{ptyFile: terminal}
	h := NewShell(nil, false)
	h.streams[streamID] = sess
	stream := &shellLifecycleStream{id: streamID}
	payload := make([]byte, 5)
	payload[0] = shellTagResize
	binary.LittleEndian.PutUint16(payload[1:3], 100)
	binary.LittleEndian.PutUint16(payload[3:5], 40)

	resizeDone := make(chan struct{})
	go func() {
		_ = h.OnDAT(stream, payload)
		close(resizeDone)
	}()
	<-terminal.resizeStarted

	detached := make(chan ptylib.Pty, 1)
	go func() { detached <- h.detachSession(streamID, sess) }()

	select {
	case <-detached:
		t.Fatal("PTY detached while Resize was still using it")
	case <-time.After(20 * time.Millisecond):
	}
	h.mu.Lock()
	_, stillMapped := h.streams[streamID]
	h.mu.Unlock()
	if stillMapped {
		t.Fatal("session remained dispatchable while shutdown waited for PTY use")
	}

	close(terminal.resizeRelease)
	<-resizeDone
	if got := <-detached; got != terminal {
		t.Fatalf("detached PTY = %T, want original terminal", got)
	}
	if err := h.OnDAT(stream, payload); err != nil {
		t.Fatalf("late resize: %v", err)
	}
	if got := terminal.resizeCount.Load(); got != 1 {
		t.Fatalf("resize calls = %d, want 1 after detachment", got)
	}
}

// With the default of one session, a shell left open somewhere used to
// lock the credential holder out of their own listener. The newcomer
// takes it instead, and the displaced one is named so the caller can end
// it.
func TestShellAdmissionsDisplaceTheOldest(t *testing.T) {
	var a shellAdmissions

	first, displaced := a.admit(1)
	if displaced != nil {
		t.Fatal("an empty list reported displacing someone")
	}
	second, displaced := a.admit(1)
	if displaced != first {
		t.Fatalf("displaced = %v, want the shell that was already live", displaced)
	}
	// The displaced shell gives up its slot at once, so a third arrival
	// throws out `second` rather than the shell that just took over.
	if got := a.count(); got != 1 {
		t.Fatalf("count = %d, want only the newcomer holding a slot", got)
	}
	a.release(first) // its stream finishing later must not disturb anything
	if got := a.count(); got != 1 {
		t.Fatalf("count = %d after the displaced stream ended, want 1", got)
	}

	// Oldest-first, not newest-first: the one still working should not be
	// the one thrown out.
	third, displaced := a.admit(1)
	if displaced != second {
		t.Fatal("displaced the wrong shell; eviction must be oldest-first")
	}
	a.release(second)
	a.release(third)
	if got := a.count(); got != 0 {
		t.Fatalf("count = %d after everything released, want 0", got)
	}
}

func TestShellAdmissionsUnlimited(t *testing.T) {
	var a shellAdmissions
	for i := 0; i < 5; i++ {
		if _, displaced := a.admit(0); displaced != nil {
			t.Fatalf("admission %d displaced someone with no limit set", i)
		}
	}
	if got := a.count(); got != 5 {
		t.Fatalf("count = %d, want 5", got)
	}
}

func TestShellAdmissionsReleaseIsIdempotent(t *testing.T) {
	var a shellAdmissions
	adm, _ := a.admit(2)
	other, _ := a.admit(2)
	a.release(adm)
	a.release(adm) // a displaced shell's stream ending after it was evicted
	if got := a.count(); got != 1 {
		t.Fatalf("count = %d, want the other shell still held", got)
	}
	a.release(other)
	if got := a.count(); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}
