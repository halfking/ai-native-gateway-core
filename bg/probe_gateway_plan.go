package bg

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// ★ 2026-11-26（第36轮 · gpt-image 探针风暴收口）：第四个跳过理由 ——
// **模型级零候选**。
//
// ── 为什么还需要它（211 真机实测，2026-11-26 01:53–02:26，现跑）★ ────────
//
//	第三处修正（ad0886c40）的判据是**凭据级**的：钉住的这把凭据对该模型还有没有
//	可用 offer（`model_offers`）＋ 凭据/供应商级排除理由（资格视图）。
//	而切流后仍然发出的 15 轮 503，网关自己的诊断给出的全是 **`plan_count=0`**
//	—— 路由层对**整个模型**就没有候选：
//	  · provider_disabled：kimi-k3（38 个 offer）里 18 条的供应商被停用、
//	    16 条凭据配额态、8 条 offer 人工下线、4 条凭据人工停用、2 条生命周期停用；
//	    deepseek-flash（17 个 offer）里 12 条供应商停用。
//	  · cred_quota：sensenova-6.8-flash（5 个 offer，2 条配额态）、
//	    kimi-k3.1-pro（9 个 offer）。
//	  · gates_open（状态闸全开 ⇒ 零候选在**别处**）：llama-3.1-nemoguard-8b-topic-control、
//	    elevenlabseleven-multilingual-sts-v2。
//	⇒ 钉住**哪一把**凭据都一样是 503 no_candidate ⇒ 网关轮零信息量。
//
// ── 判据源：路由层**自己的候选解析器**（不是我们自己写的 SQL）★★ ─────────
//
//	`provider.Client.GetCandidatesByModality` 是路由层解析请求候选的唯一入口，
//	也是 admin 面板 `/api/routing/resolve` 的 plan_order 的同源实现
//	（admin/routing_plan_order.go 的 `LiveRoutingSource.Resolver` 就是它）。
//	⇒ 判据问的**就是被测系统真正读的那条查询** ⇒ 不抄合取 ⇒ 不会漂移。
//
//	★ 第三处修正的判据是自己写的 SQL（问 `model_offers` ＋ 资格视图）——那是
//	  一份**近似**：第一版问错了表（`v_routable_credential_models` vs
//	  `model_offers`）、订正后才咬住实测残留。⇒ 本条判据刻意不重复那个错误：
//	  宁可多问一次路由层，也不自己再抄一份可路由性合取。
//
// ── 口径：参数与请求**严格对齐**（不是猜的）★ ───────────────────────────
//
//	请求侧的真实参数（domains/streaming）：
//	  · model    = 请求体里的模型名（探针发什么就问什么）；
//	  · profile  = API Key 的 client_profile（探针用系统 Key ⇒ 实测为空）；
//	  · tenant   = API Key 的租户（keyInfo.TenantID；探针用系统 Key ⇒ default，
//	211 实测探针请求的候选缓存键就是 `<model>|default|modality:text`）；
//	  · modality = detectRequestModality(body)：纯文本 ⇒ "text"；音频体 ⇒ "audio"；
//	               /v1/embeddings 端点 ⇒ "embedding"（embeddings.go:179）。
//	⇒ 探针这一侧**自己造 body、自己挑端点** ⇒ 模态由它自己算得出
//	  （见 gatewayPlanModalityFor），租户恒为 default（与真实请求同租户）。
//
// ── 失败方向：查询出错 ⇒ 按「有候选」处理（fail-open）★ ──────────────────
//
//	宁可多一条噪声请求记录，也不因为一次 DB 抖动丢掉网关轮的诊断能力。
//
// ── 已知边界（如实登记，不假装已修）★ ─────────────────────────────────
//
//	· 候选缓存有 TTL（provider/client.go 的 candCache）⇒ 读到的是**至多 TTL 旧**
//	  的候选面 ⇒ 判否方向可能滞后（多跑一轮网关轮 ＝ 安全方向）。
//	· 判据问的是**未钉住**的候选面。钉住发生在计划阶段
//	  （executors.PlanCandidatesPinned），它只绕过 URSM v2 / 状态后端的运行时
//	  可用性过滤，**绕过不了 SQL 层** ⇒ 「未钉住候选面为空 ⇒ 钉住后必为空」成立。
//	· 判据只覆盖**探针自己的**网关轮；业务请求走到同样的零候选出口时，行为不变
//	  （那是路由层自己的状态，不该由探针改写）。

