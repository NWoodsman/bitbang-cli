package streamtype

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hostPort strips the scheme from an httptest server URL, since that is the
// form OnConnect takes as a target.
func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	s := strings.TrimPrefix(rawURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	return s
}

// A plaintext target must stay on http -- the TLS fallback probe should
// never fire for it.
func TestOnConnect_PlaintextTargetStaysHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	h := NewHTTPProxy(hostPort(t, srv.URL), "uid", "bitba.ng", "", false)
	if err := h.OnConnect("/"); err != nil {
		t.Fatalf("OnConnect: %v", err)
	}
	if got := h.Scheme(); got != "http" {
		t.Errorf("scheme = %q, want http", got)
	}
	if h.SkipVerify() {
		t.Error("SkipVerify = true for a plaintext target")
	}
}

// The case the whole change exists for: a target that speaks ONLY TLS, with
// a self-signed certificate. httptest.NewTLSServer is self-signed by
// construction, so this reproduces a Frigate/NAS/OctoPrint box exactly.
//
// Before this change the plaintext probe error was discarded, OnConnect
// returned nil, and every subsequent request 502'd with no hint why.
func TestOnConnect_SelfSignedHTTPSTargetIsDetected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewHTTPProxy(hostPort(t, srv.URL), "uid", "bitba.ng", "", false)
	if err := h.OnConnect("/"); err != nil {
		t.Fatalf("OnConnect returned error for a reachable HTTPS target: %v", err)
	}
	if got := h.Scheme(); got != "https" {
		t.Fatalf("scheme = %q, want https", got)
	}
	// Loopback is local, so verification must be skipped -- otherwise the
	// self-signed cert would fail and the target stays unreachable.
	if !h.SkipVerify() {
		t.Error("SkipVerify = false for a loopback HTTPS target; self-signed certs would fail")
	}
}

// An unreachable target must fail Connect with a clear message rather than
// being accepted and then 502ing on every request.
func TestOnConnect_DeadTargetFailsClearly(t *testing.T) {
	// Port 1 on loopback: nothing listens, connection refused immediately.
	h := NewHTTPProxy("127.0.0.1:1", "uid", "bitba.ng", "", false)
	err := h.OnConnect("/")
	if err == nil {
		t.Fatal("OnConnect accepted a dead target")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error = %q, want it to mention unreachable", err)
	}
}

// An http -> https redirect should switch the session scheme rather than
// erroring out, which is what the old requiresHTTPS path did.
func TestOnConnect_HTTPSRedirectSwitchesScheme(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsSrv.Close()

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL+"/", http.StatusMovedPermanently)
	}))
	defer plain.Close()

	h := NewHTTPProxy(hostPort(t, plain.URL), "uid", "bitba.ng", "", false)
	if err := h.OnConnect("/"); err != nil {
		t.Fatalf("OnConnect: %v", err)
	}
	if got := h.Scheme(); got != "https" {
		t.Errorf("scheme = %q, want https after an http->https redirect", got)
	}
}

func TestIsLocalHost(t *testing.T) {
	local := []string{
		"localhost", "localhost:8971", "127.0.0.1:8971", "[::1]:8971",
		"192.168.1.50", "10.0.0.5:443", "172.16.4.4:8123",
		"nas.local", "frigate.local:8971",
		"169.254.10.1",    // link-local
		"100.100.1.1:443", // CGNAT / Tailscale
		"octopi",          // bare LAN hostname
	}
	for _, tc := range local {
		if !isLocalHost(tc) {
			t.Errorf("isLocalHost(%q) = false, want true", tc)
		}
	}

	public := []string{
		"bitba.ng", "bitba.ng:443", "example.com",
		"8.8.8.8:443", "93.184.216.34",
	}
	for _, tc := range public {
		if isLocalHost(tc) {
			t.Errorf("isLocalHost(%q) = true, want false (public hosts must verify certs)", tc)
		}
	}
}
