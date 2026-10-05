package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthCheckDef struct {
	CheckID  string
	Severity string
	Query    string
	// Optional 标记这条检查所依赖的对象**可能还不存在**（新迁移的表在应用前
	// 就没有），此时 42P01 undefined_table 记一条 INFO 后跳过，而不是中止整轮。
	//
	// 为什么需要它：RunChecks 原本对任何查询错误一律 return，**一轮里任何一条
	// 检查失败，后面所有检查都不会跑**。给一张刚加的表挂检查而不做这个标记，
	// 后果是**每一个还没应用该迁移的环境，健康面整体报错**——而那正是最需要
	// 健康面能用的环境。
	//
	// 为什么不能笼统地吞掉 42P01：provider_models 这类核心表消失时，静默跳过
	// 等于把「schema 崩了」说成「一切正常」。所以只对本文件里显式声明
	// Optional 的那几条生效，其余保持原样（中止）。
	Optional bool
}

func AllHealthChecks() []HealthCheckDef {
	return []HealthCheckDef{
		// canonical_cleared_at (migration 693) guards every auto re-link:
		// a row the operator explicitly unbound must not be flagged as
		// critical here, or the check list keeps offering a one-click fix
		// that undoes the admin decision — and autoFixCanonicalID below
		// would silently re-link it on its next pass.
		{CheckID: "canonical_id_null", Severity: "critical", Query: `SELECT pm.id, pm.raw_model_name, mc.id FROM provider_models pm JOIN models_canonical mc ON mc.canonical_name = pm.raw_model_name WHERE pm.canonical_id IS NULL AND pm.canonical_cleared_at IS NULL ORDER BY pm.id LIMIT 200`},
		{CheckID: "billing_mismatch", Severity: "warning", Query: `SELECT cmb.id, c.id || ':' || pm.raw_model_name, c.plan_type, cmb.billing_mode FROM credential_model_bindings cmb JOIN credentials c ON c.id = cmb.credential_id JOIN provider_models pm ON pm.id = cmb.provider_model_id WHERE c.plan_type IN ('token_plan','code_plan','agent_plan') AND cmb.billing_mode NOT IN ('token_plan','code_plan','agent_plan') ORDER BY cmb.id LIMIT 200`},
		// probe_missing (R39 fix): used to read the legacy model_probe_state
		// directly. Under the new probe mode (default since 86e09daa7) that
		// table is frozen, so every newly added binding showed up as a
		// permanent false-positive warning. v_node_probe_state_compat is the
		// single source of truth projection and is populated in BOTH modes
		// (legacy runOne/queue paths mirror into node_probe_state).
		{CheckID: "probe_missing", Severity: "warning", Query: `SELECT cmb.id, c.id || ':' || pm.raw_model_name, pm.raw_model_name, c.id FROM credential_model_bindings cmb JOIN provider_models pm ON pm.id = cmb.provider_model_id JOIN credentials c ON c.id = cmb.credential_id WHERE cmb.available = TRUE AND c.status = 'active' AND c.lifecycle_status = 'active' AND NOT EXISTS (SELECT 1 FROM v_node_probe_state_compat nps WHERE nps.credential_id = cmb.credential_id AND nps.raw_model_name = pm.raw_model_name) ORDER BY cmb.id LIMIT 200`},
		{CheckID: "family_unknown", Severity: "warning", Query: `SELECT id, canonical_name, canonical_name, canonical_name FROM models_canonical WHERE (family = 'unknown' OR family IS NULL) AND canonical_name ~* '^(claude|gpt|o[1-4]|llama|gemini|gemma|mistral|mixtral|ministral|glm|kimi|moonshot|step|stepfun|doubao|seed|qwen|deepseek|minimax|mimo|baichuan|yi|spark|xinghuo|pangu|ernie|wenxin|hunyuan|abab|falcon|nemotron|phi|sonar|grok|command|embed|rerank|bloom|pythia)' ORDER BY id LIMIT 200`},
		{CheckID: "circuit_open", Severity: "warning", Query: `SELECT cmb.id, c.id || ':' || pm.raw_model_name, c.circuit_state, c.availability_state FROM credential_model_bindings cmb JOIN provider_models pm ON pm.id = cmb.provider_model_id JOIN credentials c ON c.id = cmb.credential_id WHERE cmb.available = TRUE AND c.circuit_state NOT IN ('closed', 'disabled') ORDER BY c.circuit_state, cmb.id LIMIT 50`},
		// circuit_state / availability_state are nullable on credentials;
		// the prior version scanned them into plain *string which made pgx
		// abort the whole batch on the first NULL row. COALESCE to the
		// same 'unknown' sentinel already used for display_name so the Go
		// side stays text-only and the error detail stays meaningful.
		{CheckID: "credential_active_not_routable", Severity: "warning", Query: `SELECT c.id, c.label || ':' || COALESCE(p.display_name, 'unknown'), COALESCE(c.availability_state, 'unknown'), COALESCE(c.circuit_state, 'unknown') FROM credentials c JOIN providers p ON p.id = c.provider_id WHERE c.status = 'active' AND c.lifecycle_status = 'active' AND EXISTS (SELECT 1 FROM credential_model_bindings cmb WHERE cmb.credential_id = c.id) AND NOT EXISTS (SELECT 1 FROM v_routable_credential_models v WHERE v.credential_id = c.id AND v.is_routable = TRUE) ORDER BY c.id LIMIT 50`},
		// baseline_observation_stale（迁移 832）：漂移对账 worker 在抓不到外部
		// 机读源时**不写任何判词**——这是对的（没有观察就没有结论）。但如果不
		// 留痕，「每 12h 抓一次、每次都失败、每次都不写东西」可以连续几周不被
		// 任何人发现，而报表照常出数（用的是几个月前的基准价）。
		// 这条检查就是那个「让人知道」的另一半。
		//
		// 触发条件三选一：正在连续失败 / 从没成功过 / 上次成功早于两个对账周期
		// （ReconcileInterval = 12h ⇒ 24h）。两个周期而不是一个，是为了容忍一次
		// 抖动——2026-10-04 实测过一次性 EOF，同一时刻 curl 与三种 UA 的 Go
		// 客户端全部 200。
		//
		// Optional：832 未应用的环境上这张表不存在，此时跳过而不是让整轮健康
		// 检查报错。
		//
		// 刻意的**不**覆盖：表为空（worker 从没跑过）。那既可能是 kill switch
		// 关掉了它（合理配置），也可能是 worker 根本没被调度（该查），SQL 侧
		// 分不开这两种，所以不报——宁可少报一条，也不把「故意关掉」说成故障。
		// modality_verification_stale：目标第一句「自动对未曾标注核实过的模型定时
		// 核实，**这个需要加入到自检任务中**」在健康面的那一条。
		//
		// 为什么必须有（2026-10-04 盘点出来的缺口）：核实 worker 早已接线
		// （cmd/gateway/main.go:4589 `go modalityVerify.Run(...)`，经
		// shouldStartNewProbeWorkers opt-in），827 也已经把判断所需的每个数算好了
		// —— 但**健康面 8 条检查里没有一条覆盖多模态核实**，而 827 的两个视图在
		// 生产侧无人读。⇒ 「定时核实」这件事在运维可见的界面上是隐形的。
		//
		// 报什么：`excluded_by_strict_gate` 的组合 = 「这个模型开了严格档就会挡住
		// 路由」。它正是 `LLM_GATEWAY_MODALITY_ROUTING_STRICT` 的上线前置条件
		// —— 此前只能靠人手动跑
		// `SELECT * FROM v_model_modality_verification_rollup;` 去数。
		//
		// 粒度：**按 (模型, 模态) 逐行报**，不只报总数。理由是总数不可行动：
		// 「有 57 个组合挡着」回答不了「先修哪个」。逐行给出模型名 + 模态 + 已有
		// 证据数 + 门禁出处，运维可以直接对着探。
		//
		// 刻意**不**报 confirmed 的行：已确认的模型不挡路。
		// 刻意**不**报 `excluded_by_default_gate`：那是默认档的既有口径，不是
		// 「坏了」，报它会把真正的阻塞项淹没（同族：既有的 baseline_observation_stale
		// 刻意不把「表为空」说成故障）。
		//
		// 数据源用 827 的 progress 视图（一个**关系**）而不是直接读
		// model_modality_verification 表：827 未应用时缺的是关系 ⇒ 报 42P01，
		// 正好落在 HealthCheckDef.Optional 已支持的那条路上；若直接读 825 的列，
		// 825 未应用时报 42703 undefined_column，而 Optional 只认 42P01 ⇒
		// 那种环境下整轮健康检查会**中止**。
		{CheckID: "modality_verification_stale", Severity: "warning", Optional: true, Query: `SELECT p.canonical_id, p.canonical_name || ' [' || p.modality || ']', 'no accepted read evidence — strict routing gate would exclude this pair (evidence_rows=' || p.evidence_rows || ', verdict=' || p.verdict || ', blocked_by=' || CASE WHEN p.excluded_by_strict_gate THEN 'strict' ELSE 'default' END || ')', '' FROM public.v_model_modality_verification_progress p WHERE p.excluded_by_strict_gate ORDER BY p.evidence_rows ASC, p.canonical_name LIMIT 50`},
		{CheckID: "baseline_observation_stale", Severity: "warning", Optional: true, Query: `SELECT source_url, source_url, COALESCE(consecutive_failures, 0) || ' consecutive failure(s)', COALESCE('last success: ' || last_success_at::text, 'never succeeded') || COALESCE(' | last error: ' || left(last_error, 200), '') FROM public.model_baseline_price_observation_health WHERE consecutive_failures > 0 OR last_success_at IS NULL OR last_success_at < now() - interval '24 hours' ORDER BY last_success_at NULLS FIRST LIMIT 20`},
		// pricing_plan_stale：**仓里本来就有**一套定价 SSOT，而 15 条检查里
		// 没有一条问过它还新不新。
		//
		// 实测（2026-10-06，本机 5432 只读）：
		//
		//	pricing_plans 284 行（CNY 152 / USD 132），source 全是 'scraped'，
		//	created_at **全部** = 2026-06-12 ⇒ 最旧 **115 天**，
		//	**284/284 超 30 天、284/284 超 90 天**。
		//	覆盖 models_canonical 里的 **39 / 960（4.1%）** 个模型；
		//	另有 **116 / 284（41%）行的 model_canonical_id IS NULL** ——
		//	它们连目录模型都指不到，也就**不可能**参与按模型的成本核算。
		//
		// ★ 为什么这条不能省：现有那几条价格检查盯的是 **models_canonical 的
		//   baseline_* 九列**（当前 0 行）与 model_offers 的供应商价，
		//   **没有一条读 pricing_plans 的时效**。而 `provider/client.go:1816`
		//   的 CalcCost 会在计划价缺失时回落到 pricing_plans ⇒ 也就是说
		//   **成本计算正在用一份 115 天前的价**，却没有任何告警说它旧了。
		//
		// 报三件事，各自指向不同的处置：
		//   1. 全表最旧的 scraped_at 年龄 ⇒ 整份 SSOT 该重抓；
		//   2. 无 canonical 指针的行数 ⇒ 这些价无法按模型审计；
		//   3. 表为空 ⇒ **刻意不报**（空表是「还没定价」，不是故障）。
		//
		// ⚠ 第 3 条与 baseline_price_missing 的 '(no priced bindings yet)' 同理：
		//   把「还没开始」说成「坏了」，会让人去修一个不存在的问题。
		//
		// ★ **「空表不报」到底靠什么保证（变异实测，别把功劳记错）**：
		//   下面的 `total > 0` 守卫与 `COALESCE(max(created_at), now())`
		//   **各自单独就足够**，两个**同时**去掉也仍然不报 ——
		//   因为空表上 `max()` 返回 NULL，而 `NULL < now() - interval '30 days'`
		//   求值为 **NULL 而不是 true**（实测 `(... ) IS NOT TRUE` = t），
		//   WHERE 于是永不通过。
		//   ⇒ 这是 **SQL 三值逻辑的结构性保证**，不是这两行代码的功劳。
		//   两个守卫都留着：它们是**可读性**（一眼看出意图），**不是**承重。
		//   变异 M10（只去守卫）/ M12（只去 COALESCE）/ M13（两个都去）**全绿**，
		//   就是这条的证据。
		//
		// 用 created_at 而不是 scraped_at：pricing_plans 没有 scraped_at 列，
		// 出处记在 scraped_url 上（实测 0 行为空）。created_at 记的是**入库**
		// 时间，语义上比 mtime 可靠，但仍然不是「从原厂页核实的时间」——
		// 所以 detail 里写明这一点，不把它说成核实时间。
		{CheckID: "pricing_plan_stale", Severity: "warning", Optional: true, Query: `
WITH stat AS (
    SELECT count(*)::int                                            AS total,
           COALESCE(max(created_at), now())                          AS newest,
           count(*) FILTER (WHERE model_canonical_id IS NULL)::int   AS orphan,
           -- 孤儿行只来自少数几个**凭据**（实测 116 行来自 7 个），而这几个
           -- 凭据各自服务大量已映射模型 ⇒ 「7 个凭据」才是可行动的工作量，
           -- 「116 行」不是。只读 pricing_plans 一张表，不引入第二个依赖。
           count(DISTINCT credential_id) FILTER (
               WHERE model_canonical_id IS NULL)::int                AS orphan_cred
      FROM public.pricing_plans
)
SELECT
    -1,
    '(bulk) pricing_plans is ' || (current_date - (SELECT newest::date FROM stat)) ||
        ' day(s) past its last load',
    CASE WHEN (SELECT total FROM stat) = 0
         THEN 'pricing_plans is empty — that is "nothing has been priced yet", NOT a fault; ' ||
              'the check stays quiet on an empty table on purpose'
         ELSE (SELECT total FROM stat) || ' pricing plan row(s); ' ||
              (SELECT orphan FROM stat) || ' of them have model_canonical_id IS NULL, across ' ||
              (SELECT orphan_cred FROM stat) || ' credential(s). CalcCost selects a plan with ' ||
              'pp.model_canonical_id = mo._mc_id, and NULL never matches, so every binding on ' ||
              'those credentials falls back to another price source — this is the actionable ' ||
              'size (a credential list), not the row count. ' ||
              'NOTE: created_at records the LOAD time, not the time the price was verified ' ||
              'against the vendor page — treat it as an upper bound on freshness, not proof of it'
    END,
    ''
  FROM stat
 WHERE (SELECT total FROM stat) > 0
   AND (SELECT newest FROM stat) < now() - interval '30 days'
 ORDER BY 1
 LIMIT 50`},
		// baseline_price_missing：与 baseline_observation_stale 互补的一条。
		//
		// 两条盯的是**成本核算的两个前提**：
		//   - baseline_observation_stale 盯「观察侧还在不在」（有基准价、但抓不到
		//     外部源 ⇒ 偏差停更，看起来正常）。
		//   - 这条盯「基准侧有没有价」（能抓到源、但一条基准价都没有 ⇒ 偏差
		//     **根本无从算起**）。
		//
		// 为什么必须有：bg/pricing_baseline_sync.go 已在空 catalog 时打
		// slog.Warn，但**进程日志不是健康面** —— 没人盯日志时，「一条基准价都
		// 没有，供应商实际成本无法与原厂标准对照」这件事是隐形的，而它正是
		// 「准确控制模型实际成本」这条目标当前的真实状态（SSOT 的 models 刻意
		// 为空，直到原厂页面被逐个核实）。
		//
		// 数据源刻意用 v_supplier_price_vs_baseline（826 建）而不是直接读
		// models_canonical 的基线价列：后者在 826 未应用时报 **42703
		// undefined_column**，而 HealthCheckDef.Optional 只认 **42P01**
		// undefined_table ⇒ 那种环境下整轮健康检查会**中止**，恰是 Optional
		// 要防的后果。视图是一个关系，缺它时报 42P01，落在已支持的那条路上。
		//
		// 只报**正在按供应商价计费、且该模型没有基准价**的行：已经没人用的历史
		// 模型没有基准价，不构成成本风险。
		//
		// ★ 粒度：按**模型去重**后决定报法（2026-10-04 真库实测后改）
		//
		// 第一版是逐绑定 LIMIT 50。真库量出来的问题：那台库 186 个有价绑定 /
		// 163 个模型，因为 SSOT 尚空，这条检查会**每一轮都报满 50 行**，而那
		// 50 行说的是同一件事「这个模型没有基准价」。⇒ 它自己变成了噪声：50 行
		// 占满告警位，真正需要先修的那几个反而被埋掉 —— 与本文件里
		// baseline_observation_stale「宁可少报一条，也不把『故意关掉』说成故障」
		// 是同一条纪律。
		//
		// 改法：**按模型去重**，且缺失面大时只报一行汇总。
		//   · 缺失模型 ≤ 20 ⇒ 逐个列名，运维能直接对着填。
		//   · 缺失模型 > 20 ⇒ 报一行：缺多少个、占全部有价绑定模型的几成，
		//     并指明这是「SSOT 尚未填充」的形态而不是个例。
		// 两种形态都**可行动**：前者给清单，后者给工作量。
		// 阈值 20 是可调的运维口味、不是数据推出来的；它小到「一次填 20 个价目」
		// 在一个下午内可完成，大到不会让人误以为「只有这几个」。
		//
		// 刻意**不**报币种不一致的那些行：币种不可比是**另一个**问题（需要汇率
		// 或换算口径决策），混进来会让这条检查同时承担两件事。
		{CheckID: "baseline_price_missing", Severity: "warning", Optional: true, Query: `
WITH missing AS (
    -- 按**模型**去重（一个模型可能有多条供应商绑定）。⚠ 826 的偏差视图**没有**
    -- 暴露 canonical_id（只有 credential_id / canonical_name 等 18 列，
    -- 2026-10-04 实测），所以这里按 canonical_name 去重，entity_id 走
    -- textHash —— 与 baseline_observation_stale 同一套，保证同一模型跨轮
    -- 落同一行。
    SELECT DISTINCT canonical_name
      FROM public.v_supplier_price_vs_baseline
     WHERE NOT has_baseline AND supplier_in_per_1m IS NOT NULL
), priced AS (
    SELECT count(DISTINCT canonical_name) AS n
      FROM public.v_supplier_price_vs_baseline
     WHERE supplier_in_per_1m IS NOT NULL
)
SELECT
    0,  -- 占位：Go 侧按 entity_name 重算 entity_id（见该 case 的注释）
    CASE WHEN (SELECT n FROM priced) = 0 THEN '(no priced bindings yet)' ELSE missing.canonical_name END,
    'no vendor baseline price for this model — ' || (SELECT n FROM priced) ||
    ' priced model(s) tracked, this one has no original-vendor reference to compare against',
    ''
  FROM missing
 WHERE (SELECT count(*) FROM missing) <= 20
UNION ALL
SELECT
    -- 汇总行用 -1 而不是 0：0 是「扫描分支缺失、整行全零」的特征值
    --（真库实测过，见 health_check_scan_guard_test.go），让汇总行也用它
    -- 会让那条判据的「entity_id==0 ⇒ 缺分支」断言在正常运行时误报。
    -1,
    '(bulk) ' || (SELECT count(*) FROM missing) || ' model(s) have no baseline price',
    'every priced binding lacks an original-vendor reference, so supplier-vs-baseline deviation ' ||
    'cannot be computed for them. This is the "baseline catalog not yet populated" shape, not a ' ||
    'per-model slip: fill bg/data/model_baseline_prices.json (source_url + fetched_at required). ' ||
    'Tracked priced models: ' || (SELECT n FROM priced),
    ''
 WHERE (SELECT count(*) FROM missing) > 20
 ORDER BY 1, 2
 LIMIT 50`},
		// supplier_price_drift：把「偏差被记录」变成「偏差被告警」——成本控制
		// 闭环的最后一环。
		//
		// 为什么需要（2026-10-05 盘点出来的缺口）：整条价格链路的**度量**侧已经
		// 齐了 —— 基准价落进 models_canonical（826 九列）、倍率算进
		// v_supplier_price_vs_baseline、判词记进 model_baseline_price_reconciliation。
		// 但那两条数据**没有任何生产代码读取**：视图只被 baseline_price_missing 那个
		// 健康检查用，台账**只写不读**。仓里所有 drift 告警都只关于物化视图与
		// columnar，与价格无关。
		//
		// ⇒ 供应商把价悄悄调高 3 倍，台账里静静躺着一条 verdict='drift'，
		// **没有人会被告知**。而「准确控制模型的实际成本」要求的是闭环，不是记账。
		//
		// 阈值 1.5（比原厂贵 50%）与对账侧的 2% **刻意不同**：
		//   · 2% 是**记账**口径，超过就留痕，天然会很吵（任何舍入都可能触发）。
		//   · 1.5 是**告警**口径，意思是「贵到值得人去查一次」。把记账口径直接拿
		//     来告警，结果是健康面长期泛黄，而那比不告警更糟 —— 真信号会被埋掉。
		// 两者都是**可调的运维口味**，不是数据推出来的；这里选 1.5 是因为
		// 「贵五成」通常意味着换供应商/换档位/谈价，而不是一次调价失误。
		//
		// 币种不可比的行**不报**：倍率是拿两种货币直接相除算出来的，它没有意义。
		// 那些行由对账台账的 not_comparable 判词负责（需要的是汇率决策，不是告警）。
		//
		// 逐**绑定**报（不是逐模型）：一个凭据挂多个模型时，模型名去重会让告警行
		// 互相覆盖（与 modality_verification_stale 那次「三行撞成一行」同族）。
		// 首列给的是 `credential_id|raw_model_name` 文本键 —— 视图不暴露绑定 id，
		// 而这两列合起来正是绑定的身份（cmb 上有 UNIQUE(credential_id, provider_model_id)）。
		{CheckID: "supplier_price_drift", Severity: "warning", Optional: true, Query: `
SELECT v.credential_id::text || '|' || v.raw_model_name,
       -- ⚠ 826 视图里那个 provider_name 列其实是 p.id::text（id，不是名字）。
       -- 直接用它，运营看到的是「1:raw-pricey」这种没法据以行动的数字。
       -- 这里补一次 LEFT JOIN 取真正的 code，取不到就退回 id。
       -- （注释里不能用反引号：这段在 Go raw string 里。）
       COALESCE(p.code, v.provider_name, '') || ':' || v.raw_model_name,
       CASE
         WHEN (COALESCE(v.baseline_in_per_1m, 0) = 0 AND COALESCE(v.supplier_in_per_1m, 0) > 0)
           OR (COALESCE(v.baseline_out_per_1m, 0) = 0 AND COALESCE(v.supplier_out_per_1m, 0) > 0)
         THEN 'the original vendor lists this model as FREE (baseline 0) but the supplier charges ' ||
              COALESCE(v.supplier_in_per_1m::text, 'null') || '/' ||
              COALESCE(v.supplier_out_per_1m::text, 'null') || ' ' ||
              COALESCE(v.supplier_currency, 'USD') ||
              ' — a ratio is undefined against a 0 baseline, so this is a free-to-paid change, ' ||
              'not a rounding disagreement'
         ELSE
       'charging ' || COALESCE(v.input_price_ratio::text, '?') || 'x (in) / ' ||
       COALESCE(v.output_price_ratio::text, '?') || 'x (out) the original-vendor baseline — ' ||
       'supplier ' || COALESCE(v.supplier_in_per_1m::text, 'null') || '/' ||
       COALESCE(v.supplier_out_per_1m::text, 'null') || ' ' ||
       COALESCE(v.supplier_currency, 'USD') || ' vs baseline ' ||
       COALESCE(v.baseline_in_per_1m::text, 'null') || '/' ||
       COALESCE(v.baseline_out_per_1m::text, 'null') || ' ' ||
       COALESCE(v.baseline_currency, 'USD') || ' (alert at 1.5x; ledger records at 2%%)'
       END,
       ''
  FROM public.v_supplier_price_vs_baseline v
  LEFT JOIN public.providers p ON p.id::text = v.provider_name
 WHERE v.has_baseline
   AND v.currency_comparable
   -- ★ 基准侧币种**为空/空白**的行必须排除（2026-10-05 实测后加）。
   --
   -- 826 视图的 currency_comparable 是
   --   COALESCE(cmb.currency,'USD') IS NOT DISTINCT FROM COALESCE(mc.baseline_price_currency,'USD')
   -- ⇒ 基准币种为空时它**被当成 USD**。若供应商恰好也是 USD，两侧就"可比"了，
   -- 视图照着两个数算出 3.0x，而本检查会把这个**伪造比较的结果**当成真偏差
   -- 报出去 —— detail 里还带着 ratio，看起来比任何真告警都可信。
   --
   -- 这比"算不出来"更坏：算不出来至少是 NULL（像"没数据"），伪造出来的是
   -- 一个**精确的错数字**。真库实测（before）：raw-nofx 那一行被报了 1 条。
   --
   -- 被排除掉的行**不是**被藏起来：它们由 supplier_price_currency_mismatch
   -- 报出来，detail 会说清「未知币种被视图当成 USD」。静默与噪声都不是答案。
   AND v.baseline_currency IS NOT NULL AND btrim(v.baseline_currency) <> ''
   AND ( v.input_price_ratio > 1.5
      OR v.output_price_ratio > 1.5
      -- ★ 「原厂免费而供应商收费」：倍率对 0 分母无定义（826 视图给 NULL），
      -- 所以它不会落进上面那两条 ratio 比较 —— 而它是成本上最该先看的一类。
      -- has_baseline 判的是 IS NOT NULL，0 也算「有基准价」，于是这一类此前
      -- **两条检查都看不见**（supplier_price_drift 与 baseline_price_missing）。
      OR (COALESCE(v.baseline_in_per_1m, 0) = 0 AND COALESCE(v.supplier_in_per_1m, 0) > 0)
      OR (COALESCE(v.baseline_out_per_1m, 0) = 0 AND COALESCE(v.supplier_out_per_1m, 0) > 0) )
 -- 免费→收费排在最前：它的倍率是 NULL（GREATEST 把它甩到末尾），而它的可行动性
 -- 最高。LIMIT 50 下让它沉底等于把它藏起来。
 ORDER BY ((COALESCE(v.baseline_in_per_1m, 0) = 0 AND COALESCE(v.supplier_in_per_1m, 0) > 0)
        OR (COALESCE(v.baseline_out_per_1m, 0) = 0 AND COALESCE(v.supplier_out_per_1m, 0) > 0)) DESC,
          GREATEST(v.input_price_ratio, v.output_price_ratio) DESC NULLS LAST
 LIMIT 50`},

		// supplier_price_currency_mismatch：把「这条绑定的实际成本**没有被监控**」
		// 变成告警。这是「记录了但没人知道」那一族的第 3 例（前两例是免费→收费、
		// 以及台账只写不读）。
		//
		// 为什么需要（2026-10-05 盘点）：
		//
		// `supplier_price_drift` 的 WHERE 里有 `v.currency_comparable`，所以币种
		// 不同的行**刻意不报**。作者的注释把它委托给了台账：「那些行由对账台账的
		// not_comparable 判词负责（需要的是汇率决策，不是告警）」。
		//
		// ★ 那个委托**没有接收方** —— 台账只写不读。⇒ 供应商按 CNY 计价、原厂
		// 基准是 USD 时，这条绑定从此刻起**不受任何监控**，而它在两张报表里都
		// 看不见：偏差视图给 currency_comparable=false，两条检查都不提它。
		// 运营的仪表盘上这个模型「一切正常」，实际是在盲飞。
		//
		// 第二个分支（基准币种为空）捕的是 826 视图自己的行为：它把
		// `COALESCE(baseline_price_currency,'USD')` 拿来比币种 ⇒ **未知被当成
		// USD**。这类行更坏：视图会照着两个数算出一个**看起来正常的倍率**，
		// 而 supplier_price_drift 已经（2026-10-05 起）不再报它 —— 如果这里也
		// 不报，它就彻底静默了。检查侧不能假定写入侧永远是干净的。
		//
		// 只报**正在按供应商价计费**且**有基准价**的行：没被用的历史模型、还没填
		// 基准价的模型，都不构成当下成本风险（后者由 baseline_price_missing 负责，
		// 两者不重叠：那条判 `NOT has_baseline`，这条要求 `has_baseline`）。
		// ★ 2026-10-06 真库读数（只读量，写在这里是为了下一个人不必重跑）：
		//
		//   - 查的视图是 826 的 `v_supplier_price_vs_baseline`（18 列，含 baseline*），
		//     **不是** `v_routable_credential_models`（13 列，不含任何 baseline*/currency*
		//     列）。只看后者的列清单会以为这条 SQL 要报 42703 —— 不会，两者是不同视图。
		//   - 逐层漏斗：视图 1965 行 → 有供应商价 186 行 → `has_baseline` **0 行**
		//     → 应当报出 **0 行**。⇒ 今天它静默是**构造上的**，不是漏了。
		//     `has_baseline` = `mc.baseline_input_price_per_1m IS NOT NULL`，而
		//     `models_canonical` 960 个模型该列与 `baseline_price_currency` **全为 NULL**
		//     （SSOT `bg/data/model_baseline_prices.json` 的 models 刻意为空）。
		//     与 modality_gate_readiness_floor 同族：判据在数据补齐前**够不着**。
		//   - 覆盖有没有洞：去掉 has_baseline 守卫会多出 **186 绑定 / 14 凭据 / 163 模型**，
		//     而这 163 个模型与 baseline_price_missing 报的「163 个模型无基准价」
		//     **是同一集合** ⇒ 今天无盲区，只是管辖权在邻居那条检查手上。
		//   - 交接时机：基准价一填，若供应商币种是 CNY 而基准币种是 USD，这 7 条
		//     （6 token_plan + 1 monthly）**立刻**由本检查接住并报出。
		//
		// 逐**绑定**报（不是逐模型），与 supplier_price_drift 同一理由：一个凭据
		// 挂多个模型时按模型去重会让告警行互相覆盖。
		{CheckID: "supplier_price_currency_mismatch", Severity: "warning", Optional: true, Query: `
SELECT v.credential_id::text || '|' || v.raw_model_name,
       COALESCE(p.code, v.provider_name, '') || ':' || v.raw_model_name,
       CASE
         WHEN v.baseline_currency IS NULL OR btrim(v.baseline_currency) = ''
         THEN 'the original-vendor baseline has a price (' ||
              COALESCE(v.baseline_in_per_1m::text, '?') || '/' || COALESCE(v.baseline_out_per_1m::text, '?') ||
              ') but no currency, while the supplier charges ' ||
              COALESCE(v.supplier_in_per_1m::text, 'null') || '/' || COALESCE(v.supplier_out_per_1m::text, 'null') ||
              ' ' || COALESCE(v.supplier_currency, '?') ||
              ' — the deviation view treats an unstated baseline currency as USD, so any ratio it ' ||
              'prints for this binding compares two currencies nobody verified. This binding is ' ||
              'not being monitored. Set baseline_price_currency from the vendor pricing page.'
         ELSE 'supplier prices in ' || COALESCE(v.supplier_currency, '?') ||
              ' while the original-vendor baseline is in ' || v.baseline_currency ||
              ' (baseline ' || COALESCE(v.baseline_in_per_1m::text, '?') || '/' || COALESCE(v.baseline_out_per_1m::text, '?') ||
              ' vs supplier ' || COALESCE(v.supplier_in_per_1m::text, 'null') || '/' || COALESCE(v.supplier_out_per_1m::text, 'null') ||
              ') — a ratio between two currencies is not a deviation, so supplier_price_drift ' ||
              'deliberately stays quiet here and this binding has no cost monitoring at all. ' ||
              'Record an FX rate or correct one of the two currency labels.'
       END,
       ''
  FROM public.v_supplier_price_vs_baseline v
  LEFT JOIN public.providers p ON p.id::text = v.provider_name
 WHERE v.has_baseline
   AND v.supplier_in_per_1m IS NOT NULL
   AND ( NOT v.currency_comparable
      OR v.baseline_currency IS NULL
      OR btrim(v.baseline_currency) = '' )
 -- 未知币种排最前：它不是"需要汇率决策"，而是"连是哪种货币都没人核实"，
 -- 而 supplier_price_drift 已经把这一类排除掉了。LIMIT 50 下沉底等于藏起来。
 ORDER BY (v.baseline_currency IS NULL OR btrim(v.baseline_currency) = '') DESC,
          v.credential_id, v.raw_model_name
 LIMIT 50`},
		// modality_gate_readiness_floor：把「降到 0 才能开闸」这条**构造上达不到**
		// 的判据，换成可行动的数字。
		//
		// 为什么需要（2026-10-05 真环境实测出来的，不是推理）：
		//
		// 827 视图自己的注释写着「models_blocked_by_strict = 现在打开
		// LLM_GATEWAY_MODALITY_ROUTING_STRICT 会挡住的模型数」，而**分母是
		// models_canonical 全表**（per_pair 枚举每个 canonical 模型的三个非文本
		// 模态）。真环境实测：
		//
		//	canonical 模型总数               = 960
		//	models_blocked_by_strict（今天） = 960
		//
		// 而核实 worker **只走绑定**（dueTargets 的 FROM 是 credential_model_bindings）
		// ⇒ 拿不到 read_level='confirmed' 的模型，excluded_by_strict_gate 恒为 true
		// ⇒ **「等 models_blocked_by_strict 降到 0 再开闸」这条判据永远不会被满足。**
		//
		// ★ 为什么不去改 827 的视图：分母用全表是**刻意保守**的 —— 一个今天
		// 没绑定的模型明天可能绑上，那时它理应被算进去。保守是对的，缺的是
		// **把地板说清楚**，否则运维会一直等一个不会来的 0，或者在没有真实信号的
		// 情况下开闸。
		//
		// ★★ 地板必须**分两截**（2026-10-05 二次实测修正的精度缺陷）：
		//
		// 初版只报一个 floor_n = 「blocked ∧ ¬reachable」，文案写的是
		// 「can never be verified」。实测发现那句话**过强**：`reachable` 取的是
		// `modalityVerifyAddressableSource`，那是**当前状态**下能走到的集合（要求
		// available / lifecycle='active' / status∈(active,cooling,degraded) /
		// 未 manual_disabled / provider enabled），而「压根没有绑定」只是其中一种
		// 排除原因。逐个门拆开量（2026-10-05，同一份谓词，未手抄）：
		//
		//	blocked 总数                          = 960
		//	今天就能核实                         ≈ 580
		//	有绑定、但被状态门挡住（可恢复）        ≈ 150
		//	压根没有 provider_models 行（真地板）   ≈ 228
		//
		// 那 ≈150 的主导原因是**人为决策**，不是瞬时状态：credential.manual_disabled
		// 78 / provider.manual_disabled 65 / lifecycle<>'active' 82（一个模型可同时
		// 命中多个门）。它们「够不着」是因为**有人决定让它够不着**，这与「结构上
		// 永远够不着」是两回事，运维处置完全不同：前者该问「这条禁用要不要解」，
		// 后者该问「这些模型要不要接供应商」。
		//
		// ⚠ 初版注释里的「584 可探 / 376 地板」是**用错谓词量出来的**（手写谓词
		// 漏了 c.status IN ('cooling','degraded')、manual_disabled 与 p.enabled），
		// 与 modalityVerifyAddressableSource 声明处记的那次教训同源。改用常量后是
		// ≈580 / ≈379，且 379 还要再分 228 + 150。**这些数字随凭据状态漂移**
		// （实测相隔几分钟就差 5 个），所以注释给量级，检查的文案每次现算。
		//
		// 报法：**只报一行汇总**（不是逐模型），且只在「存在够不着的被挡模型」时
		// 报 —— 那种情况下这条检查才有话说。reachable 集合刻意引用
		// modalityVerifyAddressableSource（与 dueTargets **同一份**谓词）：抄第二份
		// 的话，两边一漂移，报出来的地板数就是**另一个集合**的数，而这条检查的
		// 全部价值恰恰在于「这个数字永远降不下去」这个事实。
		//
		// 同时给出**可探目标数**（绑定数），运维拿它和自己的日预算
		//（LLM_GATEWAY_MODALITY_VERIFY_DAILY_BUDGET，默认 2000）一比就知道还要跑
		// 几天。预算值刻意**不**写进 SQL：它是运维旋钮，钉死在查询里就成了
		// 第二份 SSOT。
		{CheckID: "modality_gate_readiness_floor", Severity: "warning", Optional: true, Query: `
WITH blocked AS (
    SELECT DISTINCT p.canonical_id
      FROM public.v_model_modality_verification_progress p
     WHERE p.excluded_by_strict_gate
), reachable AS (
    SELECT DISTINCT pp.canonical_id
      FROM ` + modalityVerifyAddressableSource + ` pp
     WHERE pp.canonical_id > 0
), -- ★★ 分桶的**唯一**判据是「有没有 credential_model_bindings 行」，不是
   -- 「有没有 provider_models 行」—— 这是 2026-10-05 一次变异实测逼出来的改正。
   --
   -- prober 走的是绑定，所以**没有绑定 = 等多久都不会变**；而
   -- provider_models 行只回答「要不要先接供应商」。
   -- 我第一版拿 provider_models 分桶，实测立刻暴露：那 4 个「无 pm 行」的模型
   -- 与「有 pm 行但无绑定」的孤儿模型被分到了**不同**的桶，而后者同样永远
   -- 不会被核实 —— 分桶按错了轴，报出来的「可恢复 1」会诱导运维去改一个禁用
   -- 开关，而那个模型根本没有开关可改。
   --
   -- provider_models 不丢掉，降级成**处置提示**（unregistered_n）：两者的补救
   -- 动作不同，一个要接供应商，一个只要建绑定，文案必须分开说。
   no_binding AS (
    SELECT b.canonical_id
      FROM blocked b
     WHERE NOT EXISTS (
             SELECT 1 FROM public.credential_model_bindings cmb
              JOIN public.provider_models pm ON pm.id = cmb.provider_model_id
             WHERE pm.canonical_id = b.canonical_id)
), unregistered AS (
    SELECT nb.canonical_id
      FROM no_binding nb
     WHERE NOT EXISTS (
             SELECT 1 FROM public.provider_models pm
              WHERE pm.canonical_id = nb.canonical_id)
), tally AS (
    SELECT (SELECT count(*) FROM blocked)                       AS blocked_n,
           (SELECT count(*) FROM blocked b JOIN reachable r
              ON r.canonical_id = b.canonical_id)               AS reachable_n,
           (SELECT count(*) FROM reachable)                     AS probe_targets,
           (SELECT count(*) FROM blocked b WHERE NOT EXISTS
              (SELECT 1 FROM reachable r WHERE r.canonical_id = b.canonical_id)) AS floor_n,
           (SELECT count(*) FROM no_binding)                    AS dead_n,
           (SELECT count(*) FROM unregistered)                  AS unregistered_n
)
SELECT
    -- 汇总行用 -1（0 是「扫描分支缺失」的特征值，见 baseline_price_missing）
    -1,
    '(readiness) strict gate would block ' || t.blocked_n || ' model(s); ' ||
    t.dead_n || ' can never be verified, and a further ' ||
    (t.floor_n - t.dead_n) || ' are unreachable only until credential/provider state changes',
    'opening LLM_GATEWAY_MODALITY_ROUTING_STRICT now would exclude ' || t.blocked_n ||
    ' model(s). Of the ' || t.floor_n || ' that cannot be verified today, ' ||
    t.dead_n || ' have NO binding, so no probe can ever be scheduled for them: ' ||
    t.unregistered_n || ' of those are not registered with any provider at all (onboard a ' ||
    'supplier), and the remaining ' || (t.dead_n - t.unregistered_n) || ' are registered but ' ||
    'unbound (create the binding). Neither improves by waiting. The other ' ||
    (t.floor_n - t.dead_n) || ' DO have bindings and are excluded only by current credential ' ||
    'or provider state (manual_disabled / lifecycle / available / provider_enabled), ' ||
    'which is a deliberate operator decision, so ask whether those should be re-enabled. ' ||
    'Today ' || t.reachable_n || ' of the blocked models are verifiable, against ' ||
    t.probe_targets ||
    ' probeable binding(s) (compare that with LLM_GATEWAY_MODALITY_VERIFY_DAILY_BUDGET to ' ||
    'estimate days remaining). Waiting for models_blocked_by_strict to reach 0 is ' ||
    'UNSATISFIABLE by construction.',
    ''
  FROM tally t
 WHERE t.blocked_n > 0 AND t.floor_n > 0
 LIMIT 1`},
		// supplier_price_missing_from_cost：按 token 计费、**正在被路由**、却**没有价**
		// 的绑定。这是「准确控制模型实际成本」这条目标上最直接的一处落空。
		//
		// # 为什么需要（2026-10-05 真环境实测出来的，不是推理）
		//
		// `domains/streaming.CalcCost`（domains/streaming/usage.go:208，
		// 经 AssignRequestCost 调用，handler.go:6619）有一句干净的守卫：
		// `if priceIn == 0 && priceOut == 0 { return nil }`。⇒ **零价绑定的
		// 成本是「算不出来」而不是 0**，telemetry 于是记成 cost_usd IS NULL，
		// 与「真的免费」在账上分不开。
		//
		// ⚠️ 本文件原先把这道守卫归给 `provider.Candidate.CalcCost`
		// （provider/client.go:267）。那个函数是**死代码**（零调用者），
		// 而且语义不同：它 return 0 而不是 nil。归因错了会让人以为改它有用。
		// 价格列本身没错 —— 两条路径都读 Candidate 的 PriceInPer1M 等字段。
		//
		// 真库读数（127.0.0.1:5432，全程只跑 SELECT）：
		//
		//	全库绑定                                = 2045
		//	billing_mode 分布：per_token 1273 / token_plan 645 / free 105
		//	                   / code_plan 17 / token 4 / monthly 1
		//	可路由 + per_token + **有**有效价          = 0    ← 稳定事实
		//	可路由 + per_token + 零价                 ≈ 150 绑定 / ≈ 129 模型
		//
		// ⇒ **当前能被路由到的按 token 计费流量，没有一条算得出成本**，全部报 0。
		// 成本汇总看起来很小，是因为它**没算**，而不是因为便宜。
		//
		// ⚠ 「≈150」这个数**会漂**（同一分钟连查三次是 146/126，两分钟后是
		// 149/129）：视图的 is_routable 含 `next_retry_at > now()` 的探针退避
		// （node_probe_state 里 379 行在退避中）。所以这里**不钉具体数字**，
		// 只钉「有价的是 0 条」——后者在任何读数下都成立，才是这条检查的骨架。
		//
		// ★ 这条检查的全部价值在于**把「未知的 0」和「已知的 0」分开**：
		// `free` / `token_plan` / `code_plan` 的 0 是**对的**（赠送 / 预付包月，
		// 边际 token 成本确实为 0），把它们一起报出来就是噪声；而 `per_token`
		// 的 0 只可能是**价没填**。两者的差别在 billing_mode 里，不在价格里。
		// （真库当前：可路由的 token_plan 零价有 83 条，它们**不报**。）
		//
		// ★ 总体必须取自 v_routable_credential_models.is_routable，**不能手挑**
		// status/lifecycle/enabled 那一组列：第一版手抄了 6 个谓词，量出 858 条
		// 「可路由」，而按视图只有约 150 条 —— 差的 700 多条是配额耗尽、手工禁用、
		// 探针失败、auth 失败，**根本不会被路由**。仓库里已经有一扇门把这个坑写成
		// 了判据：deploy/sql/verify/routing_gate_warning_pg_test.go 的注释
		// 「status/lifecycle）挑「可路由」会选到实际不可路由的凭据」。同一条纪律，
		// 这次是先撞上再写下来。
		//
		// ★ 「没有价」必须按**候选查询真正取价的那条路**判，不能只看绑定上那两个
		// 列：provider/client.go:1733 是
		// `COALESCE(mo.unit_price_in_per_1m, pp_fb.plan_in)` —— 绑定价为 NULL 时
		// 会回落到 pricing_plans 的计划价（按 _mc_id = COALESCE(pm.canonical_id,
		// 别名解析) 配 scope 守卫）。所以这里带一个**保守的 EXISTS**：只要存在任一
		// 命中该模型、当前生效、且 input/output 至少给了一个价的计划行，就算「有价」。
		// 这个 EXISTS 只会让本检查**少报**，不会多报（多计划行时生产侧按 effective_from
		// 取一条，这里按「有就算有」）—— 方向是保守的那一侧。
		// 实测当前 0 条命中（兜底救不回任何一个），但它是承重的：某天有人填了
		// pricing_plans，这条检查若不含它就会对着有价的绑定喊没价。
		//
		// 报法：按**模型**去重后 ≤ 20 就逐个列名，> 20 只报一行汇总（与
		// baseline_price_missing 同一取舍）—— 真环境约 129 个模型，逐条报会占满
		// 告警位，而它们说的是同一件事。
		//
		// 刻意**不**报「成本被低估了多少」：那要拿基准价相乘，而 SSOT 的
		// bg/data/model_baseline_prices.json 刻意是空的（models: []）⇒ 算不出金额。
		// 能确定的是**多少计费流量没被计**，那就只报这个。
		{CheckID: "supplier_price_missing_from_cost", Severity: "warning", Optional: true, Query: `
WITH pt AS (
    -- ★ 总体 = 视图判定「可路由」的 per_token 绑定。billing_mode 取视图那一列
    -- （它直通 cmb.billing_mode），不重抄 WHERE。
    SELECT v.credential_id,
           v.provider_id,
           v.raw_model_name,
           -- ★ p_in / p_out 是 COALESCE 后的值，只供「大于 0」比较用（NULL 安全）。
           --   **报告措辞绝不能引用它们**：COALESCE 造出来的 0 不是库里存的值。
           --   真库实测（2026-10-05）：可路由 per_token 绑定 145 条，raw 两列全 NULL
           --   的 145 条、字面 0 的 0 条。原消息说「are priced at 0」，而照它去
           --   WHERE unit_price_in_per_1m = 0 找，一条都找不到——运营会以为检查
           --   坏了。所以下面把 raw 值单独带出来，用于把「没填」与「填了 0」分开报。
           COALESCE(cmb.unit_price_in_per_1m, 0) AS p_in,
           COALESCE(cmb.unit_price_out_per_1m, 0) AS p_out,
           cmb.unit_price_in_per_1m AS raw_in,
           cmb.unit_price_out_per_1m AS raw_out,
           -- 候选查询里的 _mc_id = COALESCE(mo.canonical_id, 别名解析)。
           -- 用标量子查询而不是再 JOIN 一次 model_aliases：后者会扇出（实测
           -- 146 → 166 行），而扇出会让**计数本身**失真。
           COALESCE(pm.canonical_id,
                    (SELECT ma.canonical_id
                       FROM public.model_aliases ma
                      WHERE ma.raw_name = pm.canonical_raw_name
                        AND COALESCE(ma.status, 'active') = 'active'
                      LIMIT 1)) AS mc_id
      FROM public.v_routable_credential_models v
      JOIN public.credential_model_bindings cmb ON cmb.id = v.binding_id
      JOIN public.provider_models pm ON pm.id = cmb.provider_model_id
     WHERE v.is_routable = TRUE
       AND v.billing_mode = 'per_token'
), eff AS (
    SELECT pt.*,
           (p_in > 0
            OR p_out > 0
            OR EXISTS (
                 SELECT 1
                   FROM public.pricing_plans pp
                  WHERE pp.model_canonical_id = pt.mc_id
                    AND pp.effective_to IS NULL
                    AND (pp.credential_id = pt.credential_id OR pp.credential_id IS NULL)
                    AND NOT (pp.scope = 'provider'
                             AND pp.provider_id IS NOT NULL
                             AND pp.provider_id <> pt.provider_id)
                    AND (NULLIF(pp.plan_json->>'input_per_1m', '') IS NOT NULL
                      OR NULLIF(pp.plan_json->>'output_per_1m', '') IS NOT NULL)
               )) AS priced
      FROM pt
), unpriced AS (
    SELECT DISTINCT raw_model_name, raw_in, raw_out FROM eff WHERE NOT priced
), priced_n AS (
    SELECT count(DISTINCT raw_model_name) AS n FROM eff WHERE priced
), unpriced_split AS (
    -- 「没填」与「填了 0」必须分开报：前者是缺数据，后者是有数据且可疑。
    -- 两者被合并时，运营要么去改 NULL（对的），要么去改 0（错的），
    -- 而 check 的名字/文案会让他以为那是同一件事。
    SELECT count(*) FILTER (WHERE raw_in IS NULL AND raw_out IS NULL) AS price_absent_n,
           count(*) FILTER (WHERE NOT (raw_in IS NULL AND raw_out IS NULL)) AS price_zero_n
      FROM unpriced
)
SELECT
    0,  -- 占位：Go 侧按 entity_name 重算 entity_id（同 baseline_price_missing）
    u.raw_model_name,
    'routable and billed per token, but it carries NO effective price: the binding price ' ||
        'columns are ' ||
        CASE WHEN u.raw_in IS NULL AND u.raw_out IS NULL
             THEN 'both NULL (never filled in)'
             ELSE 'literally 0 on both columns' END ||
        ', and no pricing_plans row supplies a fallback — so CalcCost cannot compute a ' ||
        'cost for it and no per-token cost total includes it. Of ' ||
        ((SELECT count(*) FROM unpriced) + (SELECT n FROM priced_n)) ||
        ' routable per-token model(s), ' || (SELECT n FROM priced_n) || ' carry a price; of the ' ||
        (SELECT count(*) FROM unpriced) || ' that do not, ' ||
        (SELECT price_absent_n FROM unpriced_split) || ' have no price recorded at all and ' ||
        (SELECT price_zero_n FROM unpriced_split) || ' are quoted at a literal 0. ' ||
        'This is NOT billing_mode=''free''/''token_plan''/''code_plan'', where 0 is the correct ' ||
        'marginal cost.',
    ''
  FROM unpriced u
 WHERE (SELECT count(*) FROM unpriced) <= 20
UNION ALL
SELECT
    -- 汇总行用 -1（0 是「扫描分支缺失」的特征值，见 baseline_price_missing）
    -1,
    '(bulk) ' || (SELECT count(*) FROM unpriced) ||
        ' routable per-token model(s) carry no effective price, so their cost is counted as 0',
    'available, routed, billing_mode=''per_token'', yet there is no effective price at all: ' ||
        (SELECT price_absent_n FROM unpriced_split) ||
        ' of them have unit_price_in_per_1m / unit_price_out_per_1m **NULL** (never filled in) and ' ||
        (SELECT price_zero_n FROM unpriced_split) ||
        ' carry a literal 0, and no pricing_plans row supplies a fallback for any of them, so ' ||
        'CalcCost returns nil for them (not 0) — every per-token cost total silently omits them. ' ||
        '★ The NULL/0 split is stated here on purpose: they need opposite fixes. Filling in a NULL is ' ||
        'the only correct action for it. A literal 0 may be wrong (price forgotten) OR genuinely ' ||
        'right (supplier says free) — some ' ||
        'suppliers hand a model away free while still metering per token ' ||
        '(measured 2026-10-05: 208 nvidia bindings over 44 open models — llama-3.1-8b-instruct, ' ||
        'gemma-3-12b-it, gpt-oss-20b, nemotron-* — all quoted at 0 by the price source, and all of ' ||
        'them currently non-routable so this alert does not fire for them today). This check cannot ' ||
        'tell those two apart: it holds no supplier price list. So do NOT type a number in because this ' ||
        'alert fired — classify first: cmd/tools/propose-supplier-prices files a zero-quoted offer ' ||
        'under published_price_is_zero ("the supplier says free"); only the ones left unclassified ' ||
        'are genuinely missing a price. Of ' ||
        ((SELECT count(*) FROM unpriced) + (SELECT n FROM priced_n)) ||
        ' routable per-token model(s), ' || (SELECT n FROM priced_n) ||
        ' carry a price. Listing them one by one would be 100+ rows of the same fact.',
    ''
 WHERE (SELECT count(*) FROM unpriced) > 20
 ORDER BY 1, 2
 LIMIT 50`},

		// 第 14 条：已经记下来的**负成本**。
		//
		// 2026-10-06 真库实测（127.0.0.1:5432，全程只读）：
		//   request_logs  cost_usd < 0            = 1,628 行，合计 -$4.79
		//   usage_ledger  cost_usd < 0            = 1,299 行，合计 -$4.67
		//   全部满足 cache_read_tokens > prompt_tokens；cache 占那批 token 的
		//   96.2%（4,530,529 vs prompt 179,144）；全部来自 apiclaude
		//   （Anthropic 协议的中转）；已污染 stats_usage_daily 225 行、
		//   stats_usage_monthly 12 行。
		//
		// 根因：CalcCost 里「cache 从 prompt 里减掉」那两段**假定
		// prompt_tokens 含 cache**（OpenAI 口径），而 Anthropic 口径相反
		// （input_tokens 不含 cache_read_input_tokens）⇒ promptCost 被减成负数。
		// 写入侧已于 2026-10-06 加了 `if total < 0 { return nil }`，但
		// **已记账的历史行不会被改**，且下一个协议口径出现时仍可能复发。
		//
		// 为什么检查侧要能自己发现：这是**「已经错了」**，而前 13 条盯的都是
		// 「会不会错」（价格列、基准价、币种）。写入侧加守卫不等于台账干净 ——
		// 检查侧不能假定写入侧永远是干净的（同 supplier_price_currency_mismatch
		// 的理由）。
		//
		// 报法：按 (credential_id, outbound_model) 汇总，一行一个组合 —— 同一个模型
		// 在不同凭据下可能走不同协议，按模型去重会把「哪些组合在错」这个
		// 定位信息抹掉。
		//
		// ★ 分组列是 **outbound_model**，不是 raw_model_name —— 这两个名字在
		//   request_logs 里**都存在**（该表有 6 个 model 列：client_model /
		//   raw_model_name / canonical_model / model_chosen / outbound_model /
		//   provider_model），注释里写 "raw_model" 会把人引到错的那一列。
		//   真库实测（2026-10-06 只读核过）：1628 条负成本行上
		//   outbound_model 与 raw_model_name **全部不同**（1628/1628），
		//   按 outbound_model 分组得 4 组、按 raw_model_name 得 2 组；更要命的是
		//   **raw_model_name 在这 1628 行上全为空**，按它分组得到的是「两个凭据
		//   各一个空模型名」的假分组，定位信息直接归零。谁照注释去查
		//   raw_model_name，会看到「负成本行没有模型名」并以为数据坏了。
		//   （这条注释自己就犯过一次这个错 —— 记下来以防再犯。）
		//
		// ★ 刻意**不 JOIN providers**：provider 名称只是好看，而它会给这条检查
		//   添一个必须存在的表依赖。真库判据实测踩到过 —— 夹具只建
		//   request_logs + credentials 时整条检查红在
		//   `relation "public.providers" does not exist`，而那与本检查要问的
		//   「有没有负成本」毫无关系。凭据 id + 模型名已经足够定位。
		//   （entity_name 用 'credential#<id>:<model>'，人可读且不引依赖。）
		//
		// ★ 不报「应该收多少钱」：那要拿供应商价目相乘，而 SSOT 的
		// bg/data/model_baseline_prices.json 刻意是空的（models: []），
		// 基准价当前 0 条 ⇒ 算不出金额。能确定的是**有多少行的成本是负的**、
		// **偏移了多少美元**，那就只报这两个。
		{CheckID: "recorded_cost_is_negative", Severity: "warning", Optional: true, Query: `
WITH neg AS (
    SELECT r.credential_id,
           r.outbound_model,
           count(*)::int      AS neg_rows,
           sum(r.cost_usd)    AS neg_usd,
           sum(COALESCE(r.cache_read_tokens, 0) - COALESCE(r.prompt_tokens, 0)) AS excess_cache
      FROM public.request_logs r
     WHERE r.cost_usd < 0
       AND r.ts > now() - interval '30 days'
     GROUP BY r.credential_id, r.outbound_model
)
SELECT neg.credential_id::text || '|' || neg.outbound_model,
       'credential#' || neg.credential_id || ':' || neg.outbound_model,
       neg.neg_rows || ' row(s), sum=' || round(neg.neg_usd::numeric, 6) ||
         ' USD over 30 days, cache_read exceeds prompt_tokens by ' || neg.excess_cache ||
         ' token(s) in total',
       'A negative cost is not "cheap", it is a miscomputation: CalcCost assumes ' ||
         'prompt_tokens INCLUDES cache tokens (OpenAI: prompt_tokens superset of ' ||
         'cached_tokens), but Anthropic usage reports input_tokens EXCLUDING ' ||
         'cache_read_input_tokens, so the cache deduction drives promptCost negative. ' ||
         'Two consequences: (1) the cache traffic on those rows is billed at roughly ' ||
         'zero, and (2) any SUM over cost_usd is polluted. The write path now returns ' ||
         'nil (cost not recorded) instead of a negative number — nil means "cannot ' ||
         'compute", whereas 0 would assert "this request was free", which is its own ' ||
         'lie and would not trip supplier_price_missing_from_cost because the price ' ||
         'columns are populated. This alert exists because the guard does NOT repair ' ||
         'rows already written: fixing the token convention per protocol changes ' ||
         'booked amounts and is an operator decision, so decide it before trusting ' ||
         'any cost total that includes these credentials.'
     FROM neg
     ORDER BY neg.neg_usd ASC, neg.credential_id, neg.outbound_model
     LIMIT 50`},

		// 第 15 条：问的不是「数据对不对」，而是「你有没有能力知道数据对不对」。
		//
		// 背景（真库只读核过 2026-10-05）：stats 对账把 usage_facts 当 ground
		// truth、把 stats_usage_daily 当投影。但 usage_facts 的 9 个日分区
		// （2026-09-26 … 2026-10-03）**全部 0 行**，只有 10-04/10-05 有数据；
		// 而 stats_usage_daily 覆盖 2026-08-18 起。于是对账在 09-28 起的每一天
		// 都拿「投影里有、ground truth 里没有」比了一遍，90 次运行累计
		// 106,092 条差异、81,094 条至今 open、20,725 条 phantom_open。
		//
		// 为什么这值得单列一条检查：canAutoRepair 正确地拒绝修复 source=0 的
		// 幻影行（所以**没有数据被破坏**），代价是这些行全部堆在 open 队列里。
		// 于是「成本对账漂移了」这个告警被结构性噪声淹没——81,094 条里没有一条
		// 能被单独相信。**饱和的信号等于没有信号**：真出现一次真实的成本漂移，
		// 它淹没在同样的队列里，看不出来。
		//
		// 报法：只报「投影覆盖窗口的起点比 ground truth 早多少天」这一个标量事实
		//   + 未决差异数，不去判断每条差异的对错（那是人工的事）。
		//
		// 不 JOIN stats_reconciliation_diffs 的行级数据，只做一次 count(*)：
		//   差异台账一天几万行，逐行带出来会把健康表撑爆，而这条检查要回答的问题
		//   只需要一个总数。
		{CheckID: "stats_ground_truth_gap", Severity: "warning", Optional: true, Query: `
WITH facts AS (
  SELECT min(occurred_at)::date AS oldest FROM public.usage_facts
), proj AS (
  SELECT min(day_utc) AS oldest, max(day_utc) AS newest FROM public.stats_usage_daily
)
SELECT 'facts_start_' || facts.oldest::text || '_vs_projection_' || proj.oldest::text,
       'usage_facts ground truth vs stats_usage_daily projection',
       'usage_facts holds no source facts before ' || facts.oldest ||
       ' while the projection already covers from ' || proj.oldest || ' through ' || proj.newest ||
       ', so ' || (facts.oldest - proj.oldest) ||
       ' day(s) of the reconciled window have no ground truth at all. Every projection row ' ||
       'in that gap reconciles as a phantom, and the unresolved diff backlog is currently ' ||
       (SELECT count(*) FROM public.stats_reconciliation_diffs
         WHERE resolution IN ('open', 'phantom_open')) ||
       ' row(s). Auto-repair correctly refuses a zero source, so nothing was corrupted -- but ' ||
       'a backlog dominated by artifacts cannot be told apart from real cost drift, which ' ||
       'means this reconciliation can no longer answer "did my recorded cost drift". Backfill ' ||
       'usage_facts for the gap from the stats event inbox, or narrow the reconciliation ' ||
       'window until it does not. Do not bulk-resolve the backlog first: that would delete ' ||
       'the evidence of which rows were real.'
  FROM facts, proj
 WHERE facts.oldest IS NOT NULL
   AND proj.oldest IS NOT NULL
   AND proj.oldest < facts.oldest`},
	}
}

