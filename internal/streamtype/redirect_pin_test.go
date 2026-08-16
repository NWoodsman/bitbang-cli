package streamtype

import "testing"

// A pinned --target must not be repointed by a redirect. resolveTarget hands
// out the mutable connTarget in fixed mode and Target is never restored, so a
// cross-host rebind would move the session for the rest of its life -- a
// cooperative target, or an open redirect on it, would be enough.
func TestAllowRebindWithPinnedTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string // --target, empty for dynamic mode
		to     string // host a redirect points at
		want   bool
	}{
		{"dynamic mode follows anywhere", "", "elsewhere.example", true},
		{"dynamic mode follows a port change", "", "nas.local:5000", true},

		{"pinned: same host, new port", "nas.local", "nas.local:5000", true},
		{"pinned: same host and port", "nas.local:80", "nas.local:80", true},
		{"pinned: port dropped", "nas.local:5000", "nas.local", true},
		{"pinned: case-insensitive host", "NAS.local", "nas.local:5000", true},

		{"pinned: different host", "nas.local", "evil.example", false},
		{"pinned: different host, same port", "nas.local:80", "evil.example:80", false},
		{"pinned: subdomain is a different host", "nas.local", "sub.nas.local", false},
		{"pinned: bare IP swap", "192.168.1.10:80", "169.254.169.254:80", false},
		{"pinned: suffix trick", "nas.local", "evilnas.local", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &HTTPHandler{Target: tt.target, connTarget: tt.target}
			if got := h.allowRebind(tt.to); got != tt.want {
				t.Errorf("allowRebind(%q) with --target %q = %v, want %v",
					tt.to, tt.target, got, tt.want)
			}
		})
	}
}

// The comparison is against Target, not connTarget, so a chain of redirects
// cannot walk away from the pin one hop at a time.
func TestAllowRebindDoesNotDriftAcrossHops(t *testing.T) {
	h := &HTTPHandler{Target: "nas.local", connTarget: "nas.local"}

	// Legitimate first hop: same host, new port.
	if !h.allowRebind("nas.local:5000") {
		t.Fatal("same-host port change should be allowed")
	}
	h.connTarget = "nas.local:5000" // as the callback would

	// Second hop to another host must still be refused, even though
	// connTarget has moved.
	if h.allowRebind("evil.example") {
		t.Error("a second hop escaped the pin after a legitimate port change")
	}
}

func TestHostOnly(t *testing.T) {
	for in, want := range map[string]string{
		"nas.local":       "nas.local",
		"nas.local:5000":  "nas.local",
		"[fd00::1]:8080":  "fd00::1",
		"[fd00::1]":       "fd00::1",
		"192.168.1.10:80": "192.168.1.10",
	} {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}
