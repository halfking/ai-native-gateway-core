package bg

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ★ 2026-11-26（gpt-image 探针风暴轮 · 第三处修正）
//
// 题目：探针的第二轮（钉住凭据的网关轮）在什么条件下**可证明**是零信息量的？
//
// ── 事实（211 真机实测，2026-11-26 01:1x）──────────────────────────────
//
//	· 残留的每一条 gpt-image 失败请求（traffic_class='probe'、credential_id 为空、
//	  error_kind='no_candidate'）都来自凭据 99023（freellm 中转商）的
//	  `credential_recovery` 复检：直连轮 429/502 失败 → 网关轮被钉到同一凭据 →
//	  路由层找不到候选 → 503 no_candidate。
//	· 这 10 个 (凭据, 模型) 对在**路由层自己的资格视图**
//	  `v_routable_credential_models` 里读数**全是 is_routable=false**
//	  （7 个 node_probe_failed、3 个 binding_unavailable）；
//	 同一批读数为 true 的 3 个模型对**不在**残留名单里 ⇒ 视图有判别力。
//	· 网关轮把凭据**硬过滤**到被钉的那一把
//	  （executor.go:PinCredentialID —— "Honored as a HARD filter in
//	  dispatchRoute"）⇒ 视图判否 ⇒ 候选数必为 0 ⇒ 网关轮**必然**是
//	  no_candidate，**永远触达不到上游**。
//
// ── 结论 ───────────────────────────────────────────────────────────────
//
//	直连轮已经失败、且该 (凭据, 模型) 对在路由层已不可路由时，钉住的网关轮
//	不可能产生任何上游侧证据 —— 它只会再写一条必然失败的**业务形态**请求记录。
//
//	（与仓库既有口径同源：bg/probe_recovery_authority_test.go 的段头写明
//	 "no_candidates — the gateway's own routing state, not the node's health"
//	 ⇒ 网关轮返回 no_candidates 本来就不承载节点健康信息。）
//
// ── 判据口径 ───────────────────────────────────────────────────────────
//
//	· 只问**路由层自己的资格视图**，不自己抄一份可路由性合取（视图是十二项
//	  AND 合取，抄一份到 Go 侧必然漂移；仓库另有 routeguard 门守它的定义）。
//	· 只在**直连轮失败**时问视图：直连成功时网关轮有独立职责 —— 确认**路由层
//	  看见了已恢复的状态**（此刻视图可能还停留在上一轮的失败态，问它会误杀）。
//	· **只判否、不判未知**：视图里查不到这一对 ⇒ 不据此判否 ⇒ 照跑网关轮。
//	· 失败方向：**查询出错 ⇒ 按「可路由」处理（照跑网关轮）** ⇒ 保持现状行为，
//	  宁可多一条噪声记录，也不因为一次 DB 抖动丢掉网关轮的诊断能力。

// probeGatewayRoundRoutableSQL asks the routing layer's own eligibility view
// whether (credential, model) can currently be routed.
//
// 第一列 = 该对是否可路由（多行时取「任一可路由」）；
// 第二列 = 视图里是否**知道**这一对（不知道 ⇒ 不据此判否）。
const probeGatewayRoundRoutableSQL = `
	SELECT
		COALESCE(BOOL_OR(is_routable), false),
		COUNT(*) > 0
	FROM v_routable_credential_models
	WHERE credential_id = $1
	  AND raw_model_name = $2`

// probeGatewayRoundRoutable reports whether the routing layer can currently
// route (cred, model) — i.e. whether the pinned gateway round could find a
// candidate at all.
//
// ★ 「视图里没有这一对」与「查询失败」都按**可路由**返回（fail-open）：
//
//	前者说明该对压根不在绑定视图里（探针多半会在别处按 missing binding 丢掉，
//	不该由这里替它下结论），后者说明这次判据读数不可信 —— 两种情形都照跑
//	网关轮，保持现状行为。
func probeGatewayRoundRoutable(ctx context.Context, db *pgxpool.Pool, credID int, model string) (bool, error) {
	var routable, known bool
	if err := db.QueryRow(ctx, probeGatewayRoundRoutableSQL, credID, model).Scan(&routable, &known); err != nil {
		return false, err
	}
	if !known {
		return true, nil
	}
	return routable, nil
}

