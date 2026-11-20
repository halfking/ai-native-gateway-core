package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// healthyBindingSQL restores one (credential, model) binding after a direct
// probe or business success. It only touches rows that are currently
// unavailable and never overrides manual / admin-pinned bindings, so the
// per-request call from the executor is a 0-row no-op on a healthy node.
//
// ★ 2026-11-07（探测浪费根修轮 · R4 竞态收口）：新增**新鲜度守卫** —— 只恢复
// `unavailable_at <= 证据时刻` 的行。此前恢复是无条件的：探测直连轮在 T_c
// 成功、业务失败在 (T_c, 记账) 窗口内拉黑（unavailable_at 晚于探测证据），
// 无条件恢复会**覆盖更新的一条失败证据**；反过来记账（last_attempt_at）晚于
// 恢复时，审计 R4 的时序判据又会把这类行误判为 write-through 缺口。守卫后：
// 更新的失败证据存活（正确），配对的成功 mirror 把 last_attempt_at 写成证据
// 时刻（probe_service.go mirrorNodeProbeState），R4 只在真缺口时亮。
// evidenceAt 语义：证据的发生时刻（探测直连轮完成 / 业务成功返回），不是
// SQL 执行时刻。
func healthyBindingSQL(evidenceAt time.Time) string {
	return `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    probe_revert_at = NULL,
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.available, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_at, 'epoch') <= $3::timestamptz`
}

// healthyCredentialSQL flips the credential surfaces back to healthy/ready/ok.
// A real upstream success is newer evidence than any stored quota_state, so
// hard quota states are cleared too (same contract as writeHealth's 2026-08-07
// guard). The trailing predicate keeps the UPDATE a no-op when nothing needs
// to change — MarkNodeProbeHealthy runs on every successful request.
//
// 2026-09-13: rows pulled by bg/balance_floor_guard
// (state_reason_code='balance_floor') are exempt — they still have buffer
// quota by design, so an in-flight/stale-cache success here would instantly
// un-pull them and flap against the guard's next sweep. Recovery for those
// rows belongs exclusively to the guard's balance/plan re-checks.
func healthyCredentialSQL() string {
	return `
		UPDATE credentials
		SET health_status = 'healthy',
		    health_error = NULL,
		    health_checked_at = now(),
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    quota_state = 'ok',
		    quota_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = NULL,
		    state_updated_at = now()
		WHERE id = $1
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'
		  AND (
		      health_status IS DISTINCT FROM 'healthy'
		      OR availability_state IS DISTINCT FROM 'ready'
		      OR COALESCE(quota_state, 'ok') <> 'ok'
		      OR quota_recover_at IS NOT NULL
		      OR availability_recover_at IS NOT NULL
		      -- 2026-10-16: the three columns cleared above must also appear
		      -- here. Without them the "nothing needs to change" no-op guard
		      -- skips the whole UPDATE whenever the ONLY dirt is a stale
		      -- error string — so a credential already marked
		      -- healthy/ready/ok kept an old failure message forever.
		      -- Measured on 211: 18 credentials sat in exactly that shape
		      -- (health_status=healthy, availability_state=ready,
		      -- quota_state=ok, both recover_at NULL, health_error and/or
		      -- state_reason_detail non-NULL). That is the "node says 可用 but
		      -- the error line still shows an old 403" inconsistency.
		      --
		      -- A successful upstream call is newer evidence than any stored
		      -- error text, which is the same contract the function comment
		      -- already states for the quota columns — these are just the
		      -- diagnostic half of it.
		      OR health_error IS NOT NULL
		      OR state_reason_code IS NOT NULL
		      OR state_reason_detail IS NOT NULL
		  )`
}

func syncHealthyNodeSurfaces(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string) error {
	// 业务成功的证据时刻＝现在（调用点在成功返回路径上）。
	return syncHealthyNodeSurfacesAt(ctx, db, credentialID, rawModel, time.Now())
}

// syncHealthyNodeSurfacesAt is the evidence-timestamped core; the probe paths
// hand their direct-round completion time so a concurrent newer blacklist is
// never clobbered (see healthyBindingSQL).
func syncHealthyNodeSurfacesAt(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string, evidenceAt time.Time) error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec(ctx, healthyBindingSQL(evidenceAt), credentialID, rawModel, evidenceAt); err != nil {
		return err
	}
	_, err := db.Exec(ctx, healthyCredentialSQL(), credentialID)
	return err
}

