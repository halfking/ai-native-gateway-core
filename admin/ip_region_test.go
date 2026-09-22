package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"net/netip"
)

// Wave 3 B7 钉桩测试：内网/保留段直显、外网段表归类、无库优雅降级、
// 饼图合并语义。

func TestIsPrivateOrReserved(t *testing.T) {
	cases := map[string]bool{
		"10.1.2.3":        true,
		"172.16.0.1":      true,
		"172.31.255.9":    true,
		"172.32.0.1":      false, // just outside RFC1918
		"192.168.1.1":     true,
		"127.0.0.1":       true,
		"169.254.3.4":     true, // link-local
		"100.115.9.9":     true, // CGNAT
		"192.0.2.55":      true, // documentation
		"8.8.8.8":         false,
		"114.114.114.14":  false,
		"::1":             true, // loopback
		"fd12:3456::1":    true, // unique-local
		"2606:4700::1111": false,
	}
	for ip, want := range cases {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			t.Fatalf("parse %s: %v", ip, err)
		}
		if got := isPrivateOrReserved(addr); got != want {
			t.Errorf("%s: got %v want %v", ip, got, want)
		}
	}
}

func TestClassifyVirtualIP_IntranetAndSentinelsPassThrough(t *testing.T) {
	resetGeoIPCache(t) // empty segment table (never loads from disk)
	// No segment table loaded (empty default path) — everything degrades or
	// passes through.
	for _, in := range []string{"10.0.0.8", "192.168.10.20", "unknown", "", "-", "not-an-ip", "CN·广东·深圳"} {
		if got := classifyVirtualIP(in); got != in {
			t.Errorf("pass-through %q: got %q", in, got)
		}
	}
}

func TestClassifyVirtualIP_SegmentTableResolution(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "segments.csv")
	content := `# cidr,country,province,city
114.114.0.0/16,CN,江苏,南京
8.8.0.0/16,US,California,Mountain View
2606:4700::/32,CN
`
	if err := os.WriteFile(csvPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_GEOIP_CSV", csvPath)
	resetGeoIPCache(t)

	if got := classifyVirtualIP("114.114.115.1"); got != "CN·江苏·南京" {
		t.Errorf("public hit: got %q", got)
	}
	if got := classifyVirtualIP("8.8.8.8"); got != "US·California·Mountain View" {
		t.Errorf("public hit v4: got %q", got)
	}
	if got := classifyVirtualIP("2606:4700:4700::1111"); got != "CN" {
		t.Errorf("public hit v6: got %q", got)
	}
	// Public but outside every segment → degrade to raw IP.
	if got := classifyVirtualIP("9.9.9.9"); got != "9.9.9.9" {
		t.Errorf("degrade: got %q", got)
	}
	// Intranet still passes through with the table active.
	if got := classifyVirtualIP("192.168.5.5"); got != "192.168.5.5" {
		t.Errorf("intranet passthrough: got %q", got)
	}
}

func TestClassifyVirtualIPPie_MergesRegionLabels(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "segments.csv")
	content := "114.114.0.0/16,CN,江苏,南京\n"
	if err := os.WriteFile(csvPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_GEOIP_CSV", csvPath)
	resetGeoIPCache(t)

	items := []boardPieItem{
		{Key: "114.114.1.1", Requests: 5, Tokens: 100},
		{Key: "114.114.2.2", Requests: 7, Tokens: 50},
		{Key: "10.0.0.1", Requests: 3, Tokens: 9},
		{Key: "9.9.9.9", Requests: 2, Tokens: 4},
	}
	got := classifyVirtualIPPie(items)
	byKey := map[string]boardPieItem{}
	for _, it := range got {
		byKey[it.Key] = it
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 slices after merge, got %d: %+v", len(got), got)
	}
	if m := byKey["CN·江苏·南京"]; m.Requests != 12 || m.Tokens != 150 {
		t.Errorf("region merge wrong: %+v", m)
	}
	if m := byKey["10.0.0.1"]; m.Requests != 3 {
		t.Errorf("intranet slice wrong: %+v", m)
	}
	if m := byKey["9.9.9.9"]; m.Requests != 2 {
		t.Errorf("degraded slice wrong: %+v", m)
	}
}

func resetGeoIPCache(t *testing.T) {
	t.Helper()
	geoIPMu.Lock()
	geoIPSegments = nil
	geoIPLoadedAt = time.Time{} // zero: force a reload on next classify
	geoIPMu.Unlock()
	t.Cleanup(func() {
		geoIPMu.Lock()
		geoIPSegments = nil
		geoIPLoadedAt = time.Time{}
		geoIPMu.Unlock()
	})
}