func RunChecks(
	ctx context.Context, db *pgxpool.Pool) (newCritical, newWarning int, err error) {
	return runChecks(ctx, db, AllHealthChecks())
}

// runChecks 是 RunChecks 的可测内核：接受任意检查集合。
//
// 为什么抽出来：Optional（缺表跳过）这件事**只有在真库上、且那张表真的不在**
// 时才量得到。而夹具库不可能同时满足其余检查依赖的十几个对象，所以唯一能走到
// 42P01 分支的办法就是能只跑一条。没有这个拆分，「表不存在会不会中止整轮」
// 只能靠读代码猜。
func runChecks(ctx context.Context, db *pgxpool.Pool, checks []HealthCheckDef) (newCritical, newWarning int, err error) {
	now := time.Now()

	for _, chk := range checks {
		rows, qErr := db.Query(ctx, chk.Query)
		if qErr != nil {
			// 缺表（42P01）且这条检查显式声明 Optional ⇒ 跳过并继续，
			// 而不是中止整轮。见 HealthCheckDef.Optional 的注释。
			//
			// 判据是 **Optional + 42P01 两个条件同时成立**，不是「遇到 42P01
			// 就跳过」：后者会把 provider_models 这类核心表消失也说成一切正常。
			var pgErr *pgconn.PgError
			if chk.Optional && errors.As(qErr, &pgErr) && pgErr.Code == "42P01" {
				slog.Info("routing health check skipped: optional object not present",
					"check_id", chk.CheckID, "relation", pgErr.Message, "code", pgErr.Code)
				continue
			}
			err = fmt.Errorf("query %s: %w", chk.CheckID, qErr)
			return
		}
		defer rows.Close()
		found := 0
		for rows.Next() {
			found++
			var entityID int64
			var entityName, detail, fixSQL string

			switch chk.CheckID {
			case "canonical_id_null":
				var pmID, mcID int64
				var rawName string
				if scanErr := rows.Scan(&pmID, &rawName, &mcID); scanErr != nil {
					continue
				}
				entityID = pmID
				entityName = rawName
				detail = fmt.Sprintf("provider_models.id=%d raw_model_name='%s' → models_canonical.id=%d (canonical_id=NULL)", pmID, rawName, mcID)
				fixSQL = fmt.Sprintf("UPDATE provider_models SET canonical_id = %d WHERE id = %d;", mcID, pmID)

			case "billing_mismatch":
				var cmbID int64
				var credModel, credPlan, cmbBilling string
				if scanErr := rows.Scan(&cmbID, &credModel, &credPlan, &cmbBilling); scanErr != nil {
					continue
				}
				entityID = cmbID
				entityName = credModel
				detail = fmt.Sprintf("cred_plan='%s' cmb_billing='%s' → v.is_routable=false", credPlan, cmbBilling)
				parts := strings.SplitN(credModel, ":", 2)
				if len(parts) == 2 {
					// fix_sql is display/copy text only — raw_model_name comes
					// from upstream model catalogs, so the literal must be
					// escaped and one-click fixes run the parameterized
					// canned statement in admin.cannedFix instead of this text.
					fixSQL = fmt.Sprintf("UPDATE credential_model_bindings cmb SET billing_mode = %s, plan_type_origin = 'manual_fix', plan_type_updated_at = now() FROM provider_models pm WHERE pm.id = cmb.provider_model_id AND cmb.credential_id = %s AND pm.raw_model_name = %s; -- display only, applied via parameterized fix channel", pgQuoteLiteral(credPlan), parts[0], pgQuoteLiteral(parts[1]))
				}

			case "probe_missing":
				var cmbID, credID int64
				var modelName string
				if scanErr := rows.Scan(&cmbID, &entityName, &modelName, &credID); scanErr != nil {
					continue
				}
				entityID = cmbID
				detail = fmt.Sprintf("credential_id=%d model='%s' 无 probe_state 记录", credID, modelName)
				fixSQL = "-- 需要通过 admin API 触发 probe: POST /api/credentials/{credID}/test"

			case "family_unknown":
				var id int64
				var name string
				if scanErr := rows.Scan(&id, &name, &entityName, &detail); scanErr != nil {
					continue
				}
				entityID = id
				entityName = name
				detail = fmt.Sprintf("canonical_name='%s' family='unknown'，可由 InferFamily 自动归类", name)
				fixSQL = fmt.Sprintf("UPDATE models_canonical SET family = '<需要人工确认>' WHERE id = %d;", id)

			case "circuit_open":
				var cmbID int64
				var credModel, circuitState, availState string
				if scanErr := rows.Scan(&cmbID, &credModel, &circuitState, &availState); scanErr != nil {
					continue
				}
				entityID = cmbID
				entityName = credModel
				detail = fmt.Sprintf("circuit_state='%s' availability_state='%s'", circuitState, availState)
				fixSQL = fmt.Sprintf("-- credential 熔断器未关闭，需手动恢复或等待自动恢复\n-- 检查: SELECT circuit_state, circuit_opened_at FROM credentials WHERE id = %s;", strings.SplitN(credModel, ":", 2)[0])

			case "credential_active_not_routable":
				var credID int64
				var credName, availState, circuitState string
				if scanErr := rows.Scan(&credID, &credName, &availState, &circuitState); scanErr != nil {
					continue
				}
				entityID = credID
				entityName = credName
				detail = fmt.Sprintf("credential active but has zero routable bindings (availability=%s circuit=%s)", availState, circuitState)
				fixSQL = fmt.Sprintf("-- 调用 admin API: POST /api/admin/diagnostics/routing-blocked/fix\n-- Body: {\"provider_id\": <该 credential 的 provider_id>}")
			// ↓↓↓ 两条基线价检查的扫描分支（迁移 826 / 832）。
			//
			// ★ 它们的查询**本来就返回 4 列**，形状与上面几条一致；但没有 case 时
			// switch 走空，entityID/entityName/detail/fixSQL 全保持零值，而循环
			// **照样 INSERT** —— 健康面里于是多出一行 entity_id=0、entity_name=''、
			// detail='' 的记录，并按声明的 severity 计一次告警。症状是
			// 「查得到问题、报不出内容」。
			//
			// 这不是推测：真库实测（查询命中 1 行时）插出来的正是
			// entity_id=0 entity_name="" detail="" fix_sql=""。所以下面两条分支与
			// 它们的查询是**成对**的 —— 改查询列数就必须改这里，而
			// TestEveryHealthCheckHasScanBranch 钉住「每条 CheckID 都必须有 case」，
			// 防止再加第三条。
			case "baseline_observation_stale":
				var srcURL, name, detailText, fixText string
				if scanErr := rows.Scan(&srcURL, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// 这张表按 source_url（文本）主键，而 routing_health_checks.entity_id
				// 是 bigint。用稳定哈希当 id：同一 url 永远落同一行，跨轮刷新不会
				// 每次插一条新记录。
				entityID = textHash(srcURL)
				entityName = srcURL
				detail = detailText
				fixSQL = fixText

			case "baseline_price_missing":
				var placeholderID int64
				var name, detailText, fixText string
				if scanErr := rows.Scan(&placeholderID, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// 查询首列是占位 0：826 的偏差视图不暴露 canonical_id，而这条检查
				// 已按 canonical_name 去重（一模型一行），所以实体是**模型**、
				// id 走稳定文本哈希 —— 与 baseline_observation_stale 同一套，
				// 保证同一模型跨轮刷新落同一行、UPSERT 能收敛。
				//
				// 汇总行（"(bulk) …"）的 entity_name 不是模型名，哈希后与任何模型都
				// 不会撞，且它本来就只有一行。
				entityID = textHash(name)
				entityName = name
				detail = detailText
				fixSQL = fixText
			case "pricing_plan_stale":
				// 首列恒为 -1（bulk 汇总行的特征值，刻意不用 0 —— 0 是
				// 「扫描分支缺失、整行全零」的特征值，见 health_check_scan_guard_test.go）。
				// 实体是**这张表本身**而不是某个模型：这条报的是「整份定价 SSOT
				// 有多旧、有多少行指不到模型」，不是一个逐模型的发现。
				// entity_id 走 textHash(entity_name)，保证同一句话跨轮落同一行。
				var placeholderID int64
				var name, detailText, fixText string
				if scanErr := rows.Scan(&placeholderID, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				entityID = textHash(name)
				entityName = name
				detail = detailText
				// 不给一键修复：处置是**重抓原厂页并重新灌价**（要人跑
				// fetch-pricing.sh + apply-pricing.sh），不是一个 UPDATE 能解决的。
				// 假 fix_sql 只会让人点一个无效按钮。
				fixSQL = fixText
			case "supplier_price_drift", "supplier_price_currency_mismatch":
				var key, name, detailText, fixText string
				if scanErr := rows.Scan(&key, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// 偏差告警。首列是 `credential_id|raw_model_name` 文本键（视图不暴露绑定
				// id），所以 entity_id 走 textHash —— 同一绑定跨轮刷新落同一行。
				entityID = textHash(key)
				entityName = name
				detail = detailText
				// 不给一键修复：这条要的是**与供应商谈价 / 换档位 / 换供应商**，
				// 不是一个 UPDATE 能解决的。假 fix_sql 只会让人点一个无效按钮。
				// 币种不一致那条甚至需要先定汇率口径。
				fixSQL = fixText

			case "modality_gate_readiness_floor":
				var placeholderID int64
				var name, detailText, fixText string
				if scanErr := rows.Scan(&placeholderID, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// 汇总行，首列恒为 -1。entity_id 走 textHash(name)：这一行的
				// entity_name 是**每次读数都不同的句子**（含几个计数），哈希后会
				// 每次换一行 ⇒ 健康表里堆出一串历史行，而不是稳定刷新同一行。
				//
				// ⚠ 所以这里刻意**不用**查询给的 -1 落库，而是对下面这个**常量
				// 键**取哈希 —— 它不随读数变化：
				entityID = textHash("modality_gate_readiness_floor")
				entityName = name
				detail = detailText
				// 没有 fix_sql：这条要的是「决定要不要给这些模型配绑定 / 接受地板」，
				// 不是一个 UPDATE 能表达的。
				fixSQL = fixText

			case "supplier_price_missing_from_cost":
				var placeholderID int64
				var name, detailText, fixText string
				if scanErr := rows.Scan(&placeholderID, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// 实体是**模型**（查询已按 raw_model_name 去重）⇒ 首列占位 0，
				// entity_id 走 textHash(name)，与 baseline_price_missing 同一套。
				//
				// ★ 汇总行（首列恒为 -1）必须走**常量键**而不是 name：它的 entity_name
				// 是含计数的句子（"(bulk) 129 routable per-token model(s) …"），读数
				// 一变哈希就变 ⇒ 健康表里每次读数堆一行历史，而不是刷新同一行
				// （与 modality_gate_readiness_floor 同一个坑）。
				if placeholderID == -1 {
					entityID = textHash("supplier_price_missing_from_cost")
				} else {
					entityID = textHash(name)
				}
				entityName = name
				detail = detailText
				// 没有 fix_sql：填价要么查供应商价目、要么定 billing_mode，都不是
				// 一个 UPDATE 能表达的。给个假按钮只会让人点了白点。
				fixSQL = fixText

			case "recorded_cost_is_negative":
				// 查询首列是 'credential_id|outbound_model' 的合成键（**字符串**），
				// 不是数值 id —— 因为实体是「凭据×模型」组合，两者都不是唯一。
				var key, name, detailText, fixText string
				if scanErr := rows.Scan(&key, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// 走 textHash(key) 而不是把字符串塞进 int64 列：health 表的
				// UNIQUE 是 (check_id, entity_type, entity_id)，而 entity_id 是
				// bigint ⇒ 合成键必须先哈希。与 baseline_price_missing 同一套。
				entityID = textHash(key)
				entityName = name
				detail = detailText
				// 不给一键修复：真修法是按协议归一化 token 口径，那会改动已记账的
				// 金额，是运营决定，不是一个 UPDATE 能表达的。给个假按钮只会让
				// 人点了白点（与 supplier_price_missing_from_cost 同一取舍）。

			case "stats_ground_truth_gap":
				// 首列是两个日期拼出来的窗口键（**字符串**），与上面那条同形：
				// 既不是数值 id 也不唯一，所以同样走 textHash。
				var key, name, detailText string
				if scanErr := rows.Scan(&key, &name, &detailText); scanErr != nil {
					continue
				}
				entityID = textHash(key)
				entityName = name
				detail = detailText
				// 不给一键修复：补 usage_facts 是一次回填，不是 UPDATE。更要紧的是
				// **不能**提供「把这些差异标成已解决」的按钮——那会删掉唯一能分辨
				// 真实成本漂移和幻影行的证据（见检查定义里的说明）。

			case "modality_verification_stale":
				var canonID int64
				var name, detailText, fixText string
				// 目标第一半的核实进度。entity_id 用 canonical_id（真数值 id），所以这条
				// 不像 baseline_observation_stale 那样需要 textHash —— 同一模型在不同轮
				// 刷新落同一行。
				if scanErr := rows.Scan(&canonID, &name, &detailText, &fixText); scanErr != nil {
					continue
				}
				// ★ entity_id **不能**直接用 canonical_id：health 表的 UNIQUE 是
				//   (check_id, entity_type, entity_id)，而这条检查对**每个模态**报一行。
				//   同一模型的 vision/audio/video 三行会撞成同一行，UPSERT 的 DO UPDATE
				//   互相覆盖 —— 真库实测：查询命中 5 行、warning 也计了 5，而表里只剩
				//   2 行（2 个模型各一行）。丢掉的正是「这个模型还差哪几个模态」。
				//   ⇒ 实体是 **(模型, 模态)** 组合，不是模型。
				mod := ""
				if j := strings.LastIndex(name, " ["); j >= 0 && strings.HasSuffix(name, "]") {
					mod = name[j+2 : len(name)-1]
				}
				entityID = composePairID(canonID, mod)
				entityName = name
				detail = detailText
				// 不提供一键修复：这条要的是**出网探测**（带挑战图、消耗预算、两胜
				// 定论），不是一个 UPDATE 能解决的。给一个假的 fix_sql 只会让运维
				// 点一个注定无效的按钮。
				fixSQL = fixText
			}

			_, insErr := db.Exec(ctx, `
				INSERT INTO routing_health_checks
					(check_id, severity, entity_type, entity_id, entity_name, detail, fix_sql, status, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'open',$8,$8)
				ON CONFLICT (check_id, entity_type, entity_id)
				DO UPDATE SET detail=EXCLUDED.detail, fix_sql=EXCLUDED.fix_sql, updated_at=EXCLUDED.updated_at
				WHERE routing_health_checks.status IN ('open','auto_fixed')`,
				chk.CheckID, chk.Severity, chk.CheckID, entityID, entityName, detail, fixSQL, now)
			if insErr != nil {
				err = fmt.Errorf("upsert %s:%d: %w", chk.CheckID, entityID, insErr)
				return
			}

			if chk.Severity == "critical" {
				newCritical++
			} else {
				newWarning++
			}
		}
		if rows.Err() != nil {
			err = fmt.Errorf("rows %s: %w", chk.CheckID, rows.Err())
			return
		}
	}

	// auto-fix: canonical_id_null (safe exact match)
	applied, fixErr := autoFixCanonicalID(ctx, db, now)
	if fixErr != nil {
		err = fmt.Errorf("auto_fix canonical_id: %w", fixErr)
		return
	}
	if applied > 0 {
		newCritical -= applied
	}

	return
}

// pgQuoteLiteral doubles single quotes so DB string values (which upstream
// model catalogs can influence) stay inside SQL string literals when fix_sql
// is rendered. The result is display/copy text only — one-click fixes run the
// parameterized canned statements in admin.cannedFix, never this text.
func pgQuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// textHash 把一段文本折成一个稳定的 int64，供 routing_health_checks.entity_id
// （bigint）使用。
//
// 为什么需要它：model_baseline_price_observation_health 按 **source_url**（文本）
// 主键，而健康面那张表的 entity_id 是 bigint。观察源这条检查的「实体」天然是
// 文本 URL，没有现成的数值 id。
//
// 为什么必须是**稳定**哈希而不是长度/序号：
//   - 长度：两个不同的 url 极易等长（"a" 与 "b"）⇒ 互相覆盖。
//   - 序号：依赖扫描顺序 ⇒ 每轮刷新顺序变一下，同一个问题就变成新行，旧行留在
//     status='open' 变成永不消失的僵尸告警。
//
// 稳定哈希保证「同一个 url 永远落同一行」，UPSERT 才能收敛。
//
// 实现用 FNV-1a 64：纯标准库、无依赖、跨进程跨版本确定（不像 Go 的
// maphash.Hash 每次进程都换种子）。
func textHash(s string) int64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return int64(h)
}

// composePairID 把 (canonical_id, modality) 合成一个**稳定且互不冲突**的 int64，
// 供 routing_health_checks.entity_id（bigint）使用。
//
// 为什么需要它：modality_verification_stale 这条检查对**每个模态**报一行，而
// health 表的 UNIQUE 是 (check_id, entity_type, entity_id)。只用 canonical_id
// 的话，同一模型的 vision / audio / video 三行会撞成同一行，UPSERT 的
// DO UPDATE 互相覆盖 —— 真库实测过：查询命中 5 行、warning 也计了 5，而表里
// 只剩 2 行。「这个模型还差哪几个模态」这个信息就整个丢了。
//
// 合成方式：canonical_id 保留在高位、模态序号放低位。序号取自 827 视图枚举的
// 同一份顺序 {vision, audio, video}（modalityOrdinal），这样 id 与模态的对应
// 是可读的，而不是一个不透明的黑盒哈希。
//
// ⚠ 未知模态**不会**被静默折叠成同一个 id：modalityOrdinal 返回 0，合成后与
// vision 撞车。宁可撞车（可在日志里看见）也不要给两个不同模态发同一个 id ——
// 那会让后者无声消失，正是本函数要防的那种丢行。
func composePairID(canonicalID int64, modality string) int64 {
	return canonicalID*100 + modalityOrdinal(modality)
}

// modalityOrdinal 是 modality_verification_stale 这条检查里模态的稳定序号。
//
// 顺序必须与 827 的 v_model_modality_verification_progress 里
// `unnest(ARRAY['vision','audio','video'])` 一致 —— 那份数组是判据报告的分组
// 依据，两边不一致会让「行数对得上但分组对不上」。
func modalityOrdinal(modality string) int64 {
	switch modality {
	case "vision":
		return 1
	case "audio":
		return 2
	case "video":
		return 3
	default:
		return 0
	}
}

func autoFixCanonicalID(ctx context.Context, db *pgxpool.Pool, now time.Time) (int, error) {
	// Migration 693: same guard as the canonical_id_null check query — the
	// marker (canonical_cleared_at) records an operator unbind, and this
	// exact-match auto-fix must never resurrect it.
	tag, err := db.Exec(ctx, `
		UPDATE provider_models pm
		SET canonical_id = mc.id
		FROM models_canonical mc
		WHERE pm.canonical_id IS NULL
		  AND pm.canonical_cleared_at IS NULL
		  AND pm.raw_model_name = mc.canonical_name`)
	if err != nil {
		return 0, err
	}
	applied := int(tag.RowsAffected())
	if applied > 0 {
		db.Exec(ctx, `
			UPDATE routing_health_checks
			SET status = 'auto_fixed', auto_fixed_at = $1, auto_fix_result = 'applied', updated_at = $1
			WHERE check_id = 'canonical_id_null' AND status = 'open'`, now)
	}
	return applied, nil
}
