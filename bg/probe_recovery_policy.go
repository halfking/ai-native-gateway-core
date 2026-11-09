package bg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// NetworkProbeBackoffChain is the short retry ladder for transient, timeout,
// and network-class failures. The long 1h/2h/6h tail left those nodes red
// long after the upstream recovered.
var NetworkProbeBackoffChain = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// probeRecoveryMaxBackoff is the operator SLA for discovering that a
// previously-failing supplier/node is healthy again. Non-fixed recoverable
// ladders must not schedule further out than this (default 5 minutes).
// Permanent parks (model-not-served / protocol / auth) bypass via their
// own early-return horizons and are unaffected.
func probeRecoveryMaxBackoff() time.Duration {
	sec := settings.GetPlatformInt("probe.recovery_max_backoff_seconds", 300)
	if sec <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(sec) * time.Second
}

// ── 持续失败升级（2026-11-07 探测浪费根修轮）─────────────────────────────
//
// 背景（211 真库实测）：probe.recovery_max_backoff_seconds（默认 300s）把可
// 恢复族的退避长尾削平 ⇒ 持续失败的对稳定在 ~5 分钟一探、单对 ~480 次/天，
// consecutive_failures 累积到 ~1000 仍不停（spec 的 DescriptionLong 早已登记
// 这笔 72× 成本，但直到本轮才补上「持续失败」这一档）。
//
// 修法：attempt（跨代持续 = consecutive_failures+1）≥ 阈值（默认 21 ≈ 三代
// 完整梯零成功）且梯级本会被 SLA 削平时，复检间隔升到 sustained_failure_
// recheck_seconds（默认 1h）。「新失败 5 分钟内复检」的恢复 SLA 对阈值以下
// 的行逐字保留；恢复感知不降级 —— 配额/可用性恢复事件仍即时回拉
// （RearmCredentialFailures，见 rearm 的配额窗口守卫）、业务成功仍立即停放。
// 与 F4 配额钉同一哲学：盲探按证据定节奏，事件负责及时性。

// sustainedFailureAttemptThreshold is the cross-generation attempt count at
// which the recovery SLA stops compressing the ladder (2026-11-07). Default
// 21 = three full queue-task generations (nodeProbeMaxAttempts=7) with zero
// successes — well past "fresh failure", short of "nobody will ever look".
func sustainedFailureAttemptThreshold() int {
	n := settings.GetPlatformInt("probe.sustained_failure_attempt_threshold", 21)
	if n < 1 {
		return 21
	}
	return n
}

// sustainedFailureRecheck is the re-check interval applied to sustained-failure
// rows instead of the recovery SLA cap. Floored at probeRecoveryMaxBackoff so
// the sustained tier can never probe FASTER than the SLA tier.
func sustainedFailureRecheck() time.Duration {
	sec := settings.GetPlatformInt("probe.sustained_failure_recheck_seconds", 3600)
	d := time.Duration(sec) * time.Second
	if sec <= 0 {
		d = time.Hour
	}
	if cap := probeRecoveryMaxBackoff(); d < cap {
		return cap
	}
	return d
}

// capRecoverBackoffForAttempt applies the recovery-SLA cap with the
// sustained-failure escalation: below the threshold the cap is the operator
// SLA (unchanged behavior); at/above it the cap lifts to the sustained
// re-check interval. Callers hand in the already-computed ladder delay.
func capRecoverBackoffForAttempt(attempt int, d time.Duration) time.Duration {
	cap := probeRecoveryMaxBackoff()
	if attempt >= sustainedFailureAttemptThreshold() {
		cap = sustainedFailureRecheck()
	}
	if d > cap {
		return cap
	}
	return d
}

// SustainedFailureAttemptThreshold exposes the threshold for SQL-side guards
// (clamp exemption, rearm exhaustion guard) and tests.
func SustainedFailureAttemptThreshold() int {
	return sustainedFailureAttemptThreshold()
}

// SustainedFailureRecheck exposes the sustained tier's re-check interval for
// domain tests and operators' verification tooling.
func SustainedFailureRecheck() time.Duration {
	return sustainedFailureRecheck()
}