// ── 探针的配额族凭据面落账（2026-11-20 节点探测能力轮）──────────────────────
//
// ★ 问题（211 生产实测）：apiclaude.cc 对 claude-sonnet-5-5 回
// HTTP 403 {"code":"INSUFFICIENT_BALANCE"}。业务面把它判成
// KindQuotaPermanent 并**凭据级落账**（quota_state=permanently_exhausted +
// availability_state=suspended）⇒ UI 显示「配额耗尽 / 已停用」；
// 而**探针面**此前只写**绑定面**（cmb.available=FALSE / probe_http_403）⇒
// **凭据面一个字不写** ⇒ 凭据仍显示 availability_state='ready' +
// quota_state='ok' ⇒ **「节点正常」**。
//
// ⇒ 两面矛盾 ⇒ 用户看到「节点正常但访问不了这个模型」⇒ **状态不匹配**。
//
// ★ 修法：探针也写凭据面，**词表与业务面逐字一致**（同一个 kind 字符串做
// state_reason_code、同一套 quota_state 取值、同一套 availability_state='suspended'）
// ⇒ 两面对同一个上游裁决给出同一个答案。
//
// ★ 守卫（为什么探针可以写共享面）：
//
//	① 只写**配额族**（errorsx.PenaltyOf 判它是凭据级判决）；其它族不碰。
//	② **两次连续**配额族探针失败才落账（调用方校验 attempt ≥ 2）⇒ 一次偶发
//	   403 不会把整个凭据摘掉（探针是合成流量，比业务流量更不该单次定罪）。
//	③ lifecycle_status='active' + manual_disabled=FALSE + balance_floor 豁免
//	   —— 与 healthyCredentialSQL / domains/credential/writer.go 同款守卫。
//
// ★ 幂等：同一 kind 重复落账是同值写（quota_state 已是目标值时再写一次也是
// 同值）⇒ 探针每轮失败都落账不会产生额外扰动。
//
// ★ 严重度棘轮（2026-11-20 批判式审计轮补齐）：balance/periodic 臂带与业务面
// writer 同款的 quota_state NOT IN (...) 守卫 —— permanent > balance > periodic，
// 较轻的裁决**不降级**较重的既有落账（业务面 writer 三臂的原文守卫）。缺它时
// 同一凭据不同模型的探针裁决会互相覆盖，且 periodic 会给 permanent 行写进
// 未来的 quota_recover_at（把「充值类事件恢复」偷换成「到点自动翻绿」）。
func quotaCredentialSQL(kind errorsx.ErrorKind) string {
	switch kind {
	case errorsx.KindQuotaPermanent:
		return `
		UPDATE credentials
		SET quota_state             = 'permanently_exhausted',
		    quota_recover_at        = NULL,
		    availability_state      = 'suspended',
		    availability_recover_at = NULL,
		    state_reason_code       = $1,
		    state_reason_detail     = $2,
		    state_updated_at        = now()
		WHERE id = $3
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  -- 2026-09-13 同款守卫：不夺走 balance_floor guard 摘出的行
		  -- （那批行的恢复归 BalanceQuotaProbe / BalanceQuotaProbe v2）。
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'`
	case errorsx.KindQuotaBalance, errorsx.KindQuota:
		return `
		UPDATE credentials
		SET quota_state             = 'balance_exhausted',
		    quota_recover_at        = NULL,
		    availability_state      = 'suspended',
		    availability_recover_at = NULL,
		    state_reason_code       = $1,
		    state_reason_detail     = $2,
		    state_updated_at        = now()
		WHERE id = $3
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'
		  -- ★ 2026-11-20 批判式审计轮：严重度棘轮（与业务面 writer 的
		  --   KindQuota/KindQuotaBalance 臂逐字同款）—— 已经 permanent 的凭据
		  --   不被较轻的 balance 裁决降级。缺这条时：模型 A 判 permanent、
		  --   模型 B 的探针迟一步回 balance 体 ⇒ permanent 被覆盖成
		  --   balance_exhausted ⇒ 「永久耗尽」凭据重新进入余额复检集合。
		  AND quota_state NOT IN ('permanently_exhausted')`
	case errorsx.KindQuotaPeriodic:
		// ★ 周期窗口族：重置点来自 errorsx.NextQuotaReset（**同一个** helpers
		//   业务面 classifyProbeFailure 用的就是它）⇒ 两个面算出的重置点一致。
		//   ★ 与业务面的差别：业务面用 inferQuotaRecoveryAtNow（detail + Retry-After
		//   + 窗口启发式），探针侧没有 Retry-After ⇒ 只用正文里的重置提示。
		return `
		UPDATE credentials
		SET quota_state             = 'periodic_exhausted',
		    quota_recover_at        = $4,
		    availability_state      = 'suspended',
		    availability_recover_at = $4,
		    state_reason_code       = $1,
		    state_reason_detail     = $2,
		    state_updated_at        = now()
		WHERE id = $3
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'
		  -- ★ 2026-11-20 批判式审计轮：严重度棘轮（与业务面 writer 的
		  --   KindQuotaPeriodic 臂逐字同款）—— 已经 balance/permanent 用尽的
		  --   凭据不被带重置点的 periodic 裁决降级。缺这条时：permanent 行
		  --   会被写进一个未来的 quota_recover_at ⇒ 恢复语义从「充值类事件」
		  --   被改成「到点自动翻绿」。
		  AND quota_state NOT IN ('balance_exhausted', 'permanently_exhausted')`
	}
	return ""
}

