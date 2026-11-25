package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ─────────────────────────────────────────────────────────────────────────────
// 2026-11-20 节点探测能力轮 —— 配额族裁决在探针侧的识别与落账
//
// 生产现场（211，2026-11-20 09:44 实测）：
//
//	apiclaude.cc 对 claude-sonnet-5-5 回 HTTP 403
//	{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}
//
// 同一个上游裁决，两个面给出**矛盾**的答案：
//
//	★ 业务面：errorsx.ClassifyErrorWithBody ⇒ KindQuotaPermanent
//	  ⇒ 凭据级落账（quota_state=permanently_exhausted +
//	    availability_state=suspended）⇒ UI 显示「配额耗尽 / 已停用」；
//	★ 探针面（修复前）：只看 err code（"http_403"）⇒ 无策略
//	  ⇒ 凭据面**一个字不写**（凭据仍显示 ready / ok ⇒ 「节点正常」）
//	  ⇒ 只有绑定面落账 ⇒ **模型不可路由**。
//
// ⇒ 用户侧形态：「节点正常」+「模型访问不了」= 状态不匹配。
//
// 本文件钉死三件事：
//	① 探针的分类必须**看响应体**（和业务面同一把尺子）；
//	② 配额族裁决必须**凭据级落账**（两面同词表）；
//	③ 绑定面的冷却时长必须与**探针自己的下一跳同源**（不许再写 5 分钟假账）。
// ─────────────────────────────────────────────────────────────────────────────

// quotaFakeExec records the SQL the worker writes through the Exec seam.
type quotaFakeExec struct {
	sqls []string
	args [][]any
	fail bool
}

func (f *quotaFakeExec) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	f.sqls = append(f.sqls, sql)
	f.args = append(f.args, arguments)
	if f.fail {
		return pgconn.CommandTag{}, context.DeadlineExceeded
	}
	return pgconn.CommandTag{}, nil
}

func (f *quotaFakeExec) last() string {
	if len(f.sqls) == 0 {
		return ""
	}
	return f.sqls[len(f.sqls)-1]
}

// ── ① 分类：探针必须看上游响应体 ─────────────────────────────────────────────