// gatewayRoundRoutableFn is the test seam over the routing layer's own
// eligibility view. Production leaves it nil and the helper runs the real
// SQL; tests stub it to pin the skip behaviour without a live database.
type gatewayRoundRoutableFn func(ctx context.Context, credID int, model string) (bool, error)

// gatewayRoundRoutable is the worker's fail-open wrapper: any lookup trouble
// reports "routable" so the pinned gateway round still runs (today's
// behaviour). Both probe paths call this — one implementation, two call sites.
func (w *NodeProbeWorker) gatewayRoundRoutable(ctx context.Context, credID int, model string) bool {
	// 没有任何查读能力（既无测试桩也无 DB）⇒ 按「可路由」处理（fail-open）。
	// ★ 这一条是回归换来的：2026-11-26 首版把零值 routable 当成「不可路由」，
	// 于是单测里的 worker（无 DB）被判成不可路由 ⇒ 瞬时失败的网关轮被误杀
	// （TestProbeServiceKeepsGatewayRoundOnTransientDirectFailure 红了）。
	if w.gatewayRoutableLookup == nil && w.db == nil {
		return true
	}
	var (
		routable bool
		err      error
	)
	if w.gatewayRoutableLookup != nil {
		routable, err = w.gatewayRoutableLookup(ctx, credID, model)
	} else {
		routable, err = probeGatewayRoundRoutable(ctx, w.db, credID, model)
	}
	if err != nil {
		slog.Warn("node_probe: gateway-round routability lookup failed; running the gateway round anyway",
			"credential_id", credID, "model", model, "error", err)
		return true
	}
	return routable
}

// probeGatewayRoundDecision decides whether the pinned gateway round runs, and
// why it does not when it does not.
//
// ★ **单一判据、两处调用**（统一队列路径 ProbeService.Run 与 legacy runOne
//
//	共用本函数，不是两份抄本）。
//
//	routable = 「该 (凭据, 模型) 对此刻在路由层是否还可能找到候选」。
//	调用方只在直连轮失败时去问它（直连成功 ⇒路由层可能刚看见恢复，问视图
//	会误杀网关轮本来的职责）。
func probeGatewayRoundDecision(direct nodeProbeRoundResult, routable bool) (run bool, skipReason string) {
	if direct.ok {
		// 直连轮成功 ⇒ 网关轮照跑：它的职责是确认**路由层看见了已恢复的状态**。
		return true, ""
	}
	if probeGatewayRoundRedundant(direct) {
		// 直连轮已给出「与 (凭据, 模型, 上游) 绑定」的判决 ⇒ 钉住的网关轮只会
		// 把同一个拒绝原样再要一次（详见 probeGatewayRoundRedundant）。
		return false, "gateway_round_skipped_redundant"
	}
	if !routable {
		// 该对在路由层已不可路由 ⇒ 钉住的网关轮必然 0 候选 ⇒ 永远触达不到上游
		// ⇒ 零信息量，只会多写一条必然失败的业务形态请求记录。
		return false, "gateway_round_skipped_unroutable"
	}
	return true, ""
}

// probeGatewayRoundSkipDetail is the human-readable reason recorded on the
// skipped gateway round (single source for both skip reasons).
func probeGatewayRoundSkipDetail(reason string) string {
	switch reason {
	case "gateway_round_skipped_redundant":
		return "direct round already convicted this (credential, model) pair; the pinned gateway round would repeat the same upstream refusal"
	case "gateway_round_skipped_unroutable":
		return "routing layer currently excludes this (credential, model) pair, so the pinned gateway round can only be a no_candidate; it can never reach the upstream"
	default:
		return "gateway round skipped: " + reason
	}
}