// gatewayRoundSkippedNoPlan 是第四个跳过理由的令牌：路由层对该模型零候选。
const gatewayRoundSkippedNoPlan = "gateway_round_skipped_no_plan"

// gatewayPlanLookupFn 报告路由层当前对 (模型, 租户, 模态) 有多少候选。
//
// 生产接线：路由层自己的候选解析器（provider.Client.GetCandidatesByModality）。
// 测试接线：桩函数（不连数据库即可钉住跳过行为）。
type gatewayPlanLookupFn func(ctx context.Context, model, tenantID, modality string) (int, error)

// gatewayPlanModalityFor 推出**网关轮这一次请求**会带的模态。
//
// ★ 它必须与请求侧一致，否则「零候选」的结论不成立：
//   · 端点：effectiveProbeModality 判成 embedding ⇒ 探针发 /v1/embeddings
//     ⇒ 网关那一面固定用 "embedding" 查候选（embeddings.go:179）；
//   · chat/messages 面：模态由**请求体**检出（domains/streaming/modality_detect.go）
//     ⇒ 探针的 body 由 rawModality 决定：空 ⇒ 纯文本 ⇒ "text"；
//     "audio" ⇒ 音频体 ⇒ "audio"。
//
// rawModality 必须传**该条探针路径实际用的那个**（统一队列路径恒为 ""；
// legacy 路径用 DB 权威模态）。
func gatewayPlanModalityFor(model, rawModality string) string {
	if effectiveProbeModality(model, rawModality) == "embedding" {
		return "embedding"
	}
	switch strings.ToLower(strings.TrimSpace(rawModality)) {
	case "audio":
		return "audio"
	default:
		return "text"
	}
}

// gatewayPlanEmpty 报告路由层此刻对该模型是否**一个候选都没有**。
//
// ⇒ true ⇒ 钉住凭据的网关轮必然是 503 no_candidate（零信息量）⇒ 应当跳过。
//
// ★ fail-open：没有查读能力（无接线）、查询出错 ⇒ 一律返回 false（照跑网关轮）。
func (w *NodeProbeWorker) gatewayPlanEmpty(ctx context.Context, model, rawModality string) bool {
	if w == nil || w.gatewayPlanLookup == nil {
		return false
	}
	// 独立超时：判据是**旁路**的 ⇒ 它慢，不该拖住探针的心跳租约。
	// 超时 ⇒ fail-open（照跑网关轮）。
	lookupCtx, cancel := context.WithTimeout(ctx, gatewayPlanLookupTimeout)
	defer cancel()
	n, err := w.gatewayPlanLookup(lookupCtx, model, gatewayPlanTenant, gatewayPlanModalityFor(model, rawModality))
	if err != nil {
		slog.Warn("node_probe: gateway-round plan lookup failed; running the gateway round anyway",
			"model", model, "error", err)
		return false
	}
	return n == 0
}

// gatewayPlanLookupTimeout 是判据查询的独立时限（毫秒）。
//
// ★ 与 probeModalityFor 的 3s 同一量级：判据是旁路，超时就当「查不到」⇒ 照跑
// 网关轮（fail-open），不让一次慢查询拖住整条探针链。
const gatewayPlanLookupTimeout = 3 * time.Second

// gatewayPlanTenant 是判据用的租户。
//
// ★ 探针的网关轮用**系统 Key**发请求 ⇒ 请求的租户就是系统 Key 的租户
//   （keyInfo.TenantID）⇒ 211 实测探针请求的候选缓存键为
//   `<model>|default|modality:text` ⇒ 租户恒为 default。
//   ⇒ 传 "default" 还有一个附带好处：**与请求命中同一份候选缓存**
//   （键逐字相同）⇒ 判据几乎不额外打数据库。
//   ⇒口径与 admin/routing_plan_order.go 的「空租户回落 default」一致。
const gatewayPlanTenant = "default"