// updateCredentialQuotaSurface writes the credential-level quota verdict the
// node probe observed. It is a no-op for any kind outside the quota family.
//
// detail is the upstream error text; it is stored in state_reason_detail so the
// operator sees WHY the credential was suspended (same shape as the business
// writer's detail). It also carries the reset hint for the periodic family.
//
// The write goes through scheduleExec() — the worker's existing Exec seam —
// so tests can inject a fake exec instead of a real pool.
func (w *NodeProbeWorker) updateCredentialQuotaSurface(ctx context.Context, credID int, kind errorsx.ErrorKind, detail string) {
	if w == nil || credID <= 0 {
		return
	}
	exec := w.scheduleExec()
	if exec == nil {
		return
	}
	sql := quotaCredentialSQL(kind)
	if sql == "" {
		return
	}
	args := []any{string(kind), detail, credID}
	if kind == errorsx.KindQuotaPeriodic {
		// ★ 重置点用 errorsx.NextQuotaReset（业务面 classifyProbeFailure 用的
		//   同款 helper）⇒ 两个面算出的重置点一致 ⇒ 探针的配额重置点对齐退避
		//   （quotaResetAlignedBackoff）也能命中。
		args = append(args, errorsx.NextQuotaReset(detail, time.Now()))
	}
	if _, err := exec.Exec(ctx, sql, args...); err != nil {
		slog.Warn("node_probe_worker: credential quota surface write failed",
			"credential_id", credID, "kind", string(kind), "error", err)
	}
}

// ── 探针的鉴权族凭据面落账（2026-11-20 L1 收口）────────────────────────────
//
// ★ 问题：403 无配额体（纯鉴权 403）⇒ errorsx 判 KindAuth ⇒ 探针仍不写凭据面
//
//	⇒ 凭据面显示 ready/ok（「节点正常」）⇒ 与配额族同族形态未治。
//
// ★ 语义差异（鉴权 vs 配额）：
//
//	· 配额族：quota_state + availability_state='suspended' + 无 recover_at（permanent）
//	  或长 horizon（balance 6h）或重置点（periodic）⇒ 充值类事件恢复；
//	· 鉴权族：availability_state='auth_failed' + 15 分钟 recover_at（业务面
//	  writer.go:336-347）⇒ key 轮换 / 权限修复后可恢复，不是真正的"永久"。
//
// ★ 修法：探针侧接入 auth_failed 语义（与业务面同词表），15 分钟恢复窗口。
//
// ★ 守卫：同配额族——lifecycle_status='active' + manual_disabled=FALSE +
//
//	balance_floor 豁免 + 两次连续（attempt ≥ 2，调用方校验）。
//
// ★ 边界：只写 KindAuth / KindAuthRevoked；其它 kind（包括空 kind）不碰。
func authCredentialSQL(kind errorsx.ErrorKind) string {
	switch kind {
	case errorsx.KindAuth, errorsx.KindAuthRevoked:
		// ★ 与业务面 writer.go:338-347 同词表：availability_state='auth_failed' +
		//   15 分钟恢复窗口 + state_reason_code=kind + state_reason_detail=上游错误。
		// ★ 守卫：NOT IN ('suspended') 与业务面逐字一致（业务面 writer.go:346）
		//   —— auth_failed 不降级 suspended（配额/余额耗尽比鉴权失败更严重）。
		return `
		UPDATE credentials
		SET availability_state      = 'auth_failed',
		    availability_recover_at = now() + interval '15 minutes',
		    state_reason_code       = $1,
		    state_reason_detail     = $2,
		    state_updated_at        = now()
		WHERE id = $3
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'
		  AND availability_state NOT IN ('suspended')`
	}
	return ""
}

// updateCredentialAuthSurface writes the credential-level auth verdict the
// node probe observed. It is a no-op for any kind outside the auth family.
//
// The write goes through scheduleExec() so tests can inject a fake exec.
func (w *NodeProbeWorker) updateCredentialAuthSurface(ctx context.Context, credID int, kind errorsx.ErrorKind, detail string) {
	if w == nil || credID <= 0 {
		return
	}
	exec := w.scheduleExec()
	if exec == nil {
		return
	}
	sql := authCredentialSQL(kind)
	if sql == "" {
		return
	}
	args := []any{string(kind), detail, credID}
	if _, err := exec.Exec(ctx, sql, args...); err != nil {
		slog.Warn("node_probe_worker: credential auth surface write failed",
			"credential_id", credID, "kind", string(kind), "error", err)
	}
}
