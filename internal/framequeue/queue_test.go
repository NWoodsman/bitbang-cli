package framequeue

import (
	"sync"
	"testing"
	"time"
)

type fakeConn struct {
	mu     sync.Mutex
	closes int
}

func (c *fakeConn) Close() {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
}

func (c *fakeConn) closed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

// collector records deliveries in order.
type collector struct {
	mu   sync.Mutex
	got  []string
	fire chan struct{}
}

func newCollector() *collector { return &collector{fire: make(chan struct{}, 64)} }

func (c *collector) deliver(data []byte) {
	c.mu.Lock()
	c.got = append(c.got, string(data))
	c.mu.Unlock()
	c.fire <- struct{}{}
}

func (c *collector) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestBacklogDrainsInOrder(t *testing.T) {
	q := New(&fakeConn{}, 0)
	c := newCollector()

	// The browser's stream-0 connect arrives before the session exists.
	// Losing it would hang the handshake it belongs to.
	q.Enqueue([]byte("connect"))
	q.Enqueue([]byte("second"))

	if got := c.seen(); len(got) != 0 {
		t.Fatalf("delivered %v before Publish", got)
	}
	if !q.Publish(c.deliver, nil) {
		t.Fatal("Publish refused on a live queue")
	}

	waitFor(t, "the backlog to drain", func() bool { return len(c.seen()) == 2 })
	got := c.seen()
	if got[0] != "connect" || got[1] != "second" {
		t.Errorf("drained out of order: %v", got)
	}
}

func TestEnqueueAfterPublishDeliversDirectly(t *testing.T) {
	q := New(&fakeConn{}, 0)
	c := newCollector()
	q.Publish(c.deliver, nil)
	waitFor(t, "the empty drain to finish", func() bool { return !q.Draining() })

	q.Enqueue([]byte("live"))
	if got := c.seen(); len(got) != 1 || got[0] != "live" {
		t.Errorf("got %v, want direct delivery once published", got)
	}
}

func TestOverflowClosesTheConnection(t *testing.T) {
	conn := &fakeConn{}
	q := New(conn, 16)
	q.Enqueue(make([]byte, 12))
	if conn.closed() != 0 {
		t.Fatal("closed under the cap")
	}
	q.Enqueue(make([]byte, 12))
	if conn.closed() != 1 {
		t.Error("a peer that floods the queue before its session exists must be dropped")
	}
}

func TestPublishRefusedAfterClose(t *testing.T) {
	q := New(&fakeConn{}, 0)
	if !q.Close(nil) {
		t.Fatal("first Close did not report doing the work")
	}
	if q.Close(nil) {
		t.Error("second Close also claimed the work; owners would log twice")
	}
	if q.Publish(func([]byte) {}, nil) {
		t.Error("Publish succeeded after Close")
	}
}

func TestCloseRunsReleaseOutsideTheLock(t *testing.T) {
	q := New(&fakeConn{}, 0)
	var order []string
	q.Close(func() func() {
		order = append(order, "under-lock")
		return func() {
			// Taking the lock here would deadlock if release ran
			// while it was still held.
			q.Locked(func(bool) { order = append(order, "after-lock") })
		}
	})
	if len(order) != 2 || order[0] != "under-lock" || order[1] != "after-lock" {
		t.Errorf("got %v, want the deferred half to run after the lock is dropped", order)
	}
}

// Publish and Close must be mutually exclusive: either the owner's state
// is installed and teardown releases it, or teardown wins and Publish
// declines. Never both, never neither.
func TestPublishRacesClose(t *testing.T) {
	for i := 0; i < 200; i++ {
		q := New(&fakeConn{}, 0)
		var installed, released bool
		var wg sync.WaitGroup
		var published bool

		wg.Add(2)
		go func() {
			defer wg.Done()
			published = q.Publish(func([]byte) {}, func() { installed = true })
		}()
		go func() {
			defer wg.Done()
			q.Close(func() func() {
				return func() { released = installed }
			})
		}()
		wg.Wait()

		if published != installed {
			t.Fatalf("iteration %d: published=%v installed=%v", i, published, installed)
		}
		if installed && !released {
			t.Fatalf("iteration %d: state installed but teardown did not see it", i)
		}
	}
}

// Delivery must not run on the caller's goroutine: the signaling read
// loop is shared by every peer, and a congested write would stop
// signaling for all of them.
func TestPublishDoesNotDeliverOnCallerGoroutine(t *testing.T) {
	q := New(&fakeConn{}, 0)
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer close(release)

	q.SetDeliver(func([]byte) {
		once.Do(func() { close(blocked) })
		<-release
	})
	q.Enqueue([]byte("queued"))

	returned := make(chan bool, 1)
	go func() { returned <- q.Publish(nil, nil) }()

	select {
	case ok := <-returned:
		if !ok {
			t.Fatal("Publish refused on a live queue")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on delivery -- the signaling loop would stall with it")
	}
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued frame was never delivered")
	}
}

// A peer that goes away mid-drain must not keep feeding a session being
// torn down: exactly the delivery already in flight may complete.
func TestDrainStopsOnClose(t *testing.T) {
	const queued = 8
	q := New(&fakeConn{}, 0)
	var mu sync.Mutex
	delivered := 0
	started := make(chan struct{}, queued)
	proceed := make(chan struct{})

	q.SetDeliver(func([]byte) {
		mu.Lock()
		delivered++
		mu.Unlock()
		started <- struct{}{}
		<-proceed
	})
	for i := 0; i < queued; i++ {
		q.Enqueue([]byte("frame"))
	}
	if !q.Publish(nil, nil) {
		t.Fatal("Publish refused on a live queue")
	}

	<-started // one delivery is in flight and parked
	q.Close(nil)
	close(proceed)

	waitFor(t, "the drain goroutine to exit", func() bool { return !q.Draining() })
	mu.Lock()
	got := delivered
	mu.Unlock()
	if got != 1 {
		t.Errorf("delivered %d of %d queued frames after Close, want only the one in flight", got, queued)
	}
}

func TestSetConnClosesTheLateConnection(t *testing.T) {
	q := New(nil, 0)
	conn := &fakeConn{}
	q.SetConn(conn)
	q.Close(nil)
	if conn.closed() != 1 {
		t.Error("a connection attached after construction was not closed on teardown")
	}
}
