// cmd/gateway/probe_plan_lookup.go —— 探针 worker 的「路由计划」接线（2026-11-26 第36轮）
//
// ★ 为什么单独一个文件：这个接线**只**往 worker 上注入一个函数指针，而
// cmd/gateway/main.go 是万行级巨石、且被两道门按**行号**钉住
// （admin 路由快照、请求面文档对拍表）⇒ ★ 把辅助函数放进 main.go 会让
// 它下面**每一行**的行号都位移 ⇒ 两道门同时变红 ⇒ ★★ 为一次接线去重钉
// 两张表不值得 ⇒ ★ 辅助函数放独立文件，main.go 只留调用点。
package main

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// wireGatewayPlanLookup injects the routing layer's **own candidate resolver**
// into a probe worker.
//
// ★ 2026-11-26（第36轮 · 第四处修正）：判据「路由层对这个**模型**整体零候选
// ⇒ 钉住凭据的网关轮必然是 503 no_candidate」**问被测系统自己**——
// provider.Client.GetCandidatesByModality 是路由层解析请求候选的唯一入口，
// 也是 admin 面板 /api/routing/resolve 的 plan_order 的同源实现
// （admin/routing_plan_order.go 的 LiveRoutingSource.Resolver 就是它）。
// ⇒ 探针侧不抄一份可路由性合取 ⇒ 判据不会随路由层演进而漂移
// （第三处修正的 SQL 正是一份近似，第一版还问错了表 —— 见 AGENTS L120 轮块）。
//
// ★ 接线只有这一处 ⇒ 两个探针 worker 构造点共用（判据单源）。
// ★ provider client 不可用（没接 DB）⇒ 不接线 ⇒ 判据 fail-open（照跑网关轮）。
func wireGatewayPlanLookup(w *bg.NodeProbeWorker, pc *provider.Client) {
	if w == nil || pc == nil || !pc.Enabled() {
		return
	}
	w.SetGatewayPlanLookup(func(ctx context.Context, model, tenantID, modality string) (int, error) {
		cands, _, err := pc.GetCandidatesByModality(ctx, model, "", tenantID, modality)
		if err != nil {
			return 0, err
		}
		return len(cands), nil
	})
}
