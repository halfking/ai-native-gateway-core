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
// ── 事实（211 真机实测，2026-11-26 01:1x–01:4x）──────────────────────────
//
//	· 残留的每一条 gpt-image 失败请求（traffic_class='probe'、credential_id 为空、
//	  error_kind='no_candidate'）都来自凭据 99023（freellm 中转商）的
//	  `credential_recovery` 复检：直连轮 429/502 失败 → 网关轮被钉到同一凭据 →
//	  路由层找不到候选 → 503 no_candidate。
//	· 网关自己的诊断日志给出了路由层的**真实判据面**：
//	  `[candidate_diag] db_empty ... offers_total=N blocked_credential_quota=…
//	  blocked_unavailable_reason=…` ⇒ **路由层的候选面是 `model_offers`**
//	  （那张表按 (凭据, 模型) 记可用性与停放理由），**不是**建立在
//	  `credential_model_bindings` 上的 `v_routable_credential_models`。
//	· 两张表**已经漂移**：99023 的 `gpt-image-2.5-sunburst` 在
//	  `credential_model_bindings` 上 available=true，在 `model_offers` 上却是
//	  available=false / `probe_http_429` ⇒ **只问视图会读成「可路由」**
//	  （⇒ 第一版判据因此对实测残留**不咬**，已按事实订正）。
//	· 钉住方 99023 的 10 个 gpt-image 模型对里，**8 个**在 `model_offers` 上是
//	  available=false（停放，理由 probe_http_429 / probe_http_502），而**同一批
//	  模型在别的凭据（26/43/60/63/99025/99031）上的 offer 仍是 available=true**
//	  ⇒ 该判据有判别力，不是「全体停放」。
//	· 网关轮把凭据**硬过滤**到被钉的那一把
//	  （executor.go:PinCredentialID —— "Honored as a HARD filter in
//	  dispatchRoute"）⇒ 钉住方没有可用 offer ⇒ 候选数必为 0 ⇒ 网关轮**必然**是
//	  no_candidate，**永远触达不到上游**。
//
// ── 结论 ───────────────────────────────────────────────────────────────
//
//	直连轮已经失败、且该 (凭据, 模型) 对在路由层已无可用候选时，钉住的网关轮
//	不可能产生任何上游侧证据 —— 它只会再写一条必然失败的**业务形态**请求记录。
//
//	（与仓库既有口径同源：bg/probe_recovery_authority_test.go 的段头写明
//	 "no_candidates — the gateway's own routing state, not the node's health"
//	 ⇒ 网关轮返回 no_candidates 本来就不承载节点健康信息。）
//
// ── 判据口径 ───────────────────────────────────────────────────────────
//
//	· 判据问**路由层真正在用的两张表**，两条**各自独立成立**的跳过理由：
//	  ① offer 可用性（`model_offers`，路由候选面）：钉住凭据对该模型**没有可用
//	     offer** ⇒ 硬过滤后候选必为 0；
//	  ② 凭据/供应商级闸（`v_routable_credential_models.unavailable_reason`）：
//	     排除原因落在凭据级/供应商级前缀（provider_ / credential_ / lifecycle_ /
//	     availability_ / quota_ / recent_probe_unreachable）⇒ 这把凭据此刻整体
//	     不可用 ⇒ 候选必为 0。
//	  ⇒ ② 用**前缀**而不是枚举状态码：视图的排除原因是 CASE 出来的文本，枚举它
//	    必然随视图演进而漂移；前缀匹配只依赖「凭据级理由排在绑定级理由前面」这
//	    条顺序性质。
//	· **不在 Go 侧抄一份可路由性合取**（视图是十二项 AND 合取，抄一份必然漂移；
//	  仓库另有 routeguard 门守视图的定义）。
//	· 只在**直连轮失败**时问：直连成功时网关轮有独立职责 —— 确认**路由层
//	  看见了已恢复的状态**（此刻 offer 可能刚从停放被临时恢复，问它会误杀）。
//	· **只判否、不判未知**：两张表都查不到这一对 ⇒ 不据此判否 ⇒ 照跑网关轮。
//	· 失败方向：**查询出错 ⇒ 按「可路由」处理（照跑网关轮）** ⇒ 保持现状行为，
//	  宁可多一条噪声记录，也不因为一次 DB 抖动丢掉网关轮的诊断能力。
//
// ── 已知未覆盖（登记，不假装已修）★★★ ────────────────────────────────
//
//	钉住方 offer **仍可用**、但路由层仍零候选的子集：211 实测 2 对
//	（openaigpt-image-2.6-flare / -sunburst）。网关诊断对它们的读数是
//	`cause_class=gates_open`（offer 有、状态闸全开）⇒ 第二因在**路由计划/过滤**
//	层（诊断自己给的四个待查项：租户 / 档位 / 模态 / 客户端档匹配），尚未定位。
//	⇒ 这两对本轮**仍会**产生 no_candidate 残留 ⇒ 登记为待查项（含下一次的取证
//	命令），不写成「已归零」。

