package api

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// errBlockedHost is returned by the model-provider dial guard when the RESOLVED
// dial address is a loopback/link-local/private/unspecified IP. It carries no
// address so the blocked internal IP never leaks into a caller-visible error
// (the handlers surface a generic provider_unreachable / catalog_unavailable).
var errBlockedHost = errors.New("destination host is not allowed")

// isBlockedIP reports whether ip is one the model-provider verify/catalog probe
// must refuse to dial. It rejects the internal/reserved ranges an SSRF oracle
// would target: loopback (127.0.0.0/8, ::1), link-local unicast (169.254.0.0/16
// incl. the 169.254.169.254 cloud-metadata endpoint, fe80::/10), unique-local
// (fc00::/7) + RFC1918 private (10/8, 172.16/12, 192.168/16) — both covered by
// IsPrivate — the unspecified address (0.0.0.0, ::), and multicast.
func isBlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap() // treat an IPv4-mapped IPv6 (::ffff:10.0.0.1) as its IPv4 form
	if !ip.IsValid() {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified()
}

// dialControl builds a net.Dialer Control hook that rejects the connection when
// the ACTUAL resolved dial address is a blocked IP. Control runs on the
// post-DNS-resolution address ("ip:port"), so a DNS-rebind to an internal IP is
// blocked too — not only a literal internal host. allowPrivate is consulted at
// dial time (not construction time) so a test harness pointing a provider at an
// httptest server on 127.0.0.1 can opt out via the Server flag it closes over.
func dialControl(allowPrivate func() bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate != nil && allowPrivate() {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return errBlockedHost
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			// Control always receives a resolved numeric address; a non-IP here is
			// unexpected — refuse rather than dial blind.
			return errBlockedHost
		}
		if isBlockedIP(ip) {
			return errBlockedHost
		}
		return nil
	}
}

// guardedDialContext returns a DialContext that applies the SSRF dial guard.
func guardedDialContext(allowPrivate func() bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   dialControl(allowPrivate),
	}
	return d.DialContext
}
