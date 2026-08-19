package peerset

import (
	"sync"
	"testing"
	"time"
)

type peer struct{ name string }

func TestAddRefusesDuplicateID(t *testing.T) {
	s := New[*peer]()
	a, b := &peer{"a"}, &peer{"b"}
	if !s.Add("c1", a) {
		t.Fatal("first Add refused")
	}
	if s.Add("c1", b) {
		t.Error("duplicate client ID accepted")
	}
	if got, _ := s.Get("c1"); got != a {
		t.Error("duplicate Add displaced the live peer")
	}
}

// Teardown can run long after the fact -- a timer, a closed data channel --
// and by then the ID may belong to a reconnect. Forgetting by ID alone
// would evict the live peer and strand its answer.
func TestForgetOnlyRemovesTheSamePeer(t *testing.T) {
	s := New[*peer]()
	old, fresh := &peer{"old"}, &peer{"fresh"}
	s.Add("c1", old)
	s.Forget("c1", old)
	s.Add("c1", fresh)

	s.Forget("c1", old) // the old peer's teardown, arriving late
	if got, ok := s.Get("c1"); !ok || got != fresh {
		t.Error("a late teardown evicted the reconnected peer")
	}
}

func TestAllAndLen(t *testing.T) {
	s := New[*peer]()
	s.Add("a", &peer{"a"})
	s.Add("b", &peer{"b"})
	if s.Len() != 2 || len(s.All()) != 2 {
		t.Errorf("Len=%d All=%d, want 2 and 2", s.Len(), len(s.All()))
	}
	if !s.Has("a") || s.Has("nope") {
		t.Error("Has disagrees with the contents")
	}
}

func TestConcurrentUse(t *testing.T) {
	s := New[*peer]()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := &peer{"x"}
			id := string(rune('a' + i%10))
			if s.Add(id, p) {
				s.All()
				s.Forget(id, p)
			}
		}(i)
	}
	wg.Wait()
	if s.Len() != 0 {
		t.Errorf("%d peers left registered", s.Len())
	}
}

// Done and expiry race, and exactly one must win: expire tears the peer
// down, so running it after the handshake succeeded would drop a live
// session.
func TestDeadlineDoneBeatsExpiry(t *testing.T) {
	for i := 0; i < 200; i++ {
		var fired int32
		var mu sync.Mutex
		d := NewDeadline(time.Millisecond, func() {
			mu.Lock()
			fired++
			mu.Unlock()
		})
		d.Done()
		time.Sleep(3 * time.Millisecond)
		mu.Lock()
		got := fired
		mu.Unlock()
		if got != 0 {
			t.Fatalf("iteration %d: expire ran after Done", i)
		}
	}
}

func TestDeadlineExpires(t *testing.T) {
	fired := make(chan struct{})
	NewDeadline(time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline never expired")
	}
}

func TestDeadlineDoneIsIdempotentAndNilSafe(t *testing.T) {
	var nilD *Deadline
	nilD.Done() // must not panic: teardown paths call it unconditionally

	d := NewDeadline(time.Hour, func() { t.Error("expire ran") })
	d.Done()
	d.Done()
}

// A request already in flight must not register after shutdown has drained
// the set: that peer would be held by nobody and torn down by nothing.
func TestCloseRefusesLaterAdds(t *testing.T) {
	s := New[*peer]()
	a := &peer{"a"}
	s.Add("c1", a)

	drained := s.Close()
	if len(drained) != 1 || drained[0] != a {
		t.Fatalf("Close returned %v, want the registered peer", drained)
	}
	if s.Add("c2", &peer{"late"}) {
		t.Error("a peer registered into a closed set")
	}
	if s.Len() != 0 {
		t.Error("closed set is not empty")
	}
}

func TestAddLimitedIsExactUnderConcurrency(t *testing.T) {
	const limit = 5
	s := New[*peer]()
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.AddLimited(string(rune('A'+i)), &peer{"p"}, limit) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if admitted != limit || s.Len() != limit {
		t.Errorf("admitted %d (set holds %d), want exactly %d", admitted, s.Len(), limit)
	}
}
