package bg

// node_probe_endpoint_align_test.go — 探针目标 URL 与路由 endpoint 表对齐门。
//
// 2026-11-17：resolveDirectTarget 此前只读 providers.base_url，路由却从
// provider_endpoint_protocols 选路 ⇒ NIM 等多 endpoint 供应商状态/流量错位。

import (
	"os"
	"strings"
	"testing"
)

func TestResolveDirectTargetPrefersProviderEndpointProtocols(t *testing.T) {
	raw, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read node_probe.go: %v", err)
	}
	body := string(raw)
	idx := strings.Index(body, "func (w *NodeProbeWorker) resolveDirectTarget(")
	if idx < 0 {
		t.Fatal("resolveDirectTarget 不存在")
	}
	rest := body[idx:]
	if end := strings.Index(rest, "\nfunc ("); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "provider_endpoint_protocols") {
		t.Error("resolveDirectTarget 必须读 provider_endpoint_protocols（与路由候选同表）")
	}
	if !strings.Contains(rest, "COALESCE(pep_pick.base_url, p.base_url)") {
		t.Error("resolveDirectTarget 必须 COALESCE(pep, providers.base_url) —— 无 pep 行时回落主 URL")
	}
	// 严格臂与 loose 回退臂都必须对齐，否则 status 非 active 时又打回主 URL。
	if strings.Count(rest, "provider_endpoint_protocols") < 2 {
		t.Error("严格臂与 loose 回退臂都必须 JOIN provider_endpoint_protocols")
	}
}
