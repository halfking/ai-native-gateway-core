// Package georesolve classifies a client IP address into a coarse region
// (country / region / city) and decides whether the address is "internal"
// (RFC1918, loopback, link-local, CGNAT) or "external". The result feeds
// the dashboard's "client location" pie chart and the request-side
// fingerprint_raw JSONB column.
//
// Implementation notes (2026-09-03):
//
// The package intentionally avoids a MaxMind dependency in the default
// path so the rest of the code keeps building when no `.mmdb` file is
// shipped. We rely on a small static table of well-known CN ISP ranges
// (China Telecom / Unicom / Mobile / CERNET / Great Wall / Cernet) and
// fall back to the literal IP for everything else. This matches the
// audit requirement: internal IPs render as IPs; external IPs render as
// a coarse region label (or "ZZ/unknown" when no range matches).
//
// A pluggable Backend interface is exposed so a future change can swap
// in MaxMind/geoip2-golang behind the same Resolve() signature without
// touching downstream code. The default in-process table is the
// "static" backend.
package georesolve

import (
	"net"
	"strings"
	"sync"
)

// IPType classifies whether an address is internal or external.
type IPType string

const (
	IPTypeInternal IPType = "internal"
	IPTypeExternal IPType = "external"
	IPTypeUnknown  IPType = ""
)

// Location is the resolved geographic bucket for an external IP. Empty
// fields are omitted from the formatted label by Format().
type Location struct {
	Country string // ISO 3166-1 alpha-2 ("CN", "ZZ" when unknown)
	Region  string // province / state ("Beijing", "California"); "" when unknown
	City    string // city ("Beijing", "Mountain View"); "" when unknown
	ISP     string // "China Telecom" / "China Unicom" / etc.; "" when unknown
}

// Result bundles the classification outcome for a single IP. When
// IPType == IPTypeInternal the Location fields are zero-valued and the
// caller should display the literal IP instead.
type Result struct {
	IP       string
	IPType   IPType
	Location Location
	// Internal is true when the IP is RFC1918 / loopback / link-local
	// / CGNAT. Mirrors IPType == IPTypeInternal for callers that
	// prefer a boolean.
	Internal bool
}

// Label returns the human-readable string for the result.
//
//   - internal IPs return the literal IP (e.g. "10.1.2.3") so the
//     dashboard can show them as-is.
//   - external IPs return "country/region/city" (e.g. "CN/Beijing/Beijing"),
//     or "country" / "country/region" when finer fields are unknown,
//     or "ZZ" when nothing matches.
func (r Result) Label() string {
	if r.Internal || r.IPType == IPTypeInternal {
		if r.IP != "" {
			return r.IP
		}
		return "internal"
	}
	if r.Location.Country == "" {
		return "ZZ"
	}
	parts := []string{r.Location.Country}
	if r.Location.Region != "" {
		parts = append(parts, r.Location.Region)
	}
	if r.Location.City != "" {
		parts = append(parts, r.Location.City)
	}
	return strings.Join(parts, "/")
}

// ─── Backend interface (pluggable) ────────────────────────────────────────────

// Backend resolves an IP to a Location. Implementations MUST be safe for
// concurrent use. Returning ok=false signals "don't know" — Resolve()
// will then bucket the IP as IPTypeExternal with a zero Location, so
// callers can still key on the literal IP.
type Backend interface {
	Lookup(ip net.IP) (loc Location, ok bool)
}

// StaticBackend is the default offline classifier. It covers the well-
// known CN ISP ranges that matter for the operational dashboard; for
// anything outside the table, it returns ok=false so the caller falls
// back to the literal IP path.
type StaticBackend struct {
	ranges []staticRange
}

type staticRange struct {
	cidr    *net.IPNet
	loc     Location
	comment string
}

