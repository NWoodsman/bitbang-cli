// Package peerset holds the bookkeeping every listener does for peers in
// flight: a registry keyed by client ID, and the deadline that bounds how
// long a peer may sit half-connected.
//
// Both `serve` and `share` accept peers from the same signaling loop and
// need the same three things from a registry -- refuse a duplicate client
// ID, look one up when its answer or candidate arrives, and forget it on
// teardown without evicting a newer peer that reused the ID. The last one
// is the subtle one, and it was written twice before this package existed.
package peerset

import (
	"sync"
	"sync/atomic"
	"time"
)

// Set is a registry of live peers keyed by client ID. T is the owner's own
// peer type, so a lookup hands back everything the owner needs rather than
// a base type it has to map back from.
//
// Safe for concurrent use. The signaling read loop registers and looks up;
// teardown, which can run on a pion callback or a timer, forgets.
type Set[T comparable] struct {
	mu     sync.Mutex
	peers  map[string]T
	closed bool
}

func New[T comparable]() *Set[T] { return &Set[T]{peers: make(map[string]T)} }

// Add registers a peer, reporting false if the client ID is already taken
// or the set has been closed. A repeated ID is a peer trying to open a
// second connection under the same name, which no legitimate connector
// does.
func (s *Set[T]) Add(clientID string, p T) bool {
	return s.AddLimited(clientID, p, 0)
}

// AddLimited is Add with a ceiling on how many peers may be registered at
// once; a max of zero or less means no ceiling. The count and the insert
// happen under one lock, so the limit is exact rather than approximate --
// checking Len first and adding after would let concurrent requests slip
// past it together.
func (s *Set[T]) AddLimited(clientID string, p T, max int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if max > 0 && len(s.peers) >= max {
		return false
	}
	if _, taken := s.peers[clientID]; taken {
		return false
	}
	s.peers[clientID] = p
	return true
}

// Get returns the peer for a client ID.
func (s *Set[T]) Get(clientID string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[clientID]
	return p, ok
}

// Has reports whether a client ID is registered, without materializing the
// peer. For the duplicate check before a connection is built.
func (s *Set[T]) Has(clientID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.peers[clientID]
	return ok
}

// Forget removes a peer, but only if the registered peer is still the one
// the caller holds. Teardown can run long after the fact -- a timer, a
// closed data channel -- and by then the ID may belong to a reconnect.
// Deleting by ID alone would evict the live peer and leave the listener
// unable to route its answer.
func (s *Set[T]) Forget(clientID string, p T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.peers[clientID]; ok && cur == p {
		delete(s.peers, clientID)
	}
}

// All returns the live peers. A snapshot: the caller must not assume a peer
// is still live by the time it acts on it, only that it was a moment ago.
func (s *Set[T]) All() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]T, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p)
	}
	return out
}

// Close empties the set and refuses every later Add, returning what was in
// it so the caller can tear those peers down.
//
// The refusal is the point. Without it a request already in flight can
// register after a shutdown has drained the set, and that peer is then
// held by nobody and torn down by nothing.
func (s *Set[T]) Close() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	out := make([]T, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p)
	}
	s.peers = make(map[string]T)
	return out
}

// Len is the number of live peers, for admission limits.
func (s *Set[T]) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
}

// Deadline bounds the time a peer may take to finish its handshake, and
// covers the case neither the data channel nor the signaling loop can: a
// peer that requests a connection and then goes quiet never leaves
// WebRTC's `new` state, so there is no channel to close and no terminal
// state to observe.
//
// Done and expiry race, and exactly one wins. Stopping the timer is not
// enough on its own -- time.Timer.Stop does not stop a callback that has
// already begun -- so the winner is decided by a compare-and-swap.
type Deadline struct {
	done  atomic.Bool
	timer *time.Timer
}

// NewDeadline runs expire after the given delay, unless Done gets there
// first.
func NewDeadline(after time.Duration, expire func()) *Deadline {
	d := &Deadline{}
	d.timer = time.AfterFunc(after, func() {
		if d.done.CompareAndSwap(false, true) {
			expire()
		}
	})
	return d
}

// Done cancels the deadline. Safe on a nil Deadline and safe to call more
// than once, so every teardown path can call it without coordinating.
func (d *Deadline) Done() {
	if d != nil && d.done.CompareAndSwap(false, true) {
		d.timer.Stop()
	}
}
