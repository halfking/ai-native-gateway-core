package providercap

// egress_guard.go — preflight for probe/quota egress URLs (2026-09-09 audit
// round 3, round-2 unfixed item #10).
//
// The quota fetcher and the bg probe domain send a credential's API key to a
// base URL that admins configure per provider. Before this guard, those
// requests went out on bare http.Clients with zero URL vetting: a typo'd or
// malicious base URL could carry a live provider key into a cloud metadata
// endpoint (169.254.169.254 on AWS/GCP, 100.100.100.200 on Aliyun — the CGNAT
// range 100.64.0.0/10) or an internal service. The notification channels were
// already hardened with safehttpclient (F-6/9d7df18b2); this closes the
// cheapest gap on the data-probe plane.
//
// Scope: scheme + IP-literal checks only. DNS names that resolve into
// denied ranges are NOT caught here — full coverage needs the safehttpclient
// dial-hook swap, tracked as follow-up. This guard is defense-in-depth at
// configuration time, not a sandbox.
//
// Policy:
//   - unconditional deny: schemes other than http/https, and IP-literal
//     hosts inside the link-local (169.254.0.0/16 + fe80::/10) or CGNAT
//     (100.64.0.0/10) metadata ranges — no legitimate LLM provider lives
//     there, so no opt-out exists.
//   - private/loopback hosts (RFC1918, fc00::/7, 127.0.0.0/8): allowed by
//     default because self-hosted vLLM/one-api gateways legitimately live
//     there. Deployments that must forbid private egress entirely can set
//     PROVIDERCAP_EGRESS_ALLOW_PRIVATE=false.

import (
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
)

var (
	allowPrivateOnce   sync.Once
	allowPrivateEgress = true
)

// cgnatRange is denied unconditionally: Aliyun's metadata endpoint
// (100.100.100.200) sits inside the CGNAT range. IPv4 link-local
// 169.254.0.0/16 and IPv6 fe80::/10 are covered by net.IP.IsLinkLocalUnicast.
var cgnatRange = netip.MustParsePrefix("100.64.0.0/10")

func loadAllowPrivate() {
	allowPrivateOnce.Do(func() {
		v := strings.TrimSpace(strings.ToLower(os.Getenv("PROVIDERCAP_EGRESS_ALLOW_PRIVATE")))
		if v == "" {
			return // default: private egress allowed (self-hosted gateways)
		}
		allowPrivateEgress = v != "0" && v != "false" && v != "no" && v != "off"
	})
}

// EgressBlocked reports whether a probe/quota URL must not receive the
// credential's API key, and why. Empty URLs are vacuously unblocked (callers
// treat those as "unsupported" before reaching this check).
func EgressBlocked(rawURL string) (bool, string) {
	if rawURL == "" {
		return false, ""
	}
	loadAllowPrivate()
	u, err := url.Parse(rawURL)
	if err != nil {
		return true, "unparseable URL"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return true, "scheme must be http/https, got " + u.Scheme
	}
	host := u.Hostname() // strips brackets from IPv6 literals
	if host == "" {
		return true, "missing host"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// DNS names are out of scope for this preflight (see header): the
		// private/loopback opt-out below applies to IP literals only.
		return false, ""
	}
	if ip.IsLinkLocalUnicast() || inPrefix(ip, cgnatRange) {
		return true, "host is in a cloud-metadata range (" + host + ")"
	}
	if !allowPrivateEgress && (ip.IsPrivate() || ip.IsLoopback()) {
		return true, "private egress disabled (PROVIDERCAP_EGRESS_ALLOW_PRIVATE=false), host " + host
	}
	return false, ""
}

func inPrefix(ip net.IP, prefix netip.Prefix) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return prefix.Contains(addr.Unmap())
}

// WarnBlocked logs a rejected egress URL once per rejection site. Probe and
// quota paths run on background cadence, so a Warn (not Debug) is the right
// visibility for a misconfiguration that silently disabled probing.
func WarnBlocked(scope, rawURL, reason string) {
	slog.Warn("providercap: egress URL blocked, probe/quota skipped",
		"scope", scope, "url", rawURL, "reason", reason)
}