func TestClassifyProbeOutcomeKindQuotaVerdict(t *testing.T) {
	cases := []struct {
		name       string
		errCode    string
		httpStatus int
		body       string
		want       errorsx.ErrorKind
	}{
		{
			name:       "403 INSUFFICIENT_BALANCE ⇒ 配额族（生产形态）",
			errCode:    "http_403",
			httpStatus: 403,
			body:       `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
			want:       errorsx.KindQuotaPermanent,
		},
		{
			name:       "403 error.message 形态的余额耗尽 ⇒ 配额族",
			errCode:    "http_403",
			httpStatus: 403,
			body:       `{"error":{"message":"insufficient balance for model claude-sonnet-5-5"}}`,
			want:       errorsx.KindQuotaPermanent,
		},
		{
			name:       "402 payment required ⇒ 配额族",
			errCode:    "http_402",
			httpStatus: 402,
			body:       `{"error":{"message":"quota exceeded"}}`,
			want:       errorsx.KindQuotaPermanent,
		},
		{
			name:       "429 配额体 ⇒ 配额族",
			errCode:    "http_429",
			httpStatus: 429,
			body:       `{"error":{"message":"usage limit exceeded"}}`,
			want:       errorsx.KindQuotaPermanent,
		},
		{
			// ★ 边界一之一：429 的「限流形态」正文（usage limit reached，不是
			// exceeded）errorsx 判 rate_limit ⇒ **不是配额族** ⇒ 探针保持
			// 今天的限流族策略（窗口对齐冷却），不被配额停放 horizon 接管。
			name:       "429 限流形态 ⇒ 仍是限流族",
			errCode:    "http_429",
			httpStatus: 429,
			body:       `{"error":{"message":"usage limit reached"}}`,
			want:       errorsx.KindRateLimit,
		},
		{
			// ★ 边界一：纯鉴权 403（没有配额体）**不是**配额族 ⇒ 探针保持今天
			// 的行为（不写凭据配额面）。鉴权族要单独设计，不在本轮扩大。
			name:       "403 无配额体 ⇒ 不是配额族",
			errCode:    "http_403",
			httpStatus: 403,
			body:       `{"error":{"message":"forbidden"}}`,
			want:       "",
		},
		{
			name:       "403 空响应体 ⇒ 不是配额族",
			errCode:    "http_403",
			httpStatus: 403,
			body:       "",
			want:       "",
		},
		{
			name:       "401 ⇒ 不是配额族",
			errCode:    "http_401",
			httpStatus: 401,
			body:       `{"error":{"message":"invalid key"}}`,
			want:       "",
		},
		{
			name:       "500 ⇒ 不是配额族（沿用上游故障族）",
			errCode:    "http_500",
			httpStatus: 500,
			body:       `{"error":{"message":"boom"}}`,
			want:       errorsx.KindUpstreamDown,
		},
		{
			// ★ 边界二：既有的 err code 词表优先级不变（配额 code 直接命中）。
			name:       "err code 已是配额族 ⇒ 原样保留",
			errCode:    "quota_periodic",
			httpStatus: 429,
			body:       "",
			want:       errorsx.KindQuotaPeriodic,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyProbeOutcomeKind(tc.errCode, tc.httpStatus, tc.body)
			if got != tc.want {
				t.Fatalf("classifyProbeOutcomeKind(%q, %d, %q) = %q, want %q",
					tc.errCode, tc.httpStatus, tc.body, got, tc.want)
			}
		})
	}
}

// TestProbeOutcomeKindUsesRoundEvidence pins that the round-level helper
// reads the direct round's own evidence (status + body), not just the code.
func TestProbeOutcomeKindUsesRoundEvidence(t *testing.T) {
	r := nodeProbeRoundResult{
		ok:           false,
		httpStatus:   403,
		errCode:      "http_403",
		responseBody: `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
	}
	if got := probeOutcomeKind(r); got != errorsx.KindQuotaPermanent {
		t.Fatalf("probeOutcomeKind(round) = %q, want %q", got, errorsx.KindQuotaPermanent)
	}

	ok := nodeProbeRoundResult{ok: true, httpStatus: 200}
	if got := probeOutcomeKind(ok); got != "" {
		t.Fatalf("probeOutcomeKind(success round) = %q, want empty", got)
	}
}

// ── ② 落账：配额族必须凭据级落账，且与业务面同词表 ─────────────────────────

func TestQuotaCredentialSQLVocabulary(t *testing.T) {
	t.Run("permanent ⇒ permanently_exhausted + suspended", func(t *testing.T) {
		sql := quotaCredentialSQL(errorsx.KindQuotaPermanent)
		for _, want := range []string{
			"quota_state             = 'permanently_exhausted'",
			"quota_recover_at        = NULL",
			"availability_state      = 'suspended'",
			"availability_recover_at = NULL",
			"state_reason_code       = $1",
			"state_reason_detail     = $2",
		} {
			if !strings.Contains(sql, want) {
				t.Fatalf("quotaCredentialSQL(KindQuotaPermanent) missing %q:\n%s", want, sql)
			}
		}
	})
	t.Run("balance/generic quota ⇒ balance_exhausted", func(t *testing.T) {
		for _, kind := range []errorsx.ErrorKind{errorsx.KindQuotaBalance, errorsx.KindQuota} {
			sql := quotaCredentialSQL(kind)
			if !strings.Contains(sql, "quota_state             = 'balance_exhausted'") {
				t.Fatalf("quotaCredentialSQL(%q) must write balance_exhausted:\n%s", kind, sql)
			}
			if !strings.Contains(sql, "availability_state      = 'suspended'") {
				t.Fatalf("quotaCredentialSQL(%q) must suspend the credential:\n%s", kind, sql)
			}
		}
	})
	t.Run("periodic ⇒ periodic_exhausted + 重置点", func(t *testing.T) {
		sql := quotaCredentialSQL(errorsx.KindQuotaPeriodic)
		for _, want := range []string{
			"quota_state             = 'periodic_exhausted'",
			"quota_recover_at        = $4",
			"availability_state      = 'suspended'",
		} {
			if !strings.Contains(sql, want) {
				t.Fatalf("quotaCredentialSQL(KindQuotaPeriodic) missing %q:\n%s", want, sql)
			}
		}
	})
	t.Run("非配额族 ⇒ 不写（空 SQL）", func(t *testing.T) {
		for _, kind := range []errorsx.ErrorKind{
			errorsx.KindAuth, errorsx.KindTransient, errorsx.KindUpstreamDown,
			errorsx.KindRateLimit, errorsx.KindModelNotFound, "",
		} {
			if sql := quotaCredentialSQL(kind); sql != "" {
				t.Fatalf("quotaCredentialSQL(%q) must be empty (no credential write), got:\n%s", kind, sql)
			}
		}
	})
	t.Run("守卫：balance_floor 豁免 / 手工下线 / 非 active", func(t *testing.T) {
		sql := quotaCredentialSQL(errorsx.KindQuotaPermanent)
		for _, want := range []string{
			"AND lifecycle_status = 'active'",
			"AND COALESCE(manual_disabled, FALSE) = FALSE",
			"AND COALESCE(state_reason_code, '') <> 'balance_floor'",
		} {
			if !strings.Contains(sql, want) {
				t.Fatalf("quotaCredentialSQL must keep guard %q:\n%s", want, sql)
			}
		}
	})
	// ★ 2026-11-20 批判式审计轮：严重度棘轮必须与业务面 writer 逐字同款 ——
	//   较轻的配额裁决不降级较重的既有落账。缺它时，同一凭据不同模型的
	//   探针裁决会互相覆盖（permanent 被迟到一步的 balance/periodic 体
	//   翻回较轻态），与「两面同一把尺」的原则自相矛盾。
	t.Run("严重度棘轮：较轻裁决不降级较重落账", func(t *testing.T) {
		sql := quotaCredentialSQL(errorsx.KindQuotaBalance)
		if !strings.Contains(sql, "AND quota_state NOT IN ('permanently_exhausted')") {
			t.Fatalf("quotaCredentialSQL(KindQuotaBalance) must keep the severity ratchet (NOT IN permanently_exhausted):\n%s", sql)
		}
		for _, kind := range []errorsx.ErrorKind{errorsx.KindQuotaBalance, errorsx.KindQuota} {
			if s := quotaCredentialSQL(kind); !strings.Contains(s, "AND quota_state NOT IN ('permanently_exhausted')") {
				t.Fatalf("quotaCredentialSQL(%q) must keep the severity ratchet:\n%s", kind, s)
			}
		}
		sqlP := quotaCredentialSQL(errorsx.KindQuotaPeriodic)
		if !strings.Contains(sqlP, "AND quota_state NOT IN ('balance_exhausted', 'permanently_exhausted')") {
			t.Fatalf("quotaCredentialSQL(KindQuotaPeriodic) must keep the severity ratchet (NOT IN balance/permanent):\n%s", sqlP)
		}
		// 与业务面 writer 的守卫逐字对表（domains/credential/writer.go）。
		wsrc := readWriterSource(t)
		if !strings.Contains(wsrc, "AND quota_state NOT IN ('permanently_exhausted')") {
			t.Fatalf("business writer's balance arm lost its severity ratchet — probe side and business side must stay in lockstep")
		}
		if !strings.Contains(wsrc, "AND quota_state NOT IN ('balance_exhausted', 'permanently_exhausted')") {
			t.Fatalf("business writer's periodic arm lost its severity ratchet — probe side and business side must stay in lockstep")
		}
	})
}

// TestUpdateCredentialQuotaSurfaceWritesThroughExec pins that the probe
// actually writes the credential surface (and only for quota kinds).
func TestUpdateCredentialQuotaSurfaceWritesThroughExec(t *testing.T) {
	t.Run("配额族 ⇒ 写凭据面", func(t *testing.T) {
		fake := &quotaFakeExec{}
		w := &NodeProbeWorker{stateExecDB: fake}
		w.updateCredentialQuotaSurface(context.Background(), 56, errorsx.KindQuotaPermanent, "upstream 403 INSUFFICIENT_BALANCE")
		if len(fake.sqls) != 1 {
			t.Fatalf("expected exactly one credential-surface write, got %d", len(fake.sqls))
		}
		if !strings.Contains(fake.last(), "permanently_exhausted") {
			t.Fatalf("credential-surface write must carry the quota vocabulary:\n%s", fake.last())
		}
		if len(fake.args[0]) != 3 {
			t.Fatalf("expected (kind, detail, credential_id) args, got %d", len(fake.args[0]))
		}
		if fake.args[0][0] != string(errorsx.KindQuotaPermanent) {
			t.Fatalf("arg0 = %v, want %q", fake.args[0][0], errorsx.KindQuotaPermanent)
		}
	})
	t.Run("非配额族 ⇒ 一行都不写", func(t *testing.T) {
		fake := &quotaFakeExec{}
		w := &NodeProbeWorker{stateExecDB: fake}
		w.updateCredentialQuotaSurface(context.Background(), 56, errorsx.KindAuth, "401")
		w.updateCredentialQuotaSurface(context.Background(), 56, "", "unknown")
		if len(fake.sqls) != 0 {
			t.Fatalf("non-quota kinds must not touch the credential surface, got %d writes", len(fake.sqls))
		}
	})
	t.Run("没有 exec seam ⇒ 静默跳过", func(t *testing.T) {
		w := &NodeProbeWorker{}
		w.updateCredentialQuotaSurface(context.Background(), 56, errorsx.KindQuotaPermanent, "x")
		// 无 panic 即通过（生产路径走 db；测试路径走 stateExecDB）。
	})
}

// ── ③ 时钟统一：绑定面冷却时长 == 探针下一跳 ─────────────────────────────────

// TestQuotaBindingHorizonMatchesProbeBackoff is the core anti-drift pin:
// the binding's advertised recovery time and the probe's own next retry must
// come from ONE source. Before the fix the binding said "5 minutes" while the
// probe row sat 3 hours out — and the expiry scanner defers to the probe row,
// so nothing actually re-checked the pair for hours.
func TestQuotaBindingHorizonMatchesProbeBackoff(t *testing.T) {
	round := nodeProbeRoundResult{
		ok:           false,
		httpStatus:   403,
		errCode:      "http_403",
		errDetail:    `upstream returned HTTP 403: {"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
		responseBody: `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
	}
	verdict, isQuota := probeQuotaVerdict(round)
	if !isQuota {
		t.Fatalf("403 INSUFFICIENT_BALANCE must be recognised as a quota verdict")
	}
	if !isQuotaProbeKind(verdict.kind) {
		t.Fatalf("verdict kind %q must belong to the quota family", verdict.kind)
	}

	_, bindingHorizon := unavailableBindingHorizonForKind("http_403", verdict, 5)
	probeHorizon := probeBackoffForDirectOutcomeKind(verdict, "http_403", ProbeRootCauseNode, 5)
	if bindingHorizon != probeHorizon {
		t.Fatalf("binding cooldown (%s) and probe next retry (%s) must be the same horizon — "+
			"two independent clocks is exactly the update-delay defect", bindingHorizon, probeHorizon)
	}
	if bindingHorizon < time.Hour {
		t.Fatalf("a quota verdict must not be re-probed on a short cadence, got %s", bindingHorizon)
	}
}

// TestQuotaVerdictHorizonCoversPeriodicReset pins that the periodic family
// (a window that DOES reset) parks at the reset point — the same number the
// quota-reset pin (quotaResetAlignedBackoff) produces — so the binding
// cooldown and the probe's next retry still agree.
func TestQuotaVerdictHorizonCoversPeriodicReset(t *testing.T) {
	round := nodeProbeRoundResult{
		ok:           false,
		httpStatus:   429,
		errCode:      "http_429",
		errDetail:    `upstream returned HTTP 429: {"error":{"message":"usage limit exceeded, your quota will reset at 2026-12-01 00:00:00"}}`,
		responseBody: `{"error":{"message":"usage limit exceeded, your quota will reset at 2026-12-01 00:00:00"}}`,
	}
	verdict, isQuota := probeQuotaVerdict(round)
	if !isQuota {
		t.Fatalf("429 quota body must be recognised as a quota verdict")
	}
	if verdict.kind != errorsx.KindQuotaPeriodic {
		t.Fatalf("verdict kind = %q, want %q", verdict.kind, errorsx.KindQuotaPeriodic)
	}
	if verdict.recoverAt == nil {
		t.Fatalf("the periodic family must carry a reset point")
	}
	if verdict.horizon() <= 0 {
		t.Fatalf("periodic horizon must be positive, got %s", verdict.horizon())
	}
	_, bindingHorizon := unavailableBindingHorizonForKind("http_429", verdict, 5)
	probeHorizon := probeBackoffForDirectOutcomeKind(verdict, "http_429", ProbeRootCauseNode, 5)
	// ★ 2026-11-25 第 28 轮：horizon() 每次调用各自取一次 time.Now()（time.Until 到
	//   重置点）⇒ 两次调用的结果可以相差几个纳秒（实测 30 次里 2 次恰好差 1ns）⇒
	//   严格相等是偶发假红。本门钉的是「两处同源」而不是「同一纳秒」：容许 1ms 以内
	//   的取时抖动（时长本身是小时量级 ⇒ 秒级以上的真实分歧照样被抓）。
	if skew := bindingHorizon - probeHorizon; skew > time.Millisecond || skew < -time.Millisecond {
		t.Fatalf("periodic: binding cooldown (%s) and probe next retry (%s) must agree",
			bindingHorizon, probeHorizon)
	}
}

// TestNonQuotaBindingHorizonUnchanged pins that the fix does not move the
// horizon for every other verdict family.
func TestNonQuotaBindingHorizonUnchanged(t *testing.T) {
	_, legacy := unavailableBindingHorizon("http_500", 1)
	_, kindAware := unavailableBindingHorizonForKind("http_500", quotaVerdict{}, 1)
	if legacy != kindAware {
		t.Fatalf("non-quota horizon changed: legacy=%s kind-aware=%s", legacy, kindAware)
	}
	_, legacy404 := unavailableBindingHorizon("http_404", 3)
	_, kindAware404 := unavailableBindingHorizonForKind("http_404", quotaVerdict{}, 3)
	if legacy404 != kindAware404 {
		t.Fatalf("404 horizon changed: legacy=%s kind-aware=%s", legacy404, kindAware404)
	}
}

// TestQuotaVerdictDoesNotUseGenericLadder pins that a quota verdict parks
// instead of walking the generic failure ladder (211: cred 56's 20 model
// pairs each burned 175~489 consecutive failures on a balance-exhausted node).
func TestQuotaVerdictDoesNotUseGenericLadder(t *testing.T) {
	verdict := quotaVerdict{kind: errorsx.KindQuotaPermanent}
	parked := probeBackoffForDirectOutcomeKind(verdict, "http_403", ProbeRootCauseNode, 1)
	generic := probeBackoffForDirectOutcome("http_403", ProbeRootCauseNode, 1)
	if parked == generic {
		t.Fatalf("quota verdict must not reuse the generic ladder delay (%s)", generic)
	}
	if parked != quotaExhaustedRecheck() {
		t.Fatalf("quota verdict must park at the quota horizon, got %s want %s",
			parked, quotaExhaustedRecheck())
	}
	// 非配额族行为不变。
	if got := probeBackoffForDirectOutcomeKind(quotaVerdict{}, "http_500", ProbeRootCauseNode, 1); got != probeBackoffForDirectOutcome("http_500", ProbeRootCauseNode, 1) {
		t.Fatalf("non-quota backoff changed: %s", got)
	}
}

// ── 接线钉：两条探针路径都必须落账，且必须等两次连续失败 ────────────────────

// TestProbePathsWriteCredentialQuotaSurface pins the wiring in BOTH probe
// paths (the legacy cycle() path and the unified queue path) — a fix that
// lands in only one of them is the classic "the production path is the other
// one" defect.
func TestProbePathsWriteCredentialQuotaSurface(t *testing.T) {
	for _, file := range []string{"node_probe.go", "probe_service.go"} {
		src := readBGSource(t, file)
		if !strings.Contains(src, "updateCredentialQuotaSurface") {
			t.Fatalf("%s: the probe path must write the credential-level quota surface", file)
		}
		if !strings.Contains(src, "isQuota && ") {
			t.Fatalf("%s: the credential-surface write must be guarded by the quota-verdict check", file)
		}
		if !strings.Contains(src, "attempt >= 2") {
			t.Fatalf("%s: the credential-surface write must require two consecutive quota verdicts", file)
		}
	}
}

// TestProbePathsUseKindAwareHorizon pins that both probe paths use the
// kind-aware binding horizon (one clock).
func TestProbePathsUseKindAwareHorizon(t *testing.T) {
	for _, file := range []string{"node_probe.go", "probe_service.go"} {
		src := readBGSource(t, file)
		if !strings.Contains(src, "unavailableBindingHorizonForKind(") {
			t.Fatalf("%s: the binding cooldown must use the kind-aware horizon", file)
		}
	}
}

// TestProbePathsUseKindAwareBackoff pins that both probe paths park a quota
// verdict instead of walking the generic ladder.
func TestProbePathsUseKindAwareBackoff(t *testing.T) {
	for _, file := range []string{"node_probe.go", "probe_service.go"} {
		src := readBGSource(t, file)
		if !strings.Contains(src, "probeBackoffForDirectOutcomeKind(") {
			t.Fatalf("%s: the probe backoff must be kind-aware (quota ⇒ park)", file)
		}
	}
}

// TestFeaturedMultiplierSkipsQuotaFamily pins that the featured-model
// fast-retry multiplier no longer shortens a quota verdict's park horizon —
// the multiplier exists to re-verify TRANSIENT failures faster.
func TestFeaturedMultiplierSkipsQuotaFamily(t *testing.T) {
	src := readBGSource(t, "probe_service.go")
	idx := strings.Index(src, "globalIsFeaturedModel(model, \"\")")
	if idx < 0 {
		t.Fatalf("could not locate the featured-model multiplier in probe_service.go")
	}
	end := strings.Index(src[idx:], "\n")
	line := src[idx : idx+end]
	if !strings.Contains(line, "!quotaFamily") {
		t.Fatalf("the featured-model multiplier must exclude the quota family, got: %s", line)
	}
}

// TestBindingUnavailableReasonCarriesQuotaAttribution pins the binding-side
// half of the fix: a quota verdict must write
//   - unavailable_reason = 'probe_quota_<kind>'  ⇒ 凭据级归因（与
//     auto_credential_% 同族）⇒ credentialhealth 的 F4 守卫认得它 ⇒ 凭据
//     没回 ready 时不许把绑定翻绿；**并且**
//   - unavailable_recover_at = 与探针下一跳同源的 horizon（不再恒 5 分钟）。
func TestBindingUnavailableReasonCarriesQuotaAttribution(t *testing.T) {
	src := readBGSource(t, "node_probe.go")
	body := sourceBetween(t, src,
		"func (w *NodeProbeWorker) updateBindingAvailability(",
		"func (w *NodeProbeWorker) updateCredentialHealth")

	for _, want := range []string{
		// 通用 horizon 仍在（非配额族行为逐字不变）。
		"reasonTag, horizon := unavailableBindingHorizon(reason, attempt)",
		// 配额族覆盖它。
		"if isQuotaProbeKind(verdict.kind) {",
		"reasonTag, horizon = string(verdict.kind), verdict.horizon()",
		// 绑定理由带 probe_ 前缀 ⇒ probe_quota_<kind>。
		`"probe_"+reasonTag`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("updateBindingAvailability must contain %q:\n%s", want, body)
		}
	}
}

