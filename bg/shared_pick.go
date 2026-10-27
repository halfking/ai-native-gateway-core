package bg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/dbrows"
)

// pickDB is the subset of *pgxpool.Pool that PickProbeModelForCredential needs.
// Declaring it as an interface lets us unit-test the picker with pgxmock
// while leaving the production call sites (which pass *pgxpool.Pool)
// unchanged — *pgxpool.Pool satisfies this interface via its embedded
// *pgxpool.ConnPool / pgxpool.Pool method set.
type pickDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PickProbeResult is the value type returned by PickProbeModelForCredential.
type PickProbeResult struct {
	Model  string
	Source string
}

// mostUsedClientModelSQL 返回「近 7 天最常用 client_model」查询的 SQL。
//
// ★ 为什么抽成函数（R170）：★ 判据必须落在【产物】上 —— SQL 拼在内联
// raw string 里时，守卫只能去扫源码片段（★ 那是 R168 记过的弱判据：
// 「Contains 在形状约束上永远是弱判据」）。抽出来后，守卫可以直接断言
// 这条 SQL 的**最终形态**：投影了哪些列、谓词引用了哪些列、别名是否一致。
//
// ★ alias 是**调用方给的**：探针排除谓词 ProbeTrafficExclusionPredicateView
// 是别名参数化（三处占位符）的 ⇒ 窄投影与视图读面共用同一条谓词 ⇒
// 谓词需要的三列（quality_flags / task_type / origin_actor）**必须**都在
// 窄投影里（少一列就是 42703 —— 仓内 R50 记录过同族事故）。
//
// ★★ **为什么用「视图变体」谓词而不是「会话族变体」谓词**（R170 定案）：
//
//	窄投影带 details 腿 ⇒ 有 `quality_flags`；但**没有** `origin_stage`
//	（origin_stage 不在会话族的 details 腿上）⇒ ⇒ 会话族谓词会 42703 ⇒
//	⇒ 只能选 quality_flags 变体。★ 这条不是「因为读的是视图」，
//	而是「因为这个读面有哪些列」—— 与 R49 自纠的判据同源。
func mostUsedClientModelSQL(alias string) string {
	return `
		SELECT client_model
		FROM ` + dbpkg.SessionFamilyMostUsedClientModelSourceSQL() + ` ` + alias + `
		WHERE ` + alias + `.credential_id = $1
		  AND ` + alias + `.ts > now() - interval '7 days'
		  AND ` + alias + `.success = TRUE
		  -- R50 dual-arm exclusion (R49 自纠 S1：本读面有 quality_flags、无 origin_stage ⇒ 只能用 quality_flags 变体) — a model only probes ever
		  -- touched must not win "most-used" and steer the probe target.
		  AND ` + fmt.Sprintf(ProbeTrafficExclusionPredicateView, alias, alias, alias) + `
		  AND ` + alias + `.client_model IS NOT NULL
		GROUP BY ` + alias + `.client_model
		ORDER BY count(*) DESC
		LIMIT 1
	`
}

