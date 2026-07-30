package localdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// fakeConn is a sentinel; no method on it is ever called.
type fakeConn struct{ net.Conn }

type recorder struct {
	dialed    []string
	lookups   []string
	dialErr   map[string]error // addr -> error to return
	lookupIP  netip.Addr
	lookupTTL time.Duration
	lookupErr error
}

func newResolverWith(rec *recorder) *Resolver {
	return &Resolver{
		dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			rec.dialed = append(rec.dialed, addr)
			if err, ok := rec.dialErr[addr]; ok && err != nil {
				return nil, err
			}
			return fakeConn{}, nil
		},
		lookup: func(_ context.Context, host string) (netip.Addr, time.Duration, error) {
			rec.lookups = append(rec.lookups, host)
			if rec.lookupErr != nil {
				return netip.Addr{}, 0, rec.lookupErr
			}
			return rec.lookupIP, rec.lookupTTL, nil
		},
		cache: make(map[string]cacheEntry),
	}
}

func dnsFailure(host string) error {
	return &net.DNSError{Err: "server misbehaving", Name: host, IsNotFound: false}
}

func TestIsLocalName(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"nas.local", true},
		{"NAS.LOCAL", true},
		{"nas.local.", true}, // fully-qualified form
		{"printer.lan.local", true},
		{"nas.localdomain", false}, // must not match on prefix
		{"local", false},           // bare label is not in the namespace
		{"example.com", false},
		{"192.168.1.144", false},
		{"", false},
	} {
		if got := IsLocalName(tc.host); got != tc.want {
			t.Errorf("IsLocalName(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// A non-.local host must never reach the multicast path.
func TestDialContext_NonLocalPassesThrough(t *testing.T) {
	rec := &recorder{}
	r := newResolverWith(rec)

	if _, err := r.DialContext(context.Background(), "tcp", "example.com:80"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.lookups) != 0 {
		t.Errorf("mDNS consulted for a non-.local host: %v", rec.lookups)
	}
	if len(rec.dialed) != 1 || rec.dialed[0] != "example.com:80" {
		t.Errorf("dialed = %v, want one dial of example.com:80", rec.dialed)
	}
}

// System-first: if the OS can already resolve the .local name (CGO build,
// /etc/hosts entry, resolved with MulticastDNS on), we must not second-guess it.
func TestDialContext_SystemResolverWins(t *testing.T) {
	rec := &recorder{}
	r := newResolverWith(rec)

	if _, err := r.DialContext(context.Background(), "tcp", "nas.local:5000"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.lookups) != 0 {
		t.Errorf("mDNS used despite the system resolver succeeding: %v", rec.lookups)
	}
}

// The bug this package exists for: system resolution fails with a DNS error,
// so we fall back to multicast and dial the address it returns.
func TestDialContext_FallsBackToMDNS(t *testing.T) {
	rec := &recorder{
		dialErr:   map[string]error{"nas.local:5000": dnsFailure("nas.local")},
		lookupIP:  netip.MustParseAddr("192.168.1.144"),
		lookupTTL: 2 * time.Minute,
	}
	r := newResolverWith(rec)

	if _, err := r.DialContext(context.Background(), "tcp", "nas.local:5000"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.lookups) != 1 || rec.lookups[0] != "nas.local" {
		t.Fatalf("lookups = %v, want one lookup of nas.local", rec.lookups)
	}
	last := rec.dialed[len(rec.dialed)-1]
	if last != "192.168.1.144:5000" {
		t.Errorf("final dial = %q, want the mDNS-resolved address", last)
	}
}

// A live host refusing the connection is not a naming problem. Retrying it as
// an mDNS lookup would add latency and mask the real error.
func TestDialContext_NonDNSErrorDoesNotTriggerMDNS(t *testing.T) {
	refused := errors.New("connect: connection refused")
	rec := &recorder{dialErr: map[string]error{"nas.local:5000": refused}}
	r := newResolverWith(rec)

	_, err := r.DialContext(context.Background(), "tcp", "nas.local:5000")
	if !errors.Is(err, refused) {
		t.Errorf("err = %v, want the original dial error surfaced", err)
	}
	if len(rec.lookups) != 0 {
		t.Errorf("mDNS used for a non-DNS failure: %v", rec.lookups)
	}
}

// The original DNS error must survive the fallback, with the mDNS failure
// attached -- otherwise this surfaces as a bare "unreachable".
func TestDialContext_LookupFailureWrapsOriginal(t *testing.T) {
	orig := dnsFailure("nas.local")
	rec := &recorder{
		dialErr:   map[string]error{"nas.local:5000": orig},
		lookupErr: errors.New("no response"),
	}
	r := newResolverWith(rec)

	_, err := r.DialContext(context.Background(), "tcp", "nas.local:5000")
	if !errors.Is(err, orig) {
		t.Errorf("err = %v, want the original DNS error preserved", err)
	}
}

func TestDialContext_CachesResult(t *testing.T) {
	rec := &recorder{
		dialErr:   map[string]error{"nas.local:5000": dnsFailure("nas.local")},
		lookupIP:  netip.MustParseAddr("192.168.1.144"),
		lookupTTL: 2 * time.Minute,
	}
	r := newResolverWith(rec)
	ctx := context.Background()

	if _, err := r.DialContext(ctx, "tcp", "nas.local:5000"); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if _, err := r.DialContext(ctx, "tcp", "nas.local:5000"); err != nil {
		t.Fatalf("second dial: %v", err)
	}
	if len(rec.lookups) != 1 {
		t.Errorf("lookups = %d, want 1 (second dial should hit the cache)", len(rec.lookups))
	}
}

// A cached address that stops working (DHCP moved the device) must not be
// sticky -- the entry is dropped and the name re-resolved.
func TestDialContext_StaleCacheIsReResolved(t *testing.T) {
	rec := &recorder{
		dialErr: map[string]error{
			"nas.local:5000":     dnsFailure("nas.local"),
			"192.168.1.144:5000": errors.New("no route to host"),
		},
		lookupIP:  netip.MustParseAddr("192.168.1.144"),
		lookupTTL: 2 * time.Minute,
	}
	r := newResolverWith(rec)
	r.store("nas.local", netip.MustParseAddr("192.168.1.144"), time.Minute)

	// Cached dial fails -> entry dropped -> system dial fails (DNS) -> mDNS.
	_, _ = r.DialContext(context.Background(), "tcp", "nas.local:5000")
	if len(rec.lookups) != 1 {
		t.Errorf("lookups = %d, want 1 re-resolution after the stale cached dial", len(rec.lookups))
	}
}

func TestCacheTTLClamping(t *testing.T) {
	r := New()
	addr := netip.MustParseAddr("10.0.0.5")

	r.store("a.local", addr, time.Second) // below floor
	if _, ok := r.cached("a.local"); !ok {
		t.Error("short TTL should have been raised to the floor, not expired instantly")
	}

	r.store("b.local", addr, 24*time.Hour) // above ceiling
	r.mu.Lock()
	e := r.cache["b.local"]
	r.mu.Unlock()
	if time.Until(e.expires) > maxCacheTTL+time.Second {
		t.Errorf("TTL not clamped to the ceiling: expires in %v", time.Until(e.expires))
	}
}

func TestCacheIsCaseInsensitive(t *testing.T) {
	r := New()
	r.store("NAS.local", netip.MustParseAddr("10.0.0.5"), time.Minute)
	if _, ok := r.cached("nas.LOCAL"); !ok {
		t.Error("cache lookup should be case-insensitive")
	}
}
