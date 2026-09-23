package bg

import (
	"strings"
	"testing"
)

// R57 B7 钉桩：rollup 维度清单必须含真源 client_ip（HOST(r.client_ip)，
// 740 view 链投影），virtual_ip 保留为遗留对照维度。SQL 钉桩测不出语义错，
// 但能钉住「维度被误删/表达式被改坏」这一类回归——738 事故（view 链缺列
// 令 rollup 每分钟空转）的清单面护栏。
func TestRollupDimQueries_clientIPAndLegacyVirtualIP(t *testing.T) {
	var clientIP, virtualIP string
	var seen = map[string]string{}
	for _, dq := range rollupDimQueries {
		if _, dup := seen[dq.dimType]; dup {
			t.Fatalf("duplicate dim type %q", dq.dimType)
		}
		seen[dq.dimType] = dq.dimKey
	}
	clientIP, virtualIP = seen["client_ip"], seen["virtual_ip"]
	if clientIP == "" {
		t.Fatal("client_ip dim missing from rollupDimQueries")
	}
	if !strings.Contains(clientIP, "HOST(r.client_ip)") {
		t.Fatalf("client_ip dim must aggregate HOST(r.client_ip), got %q", clientIP)
	}
	if virtualIP == "" {
		t.Fatal("virtual_ip legacy dim missing (retained for operator comparison)")
	}
	if !strings.Contains(virtualIP, "r.virtual_ip") {
		t.Fatalf("virtual_ip legacy dim must aggregate r.virtual_ip, got %q", virtualIP)
	}
}
