package streamtype

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"strconv"
	"sync"

	"github.com/richlegrand/bitbang/internal/bytestream"
	"github.com/richlegrand/bitbang/internal/localdns"
	"github.com/richlegrand/bitbang/internal/protocol"
	"github.com/richlegrand/bitbang/internal/tcpforward"
)

// DefaultTCPMaxConcurrent bounds active TCP connections per WebRTC session.
const DefaultTCPMaxConcurrent = 64

// TCPHandler implements StreamHandler for type="tcp". Each stream dials one
// target from the listener's network and preserves directional EOF in both
// directions.
type TCPHandler struct {
	Verbose bool

	// MaxConcurrent caps active TCP streams for this WebRTC session,
	// including pending dials. 0 disables the limit.
	MaxConcurrent int

	// DialContext is injectable for focused tests. Production uses the same
	// mDNS-aware resolver as the HTTP and WebSocket proxy paths.
	DialContext func(context.Context, string, string) (net.Conn, error)

	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	streams map[uint32]*tcpStream
	active  int
}

type tcpInbound struct {
	data []byte
	fin  bool
}

type tcpStream struct {
	ctx     context.Context
	cancel  context.CancelFunc
	stream  Stream
	inbound chan tcpInbound

	mu   sync.Mutex
	conn net.Conn
}

// NewTCP returns a per-WebRTC-session TCP handler.
func NewTCP(verbose bool) *TCPHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &TCPHandler{
		Verbose:       verbose,
		MaxConcurrent: DefaultTCPMaxConcurrent,
		DialContext:   localdns.Default.DialContext,
		ctx:           ctx,
		cancel:        cancel,
		streams:       make(map[uint32]*tcpStream),
	}
}

func (h *TCPHandler) Type() string             { return "tcp" }
func (h *TCPHandler) OnConnect(_ string) error { return nil }

func (h *TCPHandler) OnSYN(s Stream, payload []byte, final bool) error {
	var open protocol.TCPOpen
	if err := json.Unmarshal(payload, &open); err != nil {
		h.sendError(s, "bad tcp request: "+err.Error())
		return nil
	}
	if err := tcpforward.ValidateTarget(open.Host, open.Port); err != nil {
		h.sendError(s, err.Error())
		return nil
	}

	h.mu.Lock()
	old := h.streams[s.ID()]
	if h.MaxConcurrent > 0 && h.active >= h.MaxConcurrent {
		h.mu.Unlock()
		log.Printf("TCP rejected: at max-streams=%d", h.MaxConcurrent)
		h.sendError(s, "listener is busy (max "+strconv.Itoa(h.MaxConcurrent)+" concurrent TCP connections)")
		return nil
	}
	ctx, cancel := context.WithCancel(h.ctx)
	ts := &tcpStream{
		ctx:     ctx,
		cancel:  cancel,
		stream:  s,
		inbound: make(chan tcpInbound, 256),
	}
	h.streams[s.ID()] = ts
	h.active++
	h.mu.Unlock()
	if old != nil {
		old.close()
	}

	if final {
		ts.inbound <- tcpInbound{fin: true}
	}
	go h.runStream(ts, open)
	return nil
}

func (h *TCPHandler) OnDAT(s Stream, payload []byte) error {
	ts := h.lookup(s.ID())
	if ts == nil {
		return nil
	}
	data := append([]byte(nil), payload...)
	select {
	case ts.inbound <- tcpInbound{data: data}:
		return nil
	case <-ts.ctx.Done():
		return ts.ctx.Err()
	}
}

func (h *TCPHandler) OnFIN(s Stream, payload []byte) error {
	ts := h.lookup(s.ID())
	if ts == nil {
		return nil
	}
	if len(payload) > 0 {
		if err := h.OnDAT(s, payload); err != nil {
			return err
		}
	}
	select {
	case ts.inbound <- tcpInbound{fin: true}:
		return nil
	case <-ts.ctx.Done():
		return ts.ctx.Err()
	}
}

func (h *TCPHandler) runStream(ts *tcpStream, open protocol.TCPOpen) {
	defer h.remove(ts.stream.ID(), ts)

	address := net.JoinHostPort(open.Host, strconv.Itoa(open.Port))
	conn, err := h.DialContext(ts.ctx, "tcp", address)
	if err != nil {
		if ts.ctx.Err() == nil {
			h.sendError(ts.stream, "dial "+address+": "+err.Error())
		}
		ts.cancel()
		return
	}
	if !ts.setConn(conn) {
		_ = conn.Close()
		return
	}

	ack, _ := json.Marshal(map[string]string{"status": "ok"})
	if err := ts.stream.WriteSYN(ack); err != nil {
		ts.cancel()
		_ = conn.Close()
		return
	}
	if h.Verbose {
		log.Printf("TCP stream %d connected to %s", ts.stream.ID(), address)
	}

	done := make(chan error, 2)
	go func() { done <- h.writeTarget(ts, conn) }()
	go func() {
		_, err := bytestream.Pump(ts.ctx, conn, ts.stream)
		done <- err
	}()

	for i := 0; i < 2; i++ {
		if pumpErr := <-done; pumpErr != nil && ts.ctx.Err() == nil {
			ts.cancel()
			_ = conn.Close()
		}
	}
	ts.cancel()
	_ = conn.Close()
	if h.Verbose {
		log.Printf("TCP stream %d closed (%s)", ts.stream.ID(), address)
	}
}

func (h *TCPHandler) writeTarget(ts *tcpStream, conn net.Conn) error {
	for {
		select {
		case <-ts.ctx.Done():
			return ts.ctx.Err()
		case in := <-ts.inbound:
			if len(in.data) > 0 {
				if err := bytestream.WriteFull(conn, in.data); err != nil {
					return err
				}
			}
			if in.fin {
				return bytestream.CloseWrite(conn)
			}
		}
	}
}

func (h *TCPHandler) lookup(id uint32) *tcpStream {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streams[id]
}

func (h *TCPHandler) remove(id uint32, want *tcpStream) {
	h.mu.Lock()
	if h.streams[id] == want {
		delete(h.streams, id)
	}
	h.active--
	h.mu.Unlock()
}

func (ts *tcpStream) setConn(conn net.Conn) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.ctx.Err() != nil {
		return false
	}
	ts.conn = conn
	return true
}

func (ts *tcpStream) close() {
	ts.cancel()
	ts.mu.Lock()
	conn := ts.conn
	ts.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// CloseAll cancels pending dials and closes every target socket owned by this
// WebRTC session.
func (h *TCPHandler) CloseAll() {
	h.cancel()
	h.mu.Lock()
	streams := make([]*tcpStream, 0, len(h.streams))
	for _, ts := range h.streams {
		streams = append(streams, ts)
	}
	h.mu.Unlock()
	for _, ts := range streams {
		ts.close()
	}
}

func (h *TCPHandler) sendError(s Stream, message string) {
	payload, _ := json.Marshal(map[string]string{"status": "error", "error": message})
	_ = s.SendRaw(protocol.FlagSYN|protocol.FlagFIN, payload)
}
