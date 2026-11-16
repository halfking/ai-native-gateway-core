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

	// ★ 2026-11-17（12h 审计 D1 回归门）：`OR pep.is_primary = TRUE` 逃逸口**不得**回来。
	//   它让探针在**没有协议匹配行**时仍取 primary pep 行 ⇒ 拿到别的协议的端点 URL，
	//   而探针请求形态按 p.protocol 构造 ⇒ 用 A 协议打 B 端点 ⇒ 404/400
	//   ⇒ binding 被判 broken_confirmed ⇒ 供应商整体变暗（端点其实健康）。
	//   权威口径：internal/endpointselect/selector.go Select() Stage1（协议匹配）
	//   → Stage4（无匹配回落 cand.BaseURL）。
	//
	//   ★ 判据必须只量 **SQL**，不能量注释：本函数里就有一处注释写着
	//   "OR pep.is_primary = TRUE"（解释它为何被删）⇒ 直接 grep 整段源码会
	//   命中注释而**误报**。⇒ 先剥掉 -- 注释行再判。
	sqlOnly := stripSQLComments(rest)
	if strings.Contains(sqlOnly, "pep.is_primary = TRUE") {
		t.Error("resolveDirectTarget 不得用 `OR pep.is_primary = TRUE` 逃逸口 —— 只能取协议匹配行（与 endpointselect.Select 的 Stage1→Stage4 同口径）")
	}
	// 协议匹配判据必须真的在 SQL 里（而不是靠 ORDER BY 里的 DESC 排序碰运气）。
	if !strings.Contains(sqlOnly, "AND pep.protocol = COALESCE(p.protocol, 'openai-completions')") {
		t.Error("resolveDirectTarget 的 pep 选取必须带协议匹配判据 `AND pep.protocol = COALESCE(p.protocol, 'openai-completions')`")
	}
}