// PickProbeModelForCredential implements the 4-level fallback algorithm
// (manual > _request_logs > featured > random_fallback > empty).
// Skips credentials with source='manual' (already pinned by admin).
//
// Priority 2 "featured" — picks a binding model whose standardized_name
// (with raw_model_name fallback) appears in routing_policy.featured_models.
// We deliberately bias toward "hot / mainstream" models for the credential-level
// health probe because a single obscure / deprecated binding that happens to be
// available should not be allowed to flip the whole credential to unreachable.
// This matches the Layer 4 featuredCycle in model_probe.go so the two probe
// layers agree on what counts as a "featured" model.
//
// If no featured match exists, we fall back to the previous random pick
// over the credential's available bindings (source='auto:domestic_random')
// — that branch is the safety net for credentials whose model set is
// entirely non-featured (e.g. legacy / sandbox providers).
//
// Used by both:
//   - admin.ProviderDetailView.pickDefaultProbeModel (admin on-demand pick)
//   - bg.DefaultProbePicker.repickAll (daily 0:00 batch)
func PickProbeModelForCredential(ctx context.Context, db pickDB, credID int) (PickProbeResult, error) {
	var (
		currModel  string
		currSource string
		status     string
		lifecycle  string
		manual     bool
	)
	err := db.QueryRow(ctx, `
		SELECT COALESCE(default_probe_model, ''),
		       COALESCE(default_probe_model_source, ''),
		       status, lifecycle_status, COALESCE(manual_disabled, FALSE)
		FROM credentials WHERE id = $1
	`, credID).Scan(&currModel, &currSource, &status, &lifecycle, &manual)
	if err != nil {
		return PickProbeResult{}, err
	}

	if currSource == "manual" && currModel != "" {
		return PickProbeResult{Model: currModel, Source: "manual"}, nil
	}
	if status != "active" || lifecycle != "active" || manual {
		return PickProbeResult{}, nil
	}

	// Priority 1: most-used client_model in the session-turn family (7d).
	//
	// 2026-09-11: read the current-month surface — the bare parent only
	// holds cold rows, so the 7d window was blind to the most recent days
	// of traffic and picked a stale "most-used" model.
	//
	// ★ 2026-10-27（R170）：读面从 canonical 视图改为**窄投影两臂**
	// （db.SessionFamilyMostUsedClientModelSourceSQL，7 列）。
	//
	// ★★ **口径等价**（252 生产规模，同快照逐值对拍）：分组多重集**逐值相同**
	//   （9 组 / 14,317 行 / 双向差集 0/0 / top-1 相同）⇒ **不改行为**。
	//
	// ★★ **性能口径（R170 自己推翻了自己上一轮写的结论）**：
	//   ★ 「改窄投影省 12.6×」是**单次采样噪声** —— 同计划形状的九个执行在 252 上
	//     耗时 368 ms ~ 16,194 ms（**44×**）⇒ 单点读数没有分辨力。
	//   ★ 「视图 118 列 ⇒ 每行物化 118 列」**不成立** —— PostgreSQL 会内联视图
	//     并裁剪未引用列 ⇒ 计划代价两者几乎相同（2026.88 vs 2025.29）⇒
	//     **执行阶段没有可省的量**。
	//   ★ 真实且可复现的收益在**规划**：视图 Planning 25~91 ms vs 窄投影 8~13 ms
	//     （3~11×，CPU 成本，与 IO 噪声无关，每次调用都付）。
	//
	// ★ 而「最常用模型」这个功能**本来就是活的**（R169 一度误判为恒失效，
	//   真因是按列序下标读投影表 ⇒ 撞上 details 位翻转形态）⇒ 本次改写
	//   **不改变行为** ⇒ 不需要产品裁决。
	//
	// ★ 本次改写的**结构性**价值：对 canonical 视图的依赖被去掉（这条读点改读
	//   会话族原生源）⇒ 读点的形态从此由**仓内代码**钉住，而不是由一个
	//   可能漂移的数据库对象钉住（守卫见 bg/shared_pick_most_used_model_test.go）。
	var topModel string
	err = db.QueryRow(ctx, mostUsedClientModelSQL("n"), credID).Scan(&topModel)
	if err == nil && topModel != "" {
		if bindingAvailableForModel(ctx, db, credID, topModel) {
			return PickProbeResult{Model: topModel, Source: "auto:request_log"}, nil
		}
	}

	// Priority 2: featured match. routing_policy.featured_models holds
	// standardized model names (e.g. "claude-3-5-sonnet-20241022",
	// "gpt-4o"). We compare on standardized_name first (the canonical
	// representation); the OR with raw_model_name is a defensive
	// compatibility fallback for providers whose bindings predate
	// standardized_name backfill. Ordered by standardized_name so the
	// pick is stable across repickAll cycles — randomness was the
	// whole reason this priority existed in the first place, and
	// stability is desirable for audit-trace reasons.
	rows, qerr := db.Query(ctx, `
		SELECT COALESCE(pm.outbound_model_name, pm.raw_model_name) AS probe_model
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		CROSS JOIN routing_policy pol
		WHERE pol.tenant_id = 'default'
		  AND cmb.credential_id = $1
		  AND cmb.available = TRUE
		  AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
		  AND pm.available = TRUE
		  AND pm.unavailable_reason IS DISTINCT FROM 'manual'
		  AND (
		    pm.standardized_name = ANY(pol.featured_models)
		    OR pm.raw_model_name = ANY(pol.featured_models)
		  )
		ORDER BY COALESCE(pm.standardized_name, pm.raw_model_name)
		LIMIT 1
	`, credID)
	if qerr != nil {
		return PickProbeResult{}, qerr
	}
	defer rows.Close()
	if rows.Next() {
		var pick string
		if scanErr := rows.Scan(&pick); scanErr != nil {
			dbrows.WarnRowSkip("bg.PickProbeModelForCredential/featured", scanErr)
		} else if pick != "" {
			return PickProbeResult{Model: pick, Source: "auto:domestic_featured"}, nil
		}
	}
	// R66（category 5）：featured 读被中断后静默落到 Priority 3 随机兜底，
	// 等于把「读失败」翻译成「该 credential 没有常用模型」——探针模型选择
	// 因此走错分支。必须上抛。
	if err := rows.Err(); err != nil {
		return PickProbeResult{}, fmt.Errorf("bg.PickProbeModelForCredential/featured: iterate rows: %w", err)
	}

	// Priority 3: safety-net random pick across all available bindings.
	// Only reached when no featured model is bound to this credential.
	// Preserved from the original 4-level design so providers with no
	// featured bindings still get a sensible default_probe_model.
	rows, qerr = db.Query(ctx, `
		SELECT COALESCE(pm.outbound_model_name, pm.raw_model_name) AS probe_model
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1
		  AND cmb.available = TRUE
		  AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
		  AND pm.available = TRUE
		  AND pm.unavailable_reason IS DISTINCT FROM 'manual'
	`, credID)
	if qerr != nil {
		return PickProbeResult{}, qerr
	}
	defer rows.Close()
	var candidates []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			if dbrows.SkipOrFail("bg.PickProbeModelForCredential/randomFallback", err) {
				continue
			}
		}
		candidates = append(candidates, name)
	}
	// R66（category 5）：兜底候选集被截断会让随机抽样在残缺集合上做，
	// 选出的探针模型与「全部可用绑定」的真实意图不符。必须上抛。
	if err := rows.Err(); err != nil {
		return PickProbeResult{}, fmt.Errorf("bg.PickProbeModelForCredential/randomFallback: iterate rows: %w", err)
	}
	if len(candidates) > 0 {
		pick := candidates[time.Now().UnixNano()%int64(len(candidates))]
		return PickProbeResult{Model: pick, Source: "auto:domestic_random"}, nil
	}

	return PickProbeResult{}, nil
}

func bindingAvailableForModel(ctx context.Context, db pickDB, credID int, model string) bool {
	var ok bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM credential_model_bindings cmb
		    JOIN provider_models pm ON pm.id = cmb.provider_model_id
		    WHERE cmb.credential_id = $1
		      AND pm.raw_model_name = $2
		      AND cmb.available = TRUE
		      AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
		      AND pm.available = TRUE
		      AND pm.unavailable_reason IS DISTINCT FROM 'manual'
		)
	`, credID, model).Scan(&ok)
	return err == nil && ok
}
