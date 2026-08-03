package client

import (
	"testing"
	"time"

	"github.com/richlegrand/bitbang/internal/protocol"
)

func TestSessionDoneClosesWhenDispatcherInboxIsFull(t *testing.T) {
	p := &Peer{
		dcMsg:    make(chan []byte, 257),
		dcClosed: make(chan struct{}),
	}
	sess := newSession(p)
	t.Cleanup(sess.Close)
	stream := sess.OpenStream()
	sess.startDispatcher(p)

	for i := 0; i < cap(stream.Inbox())+1; i++ {
		p.dcMsg <- protocol.BuildFrame(stream.ID(), protocol.FlagDAT, []byte{byte(i)})
	}
	deadline := time.Now().Add(time.Second)
	for len(stream.Inbox()) != cap(stream.Inbox()) || len(p.dcMsg) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("inbox length = %d, queued messages = %d; want full inbox and blocked delivery", len(stream.Inbox()), len(p.dcMsg))
		}
		time.Sleep(time.Millisecond)
	}

	close(p.dcClosed)
	select {
	case <-sess.Done():
	case <-time.After(time.Second):
		t.Fatal("session remained open after data channel closure")
	}
}
