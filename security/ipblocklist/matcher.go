package ipblocklist

import (
	"net"
	"strings"
)

// MatchIP returns true when ip falls inside entry (single IP or CIDR).
func MatchIP(entry string, ip net.IP) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" || ip == nil {
		return false
	}
	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil || network == nil {
			return false
		}
		return network.Contains(ip)
	}
	parsed := net.ParseIP(entry)
	if parsed == nil {
		return false
	}
	return parsed.Equal(ip)
}

// NormalizeClientIP extracts the best client IP from remote address.
func NormalizeClientIP(remoteAddr string, forwardedFor string) net.IP {
	xff := strings.TrimSpace(forwardedFor)
	if xff != "" {
		parts := strings.Split(xff, ",")
		if candidate := strings.TrimSpace(parts[0]); candidate != "" {
			if host, _, err := net.SplitHostPort(candidate); err == nil {
				candidate = host
			}
			if ip := net.ParseIP(candidate); ip != nil {
				return ip
			}
		}
	}
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return nil
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return net.ParseIP(host)
}
