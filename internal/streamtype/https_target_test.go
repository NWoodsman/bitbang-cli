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

// Regression: an explicit scheme in the URL used to break the per-request
// target. OnConnect stripped it correctly, but requests arrive carrying the
// path as typed ("/https://localhost:8971/"), targetPrefix ("/localhost:8971")
// did not match, and resolveTarget's "first segment with a colon is a host"
// heuristic adopted "https:" as the hostname -- then CheckRedirect wrote that
// into connTarget and poisoned every later request in the session.
//
// Observed live as:
//
//	Target updated: https: (from redirect)
//	Get "https://https//localhost:8971/": lookup https: server misbehaving
func TestResolveTarget_SchemeInRequestPath(t *testing.T) {
	h := NewHTTPProxy("", "uid", "bitba.ng", "", false)
	h.connTarget = "localhost:8971"
	h.targetPrefix = "/localhost:8971"
	h.connectPrefix = "/localhost:8971"

	for _, in := range []string{
		"/https://localhost:8971/",
		"/http://localhost:8971/",
		"/HTTPS://localhost:8971/",
		"/https:/localhost:8971/",
	} {
		target, path := h.resolveTarget(in)
		if target != "localhost:8971" {
			t.Errorf("resolveTarget(%q) target = %q, want localhost:8971", in, target)
		}
		if path != "/" {
			t.Errorf("resolveTarget(%q) path = %q, want /", in, path)
		}
		if h.connTarget != "localhost:8971" {
			t.Fatalf("connTarget poisoned to %q by %q", h.connTarget, in)
		}
	}
}

func TestStripScheme(t *testing.T) {
	cases := map[string]string{
		"https://host:8971":     "host:8971",
		"http://host:8971":      "host:8971",
		"HTTPS://host:8971":     "host:8971",
		"https:/host:8971":      "host:8971",
		"https%3A%2F%2Fhost:80": "host:80",
		"host:8971":             "host:8971", // untouched
		"httpsomething/x":       "httpsomething/x",
	}
	for in, want := range cases {
		if got := stripScheme(in); got != want {
			t.Errorf("stripScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikeHostPort(t *testing.T) {
	yes := []string{"host:8971", "192.0.2.1:80", "nas.local:5000"}
	no := []string{"https:", "http:", "", "webpages", "cgi-bin"}
	for _, s := range yes {
		if !looksLikeHostPort(s) {
			t.Errorf("looksLikeHostPort(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if looksLikeHostPort(s) {
			t.Errorf("looksLikeHostPort(%q) = true, want false", s)
		}
	}
}