// ProbeBackoffForKind returns the next retry delay for an automatic probe.
// Quota uses the fixed policy cadence; transport errors stay on the short
// chain; everything else keeps the historical 7-step ladder, with the
// policy interval (3m rate-limit / 5m concurrent / 15m no-channel) acting
// as a floor so a 429 is never re-probed after 5s.
//
// 2026-09-26 (P0-2 短梯长尾化): the network-class short chain used to cap at
// 60s forever — a persistently-down upstream was re-probed once a minute
// indefinitely. Long-tail remains on, but 2026-11-05 caps the sink at
// probe.recovery_max_backoff_seconds (default 5m) so healthy recovery is
// discovered within the operator SLA instead of 1h/2h/6h.
func ProbeBackoffForKind(kind errorsx.ErrorKind, attempt int) time.Duration {
	policy := errorsx.AutomaticProbePolicyFor(kind)
	if policy.Fixed && policy.Interval > 0 {
		return policy.Interval
	}
	switch kind {
	case errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindUpstreamDown, errorsx.KindStreamTimeout,
		errorsx.KindUpstreamOverloaded, errorsx.KindEmptyResponse,
		errorsx.KindUpstreamContextLoss:
		// networkProbeBackoff already holds long-tail at recovery SLA (5m).
		return networkProbeBackoff(attempt)
	}
	delay := ChainBackoffIndex(attempt, NodeProbeBackoffChain)
	if policy.Enabled && policy.Interval > delay {
		delay = policy.Interval
	}
	// ★★ 2026-11-09（429 闪断族 · 探针对齐限流窗口）：rate 族探针的下一跳
	//   ★ 不再钉 3 分钟固定档（AutomaticProbePolicyFor 的 Interval），而是
	//   ★ 对齐业务侧 W1 节奏校准的窗口重灌估计（routing.rate.window_refill_
	//   ★ seconds，默认 60s）——「窗口耗尽=窗口重灌即恢复」。211 实测动因：
	//   ★ 紧 RPM 桶（kimi-k3/glm-5.x 族）业务 429 冷却 60s 后，复检探针要等
	//   ★ 3 分钟梯底才跑 ⇒ 绑定被钉在 unavailable 多 2 分钟（no_candidate
	//   ★ 放大）；且探针自身也消耗该桶的 RPM（§631「含失败次数」形态）。
	//   ★ 节奏校准关闭（window=0）⇒ 逐字节回 3m 固定档（A2 口径）。
	if kind == errorsx.KindRateLimit {
		if w := rateWindowRefill(); w > 0 {
			if delay < w {
				delay = w
			} else if delay == policy.Interval && policy.Interval > w {
				// 3m 固定档命中 ⇒ 用窗口档替换（更早复检；更高梯级不动——
				// 连击升级后的长梯仍由 attempt 推进，窗口只定「最早有用时刻」）。
				delay = w
			}
		}
	}
	// Rate-limit / concurrent are recoverable supplier-side pressures: cap at
	// the recovery SLA so a cleared 429/并发窗可在约 5 分钟内被复检。
	// Auth/unknown (empty kind) and KindNoAvailableChannel keep their longer
	// horizons — those are not "supplier already healthy" discovery paths.
	// 2026-11-07: the cap (both here and in networkProbeBackoff) routes
	// through capRecoverBackoffForAttempt so sustained failures escalate to
	// probe.sustained_failure_recheck_seconds instead of compressing to the
	// SLA forever.
	switch kind {
	case errorsx.KindRateLimit, errorsx.KindConcurrent:
		delay = capRecoverBackoffForAttempt(attempt, delay)
	}
	return delay
}

// rateWindowRefill mirrors domains/credential's W1 pace-calibration window
// estimate (settings.RateWindowRefillSeconds, default 60s; 0 = calibration
// off). bg reads it through settings so probe and business pacing share one
// operator knob. rateWindowRefillSecondsFn is the test seam over the settings
// getter (var, not const — same pattern as nodeProbeMirrorWriteRetryDelay).
var rateWindowRefillSecondsFn = settings.RateWindowRefillSeconds

