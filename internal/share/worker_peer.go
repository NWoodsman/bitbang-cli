package share

import (
	"sync"
	"time"

	"github.com/richlegrand/bitbang/internal/framequeue"
	"github.com/richlegrand/bitbang/internal/peer"
	"github.com/richlegrand/bitbang/internal/peerset"
	"github.com/richlegrand/bitbang/internal/session"
	"github.com/richlegrand/bitbang/internal/streamtype"
)

// sharePeer owns one connection's role reservation, session, and tmux client.
// teardown releases them exactly once regardless of which close path wins.
//
// Frame sequencing -- holding the browser's stream-0 connect until the
// session exists, then draining in order off the signaling loop -- lives
// in framequeue. Its lock also guards the fields below, which is what
// keeps publication and teardown ordered.
type sharePeer struct {
	q *framequeue.Queue

	conn          *peer.Connection
	shell         *streamtype.ShellHandler
	session       *session.Session
	releases      []func()
	establishment *peerset.Deadline
	refusal       *time.Timer
}

func newSharePeer() *sharePeer {
	return &sharePeer{q: framequeue.New(nil, maxPendingPeerBytes)}
}

// handleMessage routes an inbound data-channel frame to the session,
// queueing it while there is none.
func (p *sharePeer) handleMessage(data []byte) { p.q.Enqueue(data) }

// hold transfers a reservation to teardown. A false return leaves ownership
// with the caller because teardown has already run.
func (p *sharePeer) hold(release func()) bool {
	held := false
	p.q.Locked(func(closed bool) {
		if closed {
			return
		}
		p.releases = append(p.releases, release)
		held = true
	})
	return held
}

// publish installs the session atomically with respect to teardown and
// drains early frames on a per-peer goroutine. It returns false if
// teardown won. The shell handler is set inside the queue's lock, in the
// same critical section as the session, so teardown either sees it and
// closes it or finds the peer closed and declines.
func (p *sharePeer) publish(sh *streamtype.ShellHandler, sess *session.Session) bool {
	var deliver func([]byte)
	if sess != nil {
		deliver = sess.HandleMessage
	}
	return p.q.Publish(deliver, func() {
		p.shell = sh
		p.session = sess
	})
}

// isClosed reports whether teardown has run. Stream admission checks it
// so a frame already in flight when the peer died cannot open a terminal
// on the way out.
func (p *sharePeer) isClosed() bool { return p.q.IsClosed() }

// armEstablishment bounds the time from request to a completed stream-0
// handshake. This also covers peers that authorize but never finish ICE.
func (p *sharePeer) armEstablishment(after time.Duration, expire func()) {
	p.q.Locked(func(closed bool) {
		if !closed {
			p.establishment = peerset.NewDeadline(after, expire)
		}
	})
}

// markEstablished cancels the establishment deadline. Wired to the
// session's readiness callback, which fires once the stream-0 handshake
// completes over an open data channel.
func (p *sharePeer) markEstablished() {
	p.q.Locked(func(bool) {
		p.establishment.Done()
	})
}

// armRefusal starts the grace period for a peer turned away because its
// role was full. It stays connected long enough to load the page and
// read why, then goes, so a full share cannot be pinned open by peers
// that will never be admitted.
func (p *sharePeer) armRefusal(after time.Duration, expire func()) {
	p.q.Locked(func(closed bool) {
		if !closed {
			p.refusal = time.AfterFunc(after, expire)
		}
	})
}

// teardown hands back every reservation, kills the peer's tmux client,
// and closes the connection. It reports whether this call did the work,
// so callers log and drop map entries exactly once.
//
// The handler is read under the same lock publish writes it under, which
// keeps the two ordered: either teardown sees a live peer and closes its
// handler, or publish finds the peer closed and declines.
func (p *sharePeer) teardown() bool {
	return p.q.Close(func() func() {
		p.establishment.Done()
		if p.refusal != nil {
			p.refusal.Stop()
		}
		sh := p.shell
		releases := p.releases
		p.releases = nil
		return func() {
			if sh != nil {
				sh.Close()
			}
			for _, release := range releases {
				release()
			}
		}
	})
}

// setConn attaches the peer connection. It is written after
// peer.HandleRequest returns but read from pion's callbacks, so it
// takes the same lock as every other late-bound field.
func (p *sharePeer) setConn(conn *peer.Connection) {
	p.q.Locked(func(bool) { p.conn = conn })
	p.q.SetConn(conn)
}

// roleSlots is the admission counter behind the single-controller rule
// and --max-viewers. A slot is taken when a peer is authorized and held
// for as long as it stays connected. Taking one only when a terminal
// opens would leave both limits unenforced, since a peer can finish the
// handshake and never open one.
type roleSlots struct {
	mu   sync.Mutex
	used int
	max  int
	busy string
}

// newRoleSlots returns a pool of max slots. busy is what a refused peer
// is told. A read-only share sizes its control pool at zero, though
// that is a backstop: authorize never grants control on such a share,
// so nothing reaches the pool to be refused.
func newRoleSlots(max int, busy string) *roleSlots {
	return &roleSlots{max: max, busy: busy}
}

// acquire takes a slot and returns the func that gives it back, or nil
// and the busy message when the pool is full.
//
// The returned release function is idempotent so competing cleanup paths
// cannot decrement the pool twice.
func (r *roleSlots) acquire() (func(), string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.used >= r.max {
		return nil, r.busy
	}
	r.used++
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.used--
			r.mu.Unlock()
		})
	}, ""
}
