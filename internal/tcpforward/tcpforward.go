// Package tcpforward defines and validates local TCP forwarding targets.
package tcpforward

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

// Forward maps one local TCP port to a host and port reachable by the listener.
type Forward struct {
	LocalPort int
	Host      string
	Port      int
}

// TargetAddress returns the remote target in net.Dial host:port form.
func (f Forward) TargetAddress() string {
	return net.JoinHostPort(f.Host, strconv.Itoa(f.Port))
}

// BindAddress returns the explicit local address for this mapping.
func (f Forward) BindAddress(gateway bool) string {
	host := "127.0.0.1"
	if gateway {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(f.LocalPort))
}

func (f Forward) String() string {
	return fmt.Sprintf("%d:%s", f.LocalPort, f.TargetAddress())
}

// ValidateTarget rejects malformed or ambiguous remote targets. DNS names,
// IPv4 literals, and unbracketed IPv6 values (after CLI parsing) are accepted.
func ValidateTarget(host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is outside 1-65535", port)
	}
	if host == "" {
		return fmt.Errorf("remote host is empty")
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsValid() {
		return nil
	}
	if strings.TrimSpace(host) != host || strings.ContainsAny(host, ":/\\[]\x00") {
		return fmt.Errorf("invalid remote host %q", host)
	}
	for _, r := range host {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid remote host %q", host)
		}
	}
	return nil
}
