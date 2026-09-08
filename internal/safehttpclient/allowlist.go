package safehttpclient

import (
	"net"
	"strings"
)

// Allowlist manages trusted destinations that bypass SSRF protection.
type Allowlist struct {
	hostnames    map[string]bool // Exact hostname matches
	cidrs        []*net.IPNet    // CIDR blocks
	domainSuffix []string        // Domain suffixes (e.g., ".internal.corp")
}

// newAllowlist creates an allowlist from rules.
// Rule formats:
//   - "localhost" → exact hostname
//   - "10.0.0.0/8" → CIDR block
//   - "*.internal.corp" → domain suffix (any subdomain)
func newAllowlist(rules []string) *Allowlist {
	a := &Allowlist{
		hostnames: make(map[string]bool),
	}

	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}

		// CIDR block
		if strings.Contains(rule, "/") {
			_, cidr, err := net.ParseCIDR(rule)
			if err == nil {
				a.cidrs = append(a.cidrs, cidr)
				continue
			}
		}

		// Domain suffix pattern
		if strings.HasPrefix(rule, "*.") {
			suffix := strings.TrimPrefix(rule, "*")
			a.domainSuffix = append(a.domainSuffix, strings.ToLower(suffix))
			continue
		}

		// Exact hostname
		a.hostnames[strings.ToLower(rule)] = true
	}

	return a
}

// IsAllowed checks if a hostname/IP is in the allowlist.
func (a *Allowlist) IsAllowed(host string) bool {
	if a == nil {
		return false
	}

	host = strings.ToLower(host)

	// Check exact hostname
	if a.hostnames[host] {
		return true
	}

	// Check domain suffix
	for _, suffix := range a.domainSuffix {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}

	// Check CIDR (if host is an IP)
	if ip := net.ParseIP(host); ip != nil {
		for _, cidr := range a.cidrs {
			if cidr.Contains(ip) {
				return true
			}
		}
	}

	return false
}