// TestQuotaVerdictReasonIsCredentialAttribution pins the reason vocabulary:
// the binding reason must be probe_quota_<kind> so the credential-level
// recovery guard (credentialhealth cmbExpiredRecoverySQL) recognises it as a
// credential-level attribution.
func TestQuotaVerdictReasonIsCredentialAttribution(t *testing.T) {
	verdict := quotaVerdict{kind: errorsx.KindQuotaPermanent}
	reasonTag, _ := unavailableBindingHorizonForKind("http_403", verdict, 3)
	if reasonTag != string(errorsx.KindQuotaPermanent) {
		t.Fatalf("quota binding reason tag = %q, want %q", reasonTag, errorsx.KindQuotaPermanent)
	}
	if got := "probe_" + reasonTag; !strings.HasPrefix(got, "probe_quota_") {
		t.Fatalf("quota binding reason = %q, want the probe_quota_ family", got)
	}
	// 该族必须被 credentialhealth 的 F4 守卫覆盖（凭据级归因）。
	guard := readCredentialHealthSource(t)
	if !strings.Contains(guard, "NOT LIKE 'probe_quota%'") {
		t.Fatalf("credentialhealth's recovery guard must recognise probe_quota as credential-level attribution")
	}
}

// readCredentialHealthSource reads the credentialhealth checker source from the
// sibling package directory (relative to the bg package).
func readCredentialHealthSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../credentialhealth/checker.go")
	if err != nil {
		t.Fatalf("read credentialhealth/checker.go: %v", err)
	}
	return string(b)
}

// readWriterSource reads the business-side quota writer source — the probe
// side's SQL vocabulary must stay in lockstep with it (severity ratchets and
// all).
func readWriterSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../domains/credential/writer.go")
	if err != nil {
		t.Fatalf("read domains/credential/writer.go: %v", err)
	}
	return string(b)
}

// readBGSource 是本包已有的同目录源文件读取助手（见 modality_verification_test.go）。
