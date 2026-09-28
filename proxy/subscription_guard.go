package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type subscriptionIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// subscriptionGuardTransport rechecks every redirect and replaces the URL's
// destination with a vetted IP. An HTTP proxy therefore receives a CONNECT
// target (or absolute HTTP URI) containing that IP, while Host and TLS SNI
// retain the subscription hostname. DNS cannot change the dial destination
// after validation, including when HTTP(S)_PROXY is configured.
type subscriptionGuardTransport struct {
	base         *http.Transport
	allowPrivate bool
	resolver     subscriptionIPResolver
	dialContext  func(context.Context, string, string) (net.Conn, error)
}

func newSubscriptionGuardTransport(base http.RoundTripper, allowPrivate bool, resolver subscriptionIPResolver, dialContext func(context.Context, string, string) (net.Conn, error)) (*subscriptionGuardTransport, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, errors.New("proxy: subscription client requires an http.Transport")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &subscriptionGuardTransport{base: transport, allowPrivate: allowPrivate, resolver: resolver, dialContext: dialContext}, nil
}

func (g *subscriptionGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host, port, err := subscriptionURLHostPort(req.URL)
	if err != nil {
		return nil, err
	}
	addresses, err := g.resolveAddresses(req.Context(), host)
	if err != nil {
		return nil, err
	}
	var attempts []error
	for _, ip := range addresses {
		pinnedAddress := net.JoinHostPort(ip.String(), port)
		transport := g.base.Clone()
		transport.DisableKeepAlives = true // one subscription fetch; no orphaned pools per redirect/IP
		tlsConfig := &tls.Config{}
		if transport.TLSClientConfig != nil {
			tlsConfig = transport.TLSClientConfig.Clone()
		}
		tlsConfig.ServerName = host
		transport.TLSClientConfig = tlsConfig

		var proxyURL *url.URL
		if g.base.Proxy != nil {
			proxyURL, err = g.base.Proxy(req) // proxy selection uses the original hostname
			if err != nil {
				return nil, fmt.Errorf("proxy: subscription proxy selection: %w", err)
			}
		}
		transport.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
		dial := g.dialContext
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if proxyURL == nil && address != pinnedAddress {
				return nil, fmt.Errorf("proxy: subscription dial target changed")
			}
			return dial(ctx, network, address)
		}
		// The same TLSClientConfig is used for both hops of an HTTPS proxy
		// tunnel. Dial the proxy with its own certificate name, then let the
		// transport use the origin name above for TLS inside CONNECT.
		transport.DialTLSContext = nil // a custom TLS dialer must not bypass the pinned destination
		if proxyURL != nil && strings.EqualFold(proxyURL.Scheme, "https") {
			proxyTLS := &tls.Config{}
			if g.base.TLSClientConfig != nil {
				proxyTLS = g.base.TLSClientConfig.Clone()
			}
			proxyTLS.ServerName = proxyURL.Hostname()
			proxyTLS.NextProtos = []string{"http/1.1"} // CONNECT below writes HTTP/1.1
			transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				plain, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				conn := tls.Client(plain, proxyTLS)
				if err := conn.HandshakeContext(ctx); err != nil {
					_ = conn.Close()
					return nil, err
				}
				return conn, nil
			}
		}

		pinned := req.Clone(req.Context())
		cloneURL := *req.URL
		cloneURL.Host = pinnedAddress
		pinned.URL = &cloneURL
		pinned.Host = req.URL.Host
		resp, roundTripErr := transport.RoundTrip(pinned)
		if roundTripErr != nil {
			transport.CloseIdleConnections()
			attempts = append(attempts, roundTripErr)
			continue
		}
		resp.Request = req // keep relative redirects anchored to the original hostname
		if resp.Body == nil {
			transport.CloseIdleConnections()
		} else {
			resp.Body = &subscriptionResponseBody{ReadCloser: resp.Body, transport: transport}
		}
		return resp, nil
	}
	return nil, fmt.Errorf("proxy: subscription dial failed: %w", errors.Join(attempts...))
}

type subscriptionResponseBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *subscriptionResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}

func subscriptionURLHostPort(u *url.URL) (string, string, error) {
	if u == nil || u.Hostname() == "" || u.Opaque != "" {
		return "", "", errors.New("proxy: invalid subscription redirect URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", errors.New("proxy: invalid subscription redirect URL")
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", errors.New("proxy: invalid subscription port")
		}
	}
	return u.Hostname(), port, nil
}

func (g *subscriptionGuardTransport) resolveAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	var addresses []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{ip.Unmap()}
	} else {
		resolved, err := g.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("proxy: resolve subscription host: %w", err)
		}
		addresses = resolved
	}
	if len(addresses) == 0 {
		return nil, errors.New("proxy: subscription host has no IP addresses")
	}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	allowed := make([]netip.Addr, 0, len(addresses))
	for _, raw := range addresses {
		ip := raw.Unmap()
		if !ip.IsValid() || ip.Zone() != "" || (!g.allowPrivate && !publicSubscriptionIP(ip)) {
			return nil, fmt.Errorf("proxy: subscription host %q resolves to a prohibited address", host)
		}
		if _, exists := seen[ip]; !exists {
			seen[ip] = struct{}{}
			allowed = append(allowed, ip)
		}
	}
	return allowed, nil
}

var blockedSubscriptionIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

var globalSubscriptionIPv6 = netip.MustParsePrefix("2000::/3")
var blockedSubscriptionIPv6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/32"),     // transition/tunnel addresses
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4 can embed a private IPv4 target
}

func publicSubscriptionIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is4() {
		for _, block := range blockedSubscriptionIPv4 {
			if block.Contains(ip) {
				return false
			}
		}
		return true
	}
	if !globalSubscriptionIPv6.Contains(ip) {
		return false
	}
	for _, block := range blockedSubscriptionIPv6 {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