// NewStaticBackend builds the default offline table. The table is small
// (~30 entries) and covers the CN ISPs the operator cares about plus
// the major international clouds we commonly see. International ranges
// are deliberately coarse ("US" / "HK" / "JP" with no city) — when a
// richer lookup is needed, plug in a MaxMind-backed Backend.
func NewStaticBackend() *StaticBackend {
	return &StaticBackend{ranges: defaultStaticRanges()}
}

// Lookup implements Backend.
func (s *StaticBackend) Lookup(ip net.IP) (Location, bool) {
	if ip == nil || s == nil {
		return Location{}, false
	}
	// Normalise: net.ParseIP returns 16-byte form for 4-byte input.
	// net.IPNet.Contains handles IPv4-in-IPv6 correctly when both sides
	// are in the same family, but our CIDRs are 4-byte so compare on
	// 4-byte form first, then fall through to 16-byte form for IPv6.
	if v4 := ip.To4(); v4 != nil {
		for _, r := range s.ranges {
			if r.cidr.IP.To4() == nil {
				continue
			}
			if r.cidr.Contains(v4) {
				return r.loc, true
			}
		}
		return Location{}, false
	}
	for _, r := range s.ranges {
		if r.cidr.IP.To4() != nil {
			continue
		}
		if r.cidr.Contains(ip) {
			return r.loc, true
		}
	}
	return Location{}, false
}

// ─── Resolver ────────────────────────────────────────────────────────────────

var (
	defaultBackend Backend = NewStaticBackend()
	backendMu      sync.RWMutex
)

// SetBackend replaces the default backend. Safe for concurrent use; the
// resolver takes a snapshot per Resolve() call.
func SetBackend(b Backend) {
	backendMu.Lock()
	defer backendMu.Unlock()
	if b == nil {
		defaultBackend = NewStaticBackend()
		return
	}
	defaultBackend = b
}

func backend() Backend {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return defaultBackend
}

// IsInternal reports whether the IP string is in a non-routable range
// (RFC1918, loopback, link-local, CGNAT, IPv6 ULA, etc.). The check is
// done on a parsed net.IP so "1.2.3.4:5678" parses down to "1.2.3.4".
func IsInternal(ipStr string) bool {
	ip := parseHost(ipStr)
	if ip == nil {
		return false
	}
	return isInternalIP(ip)
}

// Resolve classifies the IP string and returns a Result. Invalid IP
// strings resolve to an empty Result with IPType=IPTypeUnknown.
func Resolve(ipStr string) Result {
	ip := parseHost(ipStr)
	if ip == nil {
		return Result{IP: ipStr, IPType: IPTypeUnknown}
	}
	if isInternalIP(ip) {
		return Result{IP: ip.String(), IPType: IPTypeInternal, Internal: true}
	}
	loc, ok := backend().Lookup(ip)
	if !ok {
		// External but unmapped — still external; caller can decide
		// whether to render as literal IP or as a "ZZ" bucket.
		return Result{IP: ip.String(), IPType: IPTypeExternal}
	}
	return Result{IP: ip.String(), IPType: IPTypeExternal, Location: loc}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func parseHost(s string) net.IP {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Strip an optional :port so "1.2.3.4:5678" works.
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	return net.ParseIP(s)
}

// isInternalIP mirrors net.IP.IsPrivate() with extra CGNAT / IPv6 ULA
// coverage so the gateway's "internal = show IP" bucket matches what
// most operators expect.
func isInternalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	if ip.IsUnspecified() {
		return true
	}
	// 100.64.0.0/10 — RFC6598 carrier-grade NAT. IsPrivate() does NOT
	// cover this range, so check it explicitly.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		// 169.254.0.0/16 — link-local; covered above but keep for clarity.
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		// 127.0.0.0/8 — loopback; covered above.
	}
	// IPv6 ULA fc00::/7 — IsPrivate() handles it on go1.22+, but be
	// explicit so older toolchains stay correct.
	if v16 := ip.To16(); v16 != nil && len(v16) == 16 {
		if (v16[0] & 0xfe) == 0xfc {
			return true
		}
	}
	return false
}