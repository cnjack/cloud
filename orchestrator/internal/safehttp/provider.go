// Package safehttp constructs the narrowly-permitted HTTP clients used for
// configured SCM and JType providers. Provider URLs are an administrator
// supplied integration boundary, not an unrestricted server-side request
// primitive: self-hosted providers may live on RFC1918 addresses, but an
// integration must never be able to reach loopback, link-local/metadata,
// unspecified, or multicast destinations.
package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrRedirectDenied deliberately contains no target URL. Returning it from
// CheckRedirect prevents an authenticated request from being bounced to a
// different origin.
var ErrRedirectDenied = errors.New("provider redirects are not allowed")

// ErrBlockedDestination deliberately omits the resolved address so callers can
// surface a stable error without turning the control plane into an internal
// network discovery oracle.
var ErrBlockedDestination = errors.New("provider destination is not allowed")

// NewProviderClient returns the single transport policy for Provider and JType
// traffic. It preserves RFC1918/ULA support for self-hosted installations, but
// refuses redirect hops and unsafe destinations at the final dial address (so a
// DNS rebind is caught too). Loopback is allowed only for an explicit http
// localhost development URL.
func NewProviderClient(baseURL string, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Provider requests are authenticated and must be dial-guarded end to end.
	// ProxyFromEnvironment would move the dial boundary to an arbitrary proxy,
	// allowing that proxy to reach metadata/internal destinations with our
	// Authorization header. These requests deliberately go direct.
	transport.Proxy = nil
	transport.DialContext = guardedDialContext(allowLoopbackDevelopmentURL(baseURL))
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirectDenied
		},
	}
}

func allowLoopbackDevelopmentURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func providerBlockedIP(ip netip.Addr, allowLoopback bool) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	return ip.IsLoopback() && !allowLoopback
}

func guardedDialContext(allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlockedDestination
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || providerBlockedIP(ip, allowLoopback) {
				return ErrBlockedDestination
			}
			return nil
		},
	}
	return dialer.DialContext
}