func rateWindowRefill() time.Duration {
	sec := rateWindowRefillSecondsFn()
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// networkProbeBackoff paces the network/transient class. Long tail on:
// attempts 1..4 walk the short chain (5s/15s/30s/60s); attempts beyond it
// hold at the recovery SLA (default 5m) instead of sinking to 1h/2h/6h.
// Long tail off: the legacy short chain capped at 60s forever.
func networkProbeBackoff(attempt int) time.Duration {
	if settings.GetPlatformBool("probe.network_chain_long_tail", true) && attempt > len(NetworkProbeBackoffChain) {
		// ★★ 2026-11-05（12h 审计轮 · P-10）：★ 旋钮的语义是【上限】，★ 不是【替换】。
		//   ★ 长尾仍是**渐进**的 5m→1h→2h→6h（★ 通用梯去掉自身 5s/30s/60s 三级），
		//   ★ 只是每一级都被 probe.recovery_max_backoff_seconds 削到上限。
		//   ★ ⇒ 默认 300s ⇒ 长尾恒为 5m（★ 与 2026-11-05 口径逐字一致）；
		//   ★ ⇒ 旋钮调到 21600（6h）⇒ ★ **长尾恢复成 2026-09-25 P0-2 的原梯**（5m→1h→2h→6h）
		//   ★   ⇒ ⇒ ★ 旧口径【纯配置可回退】，不需要改代码、不需要回滚发布。
		// ★★ 2026-11-07（持续失败升级）：削顶改走 capRecoverBackoffForAttempt ——
		//   ★ attempt ≥ sustained_failure_attempt_threshold 的行不再被削到 5m，
		//   ★ 而是升到 sustained_failure_recheck_seconds（默认 1h）。
		//   ★ 阈值以下（新失败/中低失败）行为与 2026-11-05 口径逐字一致。
		tail := NodeProbeBackoffChain[3:]
		d := ChainBackoffIndex(attempt-len(NetworkProbeBackoffChain), tail)
		return capRecoverBackoffForAttempt(attempt, d)
	}
	return ChainBackoffIndex(attempt, NetworkProbeBackoffChain)
}

// ProbeBackoffForErrCode maps a probe/audit error code onto ProbeBackoffForKind.
func ProbeBackoffForErrCode(errCode string, attempt int) time.Duration {
	// 2026-09-17: a 404 the DIRECT round returned twice is the upstream's
	// "this credential does not serve this model" verdict. Re-probing it on
	// the ordinary ladder (5s..6h) made every request_failure trigger walk the
	// whole chain again — the eternal-churn shape seen on apigpt (14 models ×
	// 7 attempts/24h). Park the pair at the model-not-served re-check horizon
	// instead; the first (unconfirmed) 404 keeps the generic ladder so a
	// one-off aggregator blip still re-verifies quickly.
	if isModelNotServedProbeError(errCode) && attempt >= 2 {
		return modelNotServedRecheckInterval
	}
	return ProbeBackoffForKind(classifyProbeErrCode(errCode), attempt)
}

// probeBackoffForDirectOutcome is the root-cause-aware wrapper around
// ProbeBackoffForErrCode (2026-09-25, "错误的探测要搞清楚是协议问题还是节点的
// 问题"): a PROTOCOL-shaped failure is a probe-contract mismatch (wrong
// protocol path / body shape / catalog entry against this provider). Re-probing
// cannot heal it — only a config fix can — so from the second attempt on it
// parks at the same long horizon as model-not-served instead of walking the
// generic ladder. The first attempt keeps the generic ladder: a one-off
// aggregator blip can 400/422 once and must still re-verify quickly.
// Node- and gateway-caused failures keep the existing err-code policy
// (gateway-side is separately overridden to the fixed 15m delay by callers).
func probeBackoffForDirectOutcome(errCode string, rootCause ProbeRootCause, attempt int) time.Duration {
	if rootCause == ProbeRootCauseProtocol && attempt >= 2 {
		return modelNotServedRecheckInterval
	}
	return ProbeBackoffForErrCode(errCode, attempt)
}

func classifyProbeErrCode(code string) errorsx.ErrorKind {
	c := strings.ToLower(strings.TrimSpace(code))
	switch c {
	case "timeout", "stream_timeout":
		return errorsx.KindTimeout
	case "network_error", "network", "dns_error", "connection_error", "http_000":
		return errorsx.KindNetwork
	case "quota_periodic", "periodic_exhausted":
		return errorsx.KindQuotaPeriodic
	case "quota_balance", "balance_exhausted":
		return errorsx.KindQuotaBalance
	case "quota_permanent", "permanently_exhausted":
		return errorsx.KindQuotaPermanent
	case "quota":
		return errorsx.KindQuota
	case "rate_limit", "http_429":
		return errorsx.KindRateLimit
	case "upstream_down":
		return errorsx.KindUpstreamDown
	case "empty_response":
		return errorsx.KindEmptyResponse
	}
	if strings.HasPrefix(c, "http_5") {
		return errorsx.KindUpstreamDown
	}
	if strings.HasPrefix(c, "http_0") {
		return errorsx.KindNetwork
	}
	// Unknown / auth / pin-unsupported codes: no automatic policy, plain
	// generic ladder. Returning an empty kind keeps the caller honest — it
	// is not "auth", we simply have no transport evidence.
	return ""
}

// ----------------------------------------------------------------------------
// 配额重置点对齐退避（2026-11-07 探测时间点规划轮）
//
// 问题：配额用尽（周/月窗口、智谱 1310 族）的节点级探测失败按 rate_limit
// 族梯子每 3~5 分钟复检一次，而重置点是**已知的**（failure monitor 在配额
// 分类时写下 quota_recover_at）。对一个 7 天窗口的用尽凭据，这是 ~2000 次
// 注定 429 的探测请求/对 —— 在「1 分钟最多 30 次含失败次数」这类上游
// （凭据 29 形态），探测流量直接挤占业务配额；且与凭据级 BalanceQuotaProbe
// （2min tick + 2^n due 闸封顶 1h）双流并行，节点级行是其中更密的那条。
//
// 修法：失败体分类为配额族且凭据带**未来**的 quota_recover_at 时，把
// next_retry_at 钉在 recover_at + 60s（给上游时钟一点余量），用尽期内不再
// 打节点级探测。恢复检测不降级：凭据级探测（BalanceQuotaProbe/v2）照旧
// 负责「充值/提前重置」的发现，其成功事件触发
// NodeProbeWorker.RearmCredentialFailures 把该凭据所有失败行拉回 now()，
// 泵 30s 内接管验证。recover_at 缺失/已过期/过期在即 ⇒ 维持原梯子。
// ----------------------------------------------------------------------------

// quotaResetPinAfter is the grace delta past a known quota_recover_at at
// which the pinned verification probe lands (upstream clocks lag their own
// announced reset boundary).
const quotaResetPinAfter = 60 * time.Second

// quotaProbeHTTPStatuses are the statuses upstreams use to carry quota
// exhaustion (payment required / forbidden-by-plan / rate-limit-with-quota-
// body). Anything else keeps the ordinary ladder without spending a
// classification regex on it.
var quotaProbeHTTPStatuses = map[int]bool{
	402: true,
	403: true,
	429: true,
}

// quotaResetPinDuration is the pure pin decision: land just past a future
// reset boundary; a zero/past/boundary-imminent recover_at (within the pin
// grace itself) is not pinnable — the ordinary ladder probes within minutes
// anyway, and it will likely succeed at the boundary.
func quotaResetPinDuration(recoverAt time.Time, now time.Time) (time.Duration, bool) {
	if recoverAt.IsZero() {
		return 0, false
	}
	if !recoverAt.After(now.Add(quotaResetPinAfter)) {
		return 0, false
	}
	return recoverAt.Add(quotaResetPinAfter).Sub(now), true
}

// probeQuotaPinDB is the minimal DB surface the quota pin needs.
type probeQuotaPinDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// quotaResetAlignedBackoff inspects a failed direct round and reports the
// reset-aligned next retry when the pin applies. Everything else returns
// ok=false and the caller keeps the ladder it already computed. Best-effort:
// any DB error also returns ok=false (the ladder is the safe default; the
// next failure retries the pin).
//
// ★ 2026-11-07（探测浪费根修轮）第二臂「凭据行判定」：原判定只认
// 「配额类状态码 + 配额类错误体」。实测（211，凭据 35）存在用尽凭据在用尽
// 窗口内返回 503 Service Unavailable 形态的失败 —— 不触发原臂 ⇒ 每 5 分钟
// 盲探一次直到重置点。第二臂不再看失败形态，直接看凭据行自身：quota_state
// ∈ 用尽族且 quota_recover_at 已知未来 ⇒ 钉 recover_at+60s（F4「用尽期 0
// 探测」对非配额形态失败的补全）。提前恢复仍由凭据级探测发现并事件回拉。
func quotaResetAlignedBackoff(ctx context.Context, db probeQuotaPinDB, credID int, round nodeProbeRoundResult) (time.Duration, bool) {
	if db == nil || credID <= 0 {
		return 0, false
	}
	// 臂 2 让**每一次**失败都要查一眼凭据行 ⇒ 调用方传入的 nil *pgxpool.Pool
	// 装进接口后 db==nil 拦不住（typed-nil），会在 QueryRow 上 SIGSEGV ——
	// 这里按具体类型显式拦（原臂 1 的状态门曾在调用方把这种情形挡在查询之前）。
	if p, ok := db.(*pgxpool.Pool); ok && p == nil {
		return 0, false
	}
	bodyIsQuota := false
	if quotaProbeHTTPStatuses[round.httpStatus] {
		kind := errorsx.ClassifyErrorWithBody(round.httpStatus, []byte(round.responseBody))
		switch kind {
		case errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent:
			bodyIsQuota = true
		}
	}
	var (
		quotaState string
		recoverAt  *time.Time
	)
	err := db.QueryRow(ctx,
		`SELECT quota_state, quota_recover_at FROM credentials WHERE id = $1`, credID).Scan(&quotaState, &recoverAt)
	if err != nil {
		return 0, false
	}
	// 臂 1（原判定）：配额体 + 未来 recover_at。
	if bodyIsQuota && recoverAt != nil {
		return quotaResetPinDuration(*recoverAt, time.Now())
	}
	// 臂 2（新）：凭据行自身处于带未来重置点的用尽窗口 —— 失败形态无关。
	switch quotaState {
	case "periodic_exhausted", "balance_exhausted", "permanently_exhausted":
	default:
		return 0, false
	}
	if recoverAt == nil {
		// balance/permanent 用尽不带重置点（恢复靠充值类事件）⇒ 无从钉，
		// 维持梯子；恢复事件仍会回拉。
		return 0, false
	}
	return quotaResetPinDuration(*recoverAt, time.Now())
}
