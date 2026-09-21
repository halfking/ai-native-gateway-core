package georesolve

import (
	"strings"
	"testing"
)

// TestIsInternal covers the well-known non-routable ranges so an
// accidental CIDR-table regression is caught immediately.
func TestIsInternal(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
		why  string
	}{
		// RFC1918
		{"10.0.0.1", true, "10/8 RFC1918"},
		{"10.255.255.255", true, "10/8 RFC1918 upper bound"},
		{"172.16.0.1", true, "172.16/12 RFC1918"},
		{"172.31.255.255", true, "172.16/12 RFC1918 upper bound"},
		{"172.32.0.0", false, "172.32/12 is public (out of RFC1918)"},
		{"192.168.1.1", true, "192.168/16 RFC1918"},

		// Loopback
		{"127.0.0.1", true, "127/8 loopback"},
		{"127.255.255.254", true, "127/8 loopback upper bound"},

		// Link-local
		{"169.254.169.254", true, "169.254/16 link-local (AWS metadata)"},

		// CGNAT
		{"100.64.0.1", true, "100.64/10 CGNAT lower bound"},
		{"100.127.255.255", true, "100.64/10 CGNAT upper bound"},
		{"100.63.255.255", false, "100.63 is public (below CGNAT)"},
		{"100.128.0.0", false, "100.128 is public (above CGNAT)"},

		// Multicast
		{"224.0.0.1", true, "224/4 multicast"},

		// Public (must
		{"8.8.8.8", false, "8.8.8.8 is Google public DNS"},
		{"1.1.1.1", false, "1.1.1.1 is Cloudflare public DNS"},
		{"36.0.0.1", false, "36.0.0.0/12 is China Telecom (public)"},
		{"52.0.0.1", false, "52/8 is AWS (public)"},

		// IPv6
		{"::1", true, "::1 IPv6 loopback"},
		{"fc00::1", true, "fc00::/7 IPv6 ULA"},
		{"fd00::1", true, "fd00::/8 falls inside fc00::/7"},
		{"fe80::1", true, "fe80::/10 IPv6 link-local"},
		{"2001:4860:4860::8888", false, "Google IPv6 DNS (public)"},

		// Bogus inputs
		{"", false, "empty string"},
		{"not-an-ip", false, "garbage"},
		{"1.2.3.4:5678", false, "host:port stripped to 1.2.3.4 (public)"},
	}
	for _, tc := range cases {
		t.Run(tc.ip+"_"+strings.ReplaceAll(tc.why, " ", "_"), func(t *testing.T) {
			if got := IsInternal(tc.ip); got != tc.want {
				t.Errorf("IsInternal(%q) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestResolveInternal(t *testing.T) {
	r := Resolve("10.1.2.3")
	if !r.Internal {
		t.Fatalf("expected Internal=true, got %+v", r)
	}
	if r.IPType != IPTypeInternal {
		t.Errorf("IPType = %q, want %q", r.IPType, IPTypeInternal)
	}
	if got := r.Label(); got != "10.1.2.3" {
		t.Errorf("Label() = %q, want %q", got, "10.1.2.3")
	}
}

func TestResolveExternalKnownRange(t *testing.T) {
	// 36.0.0.0/12 should map to China Telecom.
	r := Resolve("36.0.0.1")
	if r.Internal {
		t.Fatalf("36.0.0.1 should be external, got %+v", r)
	}
	if r.Location.Country != "CN" {
		t.Errorf("Location.Country = %q, want CN", r.Location.Country)
	}
	if r.Location.ISP != "China Telecom" {
		t.Errorf("Location.ISP = %q, want China Telecom", r.Location.ISP)
	}
	if !strings.HasPrefix(r.Label(), "CN/") {
		t.Errorf("Label() = %q, want CN/* prefix", r.Label())
	}
}

func TestResolveExternalUnknown(t *testing.T) {
	// 1.2.3.4 is public but not in the static table; we still classify
	// it as external and let the dashboard render it as the literal IP
	// or as "ZZ" depending on the rollup's dim_key expression.
	r := Resolve("1.2.3.4")
	if r.Internal {
		t.Fatalf("1.2.3.4 should be external, got %+v", r)
	}
	if r.IPType != IPTypeExternal {
		t.Errorf("IPType = %q, want %q", r.IPType, IPTypeExternal)
	}
}

func TestResolveInvalid(t *testing.T) {
	r := Resolve("garbage")
	if r.IPType != IPTypeUnknown {
		t.Errorf("IPType = %q, want %q", r.IPType, IPTypeUnknown)
	}
}

// TestStaticBackendCIDRs sanity-checks that every CIDR in the static
// table parses successfully. The table is built via mustCIDR which
// panics on parse failure; this test exists to fail loudly if someone
// edits the table and accidentally introduces a malformed entry that
// would otherwise panic at init.
func TestStaticBackendCIDRs(t *testing.T) {
	backend := NewStaticBackend()
	if len(backend.ranges) < 10 {
		t.Fatalf("static table too small: %d entries, want >=10", len(backend.ranges))
	}
	seen := make(map[string]bool, len(backend.ranges))
	for _, r := range backend.ranges {
		if r.cidr == nil {
			t.Fatalf("static range has nil CIDR: %+v", r)
		}
		s := r.cidr.String()
		if seen[s] {
			t.Errorf("duplicate CIDR in static table: %s", s)
		}
		seen[s] = true
	}
}