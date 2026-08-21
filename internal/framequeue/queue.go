// Package framequeue sequences inbound data-channel frames for a peer
// whose session does not exist yet.
//
// A listener that chooses its handler set from the presented access code
// cannot build the session until the answer arrives, but frames start
// arriving the moment the channel opens: the browser sends its stream-0
// connect immediately, and losing that frame hangs the handshake it
// belongs to. The queue holds them, and Publish delivers the backlog in
// order once the session exists.
//
// Delivery never runs on the caller's goroutine. The signaling read loop
// is shared by every peer, and a congested write in one session would
// otherwise stop signaling for all of them.
package framequeue

import "sync"

// DefaultMaxPendingBytes bounds the backlog one peer may accumulate
// before its session exists. Past it the connection is closed rather
// than buffered: a peer that floods the channel and never completes its
// handshake would otherwise cost memory for as long as it liked.
const DefaultMaxPendingBytes = 256 << 10

// Closer is the connection the queue tears down on Close. Narrow on
// purpose, so this package depends on neither peer nor session.
type Closer interface{ Close() }

// Queue holds a peer's frames until a session can take them, then hands
// over. Its mutex also guards whatever late-bound state the owner
// installs through the Publish and Close callbacks, which is what keeps
// publication and teardown ordered: either teardown sees the installed
// state and releases it, or publication finds the queue closed and
// declines.
type Queue struct {
	mu     sync.Mutex
	closed bool
	conn   Closer
	// published marks that Publish has run. Frames queue until it does,
	// even when a delivery target is already set: the target is only
	// where frames go, while published is when they may start going.
	published    bool
	deliver      func([]byte)
	dispatching  bool
	pending      [][]byte
	pendingBytes int
	maxBytes     int
}

// New returns a queue holding frames for a peer. A zero maxBytes takes
// DefaultMaxPendingBytes. conn may be nil and set later with SetConn.
func New(conn Closer, maxBytes int) *Queue {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxPendingBytes
	}
	return &Queue{conn: conn, maxBytes: maxBytes}
}

// SetConn attaches the connection. It is written after the peer
// connection exists but read from callbacks, so it takes the lock every
// other late-bound field does.
func (q *Queue) SetConn(conn Closer) {
	q.mu.Lock()
	q.conn = conn
	q.mu.Unlock()
}

// SetDeliver overrides where published frames go. Production leaves this
// to Publish; tests use it to substitute a delivery that blocks or
// counts.
func (q *Queue) SetDeliver(deliver func([]byte)) {
	q.mu.Lock()
	q.deliver = deliver
	q.mu.Unlock()
}

// Enqueue takes one inbound frame: delivered straight through once a
// session is live, queued while there is none or while the backlog is
// still draining. A peer that fills the queue past its cap is closed.
func (q *Queue) Enqueue(data []byte) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if !q.published || q.dispatching {
		if q.pendingBytes+len(data) > q.maxBytes {
			conn := q.conn
			q.mu.Unlock()
			if conn != nil {
				conn.Close()
			}
			return
		}
		q.pending = append(q.pending, append([]byte(nil), data...))
		q.pendingBytes += len(data)
		q.mu.Unlock()
		return
	}
	deliver := q.deliver
	q.mu.Unlock()
	deliver(data)
}

// Publish installs the delivery target and drains the backlog on its own
// goroutine, returning false if the queue is already closed.
//
// install, when non-nil, runs under the queue's lock immediately before
// the target is installed. Owners use it to set their own per-peer state
// in the same critical section, so a concurrent Close cannot see the
// state installed but the queue open, or the reverse.
//
// A nil deliver leaves any target set by SetDeliver in place.
func (q *Queue) Publish(deliver func([]byte), install func()) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if install != nil {
		install()
	}
	if deliver != nil && q.deliver == nil {
		q.deliver = deliver
	}
	q.published = true
	q.dispatching = true
	batch := q.takePendingLocked()
	q.mu.Unlock()

	go q.drain(batch)
	return true
}

// drain delivers the backlog, then whatever arrived while it was
// working, until the queue comes up empty or the peer goes away.
// Clearing dispatching under the lock is what hands routing back to
// Enqueue's direct path with no frame overtaking another.
func (q *Queue) drain(batch [][]byte) {
	defer func() {
		q.mu.Lock()
		q.dispatching = false
		q.mu.Unlock()
	}()

	for {
		for _, data := range batch {
			// Stop between frames so teardown cannot be followed by
			// delivery of the rest of the backlog.
			q.mu.Lock()
			deliver, closed := q.deliver, q.closed
			q.mu.Unlock()
			if closed {
				return
			}
			if deliver != nil {
				deliver(data)
			}
		}

		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return
		}
		batch = q.takePendingLocked()
		if len(batch) == 0 {
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
	}
}

func (q *Queue) takePendingLocked() [][]byte {
	batch := q.pending
	q.pending = nil
	q.pendingBytes = 0
	return batch
}

// Close marks the queue closed, drops the backlog, and closes the
// connection. It reports whether this call did the work, so owners log
// and forget the peer exactly once.
//
// release, when non-nil, runs under the lock and may return a function
// to run after the lock is dropped -- the place for work that must not
// hold it, such as closing a handler or handing back a reservation.
func (q *Queue) Close(release func() func()) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.closed = true
	var after func()
	if release != nil {
		after = release()
	}
	conn := q.conn
	q.pending = nil
	q.pendingBytes = 0
	q.mu.Unlock()

	if after != nil {
		after()
	}
	if conn != nil {
		conn.Close()
	}
	return true
}

// Draining reports whether the backlog goroutine is still running.
// Observable so a test can wait for delivery to finish rather than
// sleeping.
func (q *Queue) Draining() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dispatching
}

// IsClosed reports whether Close has run. Stream admission checks it so
// a frame already in flight when the peer died cannot open anything on
// the way out.
func (q *Queue) IsClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

// Locked runs f while holding the queue's lock, passing whether the
// queue is closed. For owners that need to read or amend state they
// installed through Publish without racing Close.
func (q *Queue) Locked(f func(closed bool)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	f(q.closed)
}