// probeGatewayRoundRoutableSQL asks the routing layer's own candidate面 whether
// the pinned (credential, model) pair can find a candidate at all.
//
// 第一列 = 「可证明零候选」（true ⇒ 该跳过网关轮）；
// 第二列 = 两张表里**是否知道**这一对（不知道 ⇒ 不据此判否）。
const probeGatewayRoundRoutableSQL = `
	SELECT
		(
			(
				NOT EXISTS (
					SELECT 1 FROM model_offers o
					 WHERE o.credential_id = $1
					   AND o.raw_model_name = $2
					   AND o.available IS TRUE
				)
				AND EXISTS (
					SELECT 1 FROM model_offers o2
					 WHERE o2.credential_id = $1
					   AND o2.raw_model_name = $2
				)
			)
			OR COALESCE((
				SELECT bool_or(v.unavailable_reason ~ '^(provider_|credential_|lifecycle_|availability_|quota_|recent_probe_unreachable)')
				  FROM v_routable_credential_models v
				 WHERE v.credential_id = $1
				   AND v.raw_model_name = $2
			), false)
		),
		(
			EXISTS (
				SELECT 1 FROM model_offers o3
				 WHERE o3.credential_id = $1
				   AND o3.raw_model_name = $2
			)
			OR EXISTS (
				SELECT 1 FROM v_routable_credential_models v2
				 WHERE v2.credential_id = $1
				   AND v2.raw_model_name = $2
			)
		)`

// probeGatewayRoundRoutable reports whether the pinned gateway round can
// provably not find any candidate (⇒ it can never reach the upstream).
//
// ★ 「两张表都查不到这一对」与「查询失败」都按**可路由**返回（fail-open）：
//
//	前者说明该对压根不在路由层的事实源里（不该由这里替它下结论），后者说明
//	这次判据读数不可信 —— 两种情形都照跑网关轮，保持现状行为。
func probeGatewayRoundRoutable(ctx context.Context, db *pgxpool.Pool, credID int, model string) (bool, error) {
	var noCandidate, known bool
	if err := db.QueryRow(ctx, probeGatewayRoundRoutableSQL, credID, model).Scan(&noCandidate, &known); err != nil {
		return false, err
	}
	if !known {
		return false, nil
	}
	return noCandidate, nil
}

// gatewayRoundRoutableFn is the test seam over the routing layer's own
// candidate tables. Production leaves it nil and the helper runs the real
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
		noCandidate bool
		err         error
	)
	if w.gatewayRoutableLookup != nil {
		noCandidate, err = w.gatewayRoutableLookup(ctx, credID, model)
	} else {
		noCandidate, err = probeGatewayRoundRoutable(ctx, w.db, credID, model)
	}
	if err != nil {
		slog.Warn("node_probe: gateway-round routability lookup failed; running the gateway round anyway",
			"credential_id", credID, "model", model, "error", err)
		return true
	}
	return !noCandidate
}

// probeGatewayRoundDecision decides whether the pinned gateway round runs, and
// why it does not when it does not.
//
// ★ **单一判据、两处调用**（统一队列路径 ProbeService.Run 与 legacy runOne
//
//	共用本函数，不是两份抄本）。
//
//	routable  = 「该 (凭据, 模型) 对此刻在路由层是否还可能找到候选」（凭据级）。
//	planEmpty = 「路由层对这个**模型**是否一个候选都没有」（模型级，
//	            判据见 probe_gateway_plan.go —— 问路由层自己的候选解析器）。
//	调用方只在直连轮失败时去问它们（直连成功 ⇒路由层可能刚看见恢复，问判据
//	会误杀网关轮本来的职责）。
func probeGatewayRoundDecision(direct nodeProbeRoundResult, routable bool, planEmpty bool) (run bool, skipReason string) {
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
		// 该对在路由层已找不到候选 ⇒ 钉住的网关轮必然 0 候选 ⇒ 永远触达不到上游
		// ⇒ 零信息量，只会多写一条必然失败的业务形态请求记录。
		return false, "gateway_round_skipped_unroutable"
	}
	if planEmpty {
		// 路由层对这个模型**整体**零候选（plan_count=0）⇒ 钉住哪一把凭据都一样
		// 是 503 no_candidate ⇒ 零信息量。
		return false, gatewayRoundSkippedNoPlan
	}
	return true, ""
}

// probeGatewayRoundSkipDetail is the human-readable reason recorded on the
// skipped gateway round (single source for all skip reasons).
func probeGatewayRoundSkipDetail(reason string) string {
	switch reason {
	case "gateway_round_skipped_redundant":
		return "direct round already convicted this (credential, model) pair; the pinned gateway round would repeat the same upstream refusal"
	case "gateway_round_skipped_unroutable":
		return "routing layer currently has no available candidate for this (credential, model) pair, so the pinned gateway round can only be a no_candidate; it can never reach the upstream"
	case gatewayRoundSkippedNoPlan:
		return "routing layer currently has no candidate at all for this model (plan is empty), so the pinned gateway round can only be a no_candidate; it can never reach the upstream"
	default:
		return "gateway round skipped: " + reason
	}
}
