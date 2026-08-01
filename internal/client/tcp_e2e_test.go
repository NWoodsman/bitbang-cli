package client

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/richlegrand/bitbang/internal/bytestream"
	"github.com/richlegrand/bitbang/internal/streamtype"
	"github.com/richlegrand/bitbang/internal/tcpforward"
)

func startTCPEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
			}()
		}
	}()
	return ln
}

func unusedTCPPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	for i := 0; i < count; i++ {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			for _, listener := range listeners {
				_ = listener.Close()
			}
			t.Fatalf("temporary listen: %v", err)
		}
		listeners = append(listeners, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return ports
}

func roundTripForward(t *testing.T, port int, payload []byte) {
	t.Helper()
	if err := forwardRoundTrip(port, payload); err != nil {
		t.Fatal(err)
	}
}

func forwardRoundTrip(port int, payload []byte) error {
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return fmt.Errorf("dial local forward: %w", err)
	}
	tcp := conn.(*net.TCPConn)
	defer tcp.Close()
	if _, err := tcp.Write(payload); err != nil {
		return fmt.Errorf("write local forward: %w", err)
	}
	if err := tcp.CloseWrite(); err != nil {
		return fmt.Errorf("half-close local forward: %w", err)
	}
	got, err := io.ReadAll(tcp)
	if err != nil {
		return fmt.Errorf("read local forward: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("echoed %d bytes matching=%v, want %d", len(got), bytes.Equal(got, payload), len(payload))
	}
	return nil
}

func TestSession_TCPForwardingEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: spins up real pion peer connections and TCP listeners")
	}
	echo := startTCPEcho(t)
	echoPort := echo.Addr().(*net.TCPAddr).Port

	id := ephemeralID(t)
	relay := newFakeSignaling()
	t.Cleanup(relay.Close)
	tcpHandler := streamtype.NewTCP(false)
	startListener(relay.host(), id, streamtype.NewShell([]string{"sh"}, false), tcpHandler)
	waitRegistered(t, relay)
	sess := mustDial(t, relay.host(), id, "shell", "tcp")
	t.Cleanup(sess.Close)
	if !containsString(sess.ServerCaps, "tcp") {
		t.Fatalf("server caps = %v, want tcp", sess.ServerCaps)
	}

	ports := unusedTCPPorts(t, 3)
	goodLocal, badLocal, badRemote := ports[0], ports[1], ports[2]
	forwarder, err := sess.StartLocalForwarding([]tcpforward.Forward{
		{LocalPort: goodLocal, Host: "127.0.0.1", Port: echoPort},
		{LocalPort: badLocal, Host: "127.0.0.1", Port: badRemote},
	}, false)
	if err != nil {
		t.Fatalf("StartLocalForwarding: %v", err)
	}
	t.Cleanup(forwarder.Close)

	// More than the old 64-frame client queue could retain. Exact binary
	// comparison catches drops, duplication, and frame-boundary corruption.
	large := make([]byte, bytestream.FrameSize*70+137)
	for i := range large {
		large[i] = byte(i * 29)
	}
	roundTripForward(t, goodLocal, large)

	// Independent stream IDs must allow simultaneous local TCP connections.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte(i), 0, 0xff}, 4000+i)
			errs <- forwardRoundTrip(goodLocal, payload)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	// One remote dial failure closes only that accepted connection.
	bad, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", badLocal), 2*time.Second)
	if err != nil {
		t.Fatalf("dial failing local mapping: %v", err)
	}
	_ = bad.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = bad.Write([]byte("trigger"))
	buf := make([]byte, 1)
	if _, err := bad.Read(buf); err == nil {
		t.Fatal("failed target connection stayed open")
	}
	_ = bad.Close()
	roundTripForward(t, goodLocal, []byte("healthy after isolated failure"))

	// Session completion tears down both idle accepted sockets and listeners.
	idle, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", goodLocal), 2*time.Second)
	if err != nil {
		t.Fatalf("dial idle forwarded connection: %v", err)
	}
	sess.Close()
	forwarder.Close()
	_ = idle.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := idle.Read(buf); err == nil {
		t.Fatal("idle forwarded connection survived session teardown")
	}
	_ = idle.Close()
	if conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", goodLocal), 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("local listener survived session teardown")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
