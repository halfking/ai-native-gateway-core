// bg/modality_verification.go — 多模态能力分级核实的定时任务（迁移 825）
//
// 立项依据：现状的 Layer 2 探测（bg/model_probe.go verifyTargetModality）
// 有三处让它无法履行「定时核实未曾核实过的模型」这个职责：
//
//  1. **触发条件排除了 text**。verifyTargetModality 第一行就
//     `t.Modality == "text"` 时 return，所以当前被标成 text 的模型
//     永远不会被探。而 text→vision 的升级恰恰是规则表漏判时最需要的
//     一次发现——现状在设计上就看不见它。
//  2. **结论不落库**。成功与失败都只 slog.*，没有任何
//     INSERT/UPDATE。于是「这个模型核实过没有」在库里无法表达，
//     也就没有队列可选。
//  3. **把 200 当证据**。见 bg/modality_semantic_probe.go 文件头。
//
// 本任务按与 bg/capability_backfill.go 同构的运行骨架写（周期 + 到期
// 扫描 + 进程内退避台账 + 出网预算 + 蓝绿单跑 + kill switch），
// 因为它有同样的一条性质：**定期花真钱出网**。那套骨架里的每条纪律
// 在这里同样成立，尤其是「无证据不写」与「记账必须在真正出网点」。
//
// # 路由信任语义级
//
// 写回 models_canonical 的规则（见 rollupVerdict）：
//
//   - 升级（text → vision/audio/multimodal）**只认语义正证据**。结构级
//     「载得下」永远不提升路由信任——那正是今天把 200 当证据的缺陷。
//   - 降级**只认语义负证据**，且不覆盖 Layer 3 手工覆盖。
//   - 没有结论（unknown）不改任何东西。
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ModalityVerifyEnvKillSwitch 关闭本任务的唯一开关。运营方必须能不重新
// 发版就掐掉它——这是一个会自己花钱的周期任务。
const ModalityVerifyEnvKillSwitch = "LLM_GATEWAY_MODALITY_VERIFY"

const (
	// modalityVerifyBudgetEnv 覆盖每日出网探测预算。0 或负数 = 不设预算。
	modalityVerifyBudgetEnv = "LLM_GATEWAY_MODALITY_VERIFY_DAILY_BUDGET"

	modalityVerifyDefaultDailyBudget = 2000

	// modalityVerifyInterval 是扫描周期。到期判据是 staleAfter（30 天），
	// 所以 1h 一轮只是把「昨晚新进来的模型」尽快捡起来。
	modalityVerifyInterval = time.Hour

	// modalityVerifyStaleAfter 是重新核实一条已核实绑定的间隔。
	// 30 天：多模态能力很少变，但规则表与供应商会变，定期复核能抓到
	// 「以前能看现在不能」的上游阉割。
	modalityVerifyStaleAfter = 30 * 24 * time.Hour

	// modalityVerifyBatchLimit 限制单轮**出网探测**行数。
	modalityVerifyBatchLimit = 50

	// modalityVerifyScanFactor：单轮扫描窗口 = 探测预算 × 该倍数。
	// 窗口放大后由内存侧 attempt 台账（见 modalityVerifyAttemptBackoff）
	// 做反饥饿：被退避跳过的行不会让后面真正能探的行饿死。
	modalityVerifyScanFactor = 4

	// modalityVerifyProbeTimeout 限制单次探测的墙钟时间。一次探测最坏
	// 是「结构请求 + 挑战请求」两次出网，所以给到 2× 结构探针的预算。
	modalityVerifyProbeTimeout = 60 * time.Second

	// modalityVerifyAttemptBackoff 是同一条 (凭证, 模型, 模态) 两次探测
	// 之间的最小间隔。挑战带图，比普通 ping 贵，退避要比 capability_backfill
	// 更保守一些。
	modalityVerifyAttemptBackoff = 6 * time.Hour

	modalityVerifyAttemptLedgerMax = 16384

	// modalityVerifyDistLockTTL 是蓝绿单跑选举的持锁时长。周期 1h、
	// 最坏一轮 = batchLimit × 45s ≈ 37min，60min 覆盖得住。
	modalityVerifyDistLockTTL = 60 * time.Minute

	// modalityVerifyDailyBudgetWindow 是滚动窗口长度。滚动而非自然日。
	modalityVerifyDailyBudgetWindow = 24 * time.Hour

	// modalityVerifyStreakGoal 是定论所需的连续命中次数。
	//
	// 单次判据的假阳性率是 1/(8·7·6·5) = 1/1680 ≈ 0.06%。一个模型被
	// 永久标成多模态、或被永久降级成 text，都是不可逆的路由级后果，
	// 0.06% 不够。两胜 / 两负把误判率压到 ≈3.5e-7。
	modalityVerifyStreakGoal = 2
)

// modalityVerifyTarget 是一条待核实的绑定。
//
// 下面几个闸门字段刻意随行取回：SQL 里也筛了同样几条，但**那一份只是
// 少取行的预筛，正确性来源在 modalityVerifyAdmit 这个纯函数里**——
// 只写在 SQL 里的闸门一条也测不到，删掉也不会红（bg/capability_backfill.go
// 的同一条纪律）。
type modalityVerifyTarget struct {
	CredentialID   int
	CanonicalID    int64
	CanonicalName  string
	Ciphertext     []byte
	OutboundModel  string
	RawModel       string
	Modality       string
	BaseURL        string
	Protocol       string
	CatalogCode    string
	StoredModality string

	CredentialStatus   string
	LifecycleStatus    string
	CredentialDisabled bool
	ProviderEnabled    bool
	ProviderDisabled   bool
	BindingAvailable   bool
	ExistingCarry      string
	ExistingRead       string
	PosStreak          int
	NegStreak          int
}

// modalityVerifyAdmit 决定这条绑定该不该被探。纯函数，逐条闸门可测。
func modalityVerifyAdmit(t modalityVerifyTarget) (bool, string) {
	if !t.BindingAvailable {
		return false, "binding_unavailable"
	}
	if t.CredentialDisabled {
		return false, "credential_manual_disabled"
	}
	if t.ProviderDisabled {
		return false, "provider_manual_disabled"
	}
	if !t.ProviderEnabled {
		return false, "provider_disabled"
	}
	if !strings.EqualFold(strings.TrimSpace(t.LifecycleStatus), "active") {
		return false, "lifecycle_" + strings.ToLower(strings.TrimSpace(t.LifecycleStatus))
	}
	// 与 capabilityBackfillAdmit 同一份状态集合（active/cooling/degraded
	// 是「还在服务」的三态）。探一条已放弃的凭据既花钱又可能打在软删除
	// 记录上。
	if !probeableCredentialStatuses[strings.ToLower(strings.TrimSpace(t.CredentialStatus))] {
		return false, "credential_status_" + strings.ToLower(strings.TrimSpace(t.CredentialStatus))
	}
	// 模态必须已算出。算出失败的行是数据问题，不该拿一次出网去试。
	switch t.Modality {
	case "vision", "audio", "video":
		return true, ""
	case "":
		return false, "modality_unresolved"
	default:
		return false, "modality_" + t.Modality
	}
}

// ModalityVerification 定期核实多模态能力，把分级结论写回
// model_modality_verification 与 models_canonical。
//
// scan / persist / rollup / probe 四条是接缝：把它们参数化后，「拿到真实
// 上游帧之后到底写不写、写什么」这条决策可以在没有网关 schema 的环境里
// 测。probe 本身**不是**接缝——那条路径必须是真的，否则测的就是 mock。
type ModalityVerification struct {
	db       *pgxpool.Pool
	encKey   []byte
	keyring  *secret.Keyring
	interval time.Duration
	// staleAfter 不作为字段参与决策（到期判据在 SQL 里），保留它是为了
	// 让测试能覆盖 staleAfter 的行为。
	staleAfter time.Duration
	batchLimit int
	distLock   distlock.Manager

	attemptsMu sync.Mutex
	attempts   map[string]time.Time

	budgetMu sync.Mutex
	probes   []time.Time

	dailyBudget    int
	probeLedgerCap int

	probe   func(ctx context.Context, t modalityVerifyTarget, apiKey string) SemanticProbeResult
	scan    func(ctx context.Context) ([]modalityVerifyTarget, error)
	persist func(ctx context.Context, t modalityVerifyTarget, res SemanticProbeResult) error
	rollup  func(ctx context.Context, t modalityVerifyTarget) error
	// decryptFn 是接缝，缺省走 secret.DecryptAny。它必须在解密与出网之前
	// 才有意义：一条软删除的凭据不该被解密，更不该被发请求。
	decryptFn func(modalityVerifyTarget) (string, error)
}

// NewModalityVerification 构造核实任务。
func NewModalityVerification(db *pgxpool.Pool, encKey []byte, keyring *secret.Keyring) *ModalityVerification {
	budget := modalityVerifyDailyBudget()
	ledgerCap := capabilityBackfillProbeTimestampsMax
	if budget > 0 && budget*2 > ledgerCap {
		ledgerCap = budget * 2
	}
	return &ModalityVerification{
		db:             db,
		encKey:         encKey,
		keyring:        keyring,
		interval:       modalityVerifyInterval,
		staleAfter:     modalityVerifyStaleAfter,
		batchLimit:     modalityVerifyBatchLimit,
		dailyBudget:    budget,
		probeLedgerCap: ledgerCap,
		attempts:       make(map[string]time.Time),
		// 缺省探测走真实的 ProbeVisionSemantics：结构级 + 语义级两级
		// 都在里面。测试可以换掉这条接缝，但换掉之后测的不再是
		// 「真实上游帧 → 真实判级」这条链。
		probe: func(ctx context.Context, t modalityVerifyTarget, apiKey string) SemanticProbeResult {
			modelField := t.OutboundModel
			if modelField == "" {
				modelField = t.RawModel
			}
			return ProbeVisionSemantics(ctx, t.BaseURL, apiKey, modelField, t.Protocol)
		},
	}
}

// SetDistLock 接线蓝绿单跑选举。与 CapabilityBackfill.SetDistLock 同契约。
//
// 没有它，双实例各跑各的 ⇒ 语义核实的出网开销翻倍。这不是「浪费一点」：
// 挑战带图，是这批周期任务里单次最贵的一种。
func (m *ModalityVerification) SetDistLock(mgr distlock.Manager) { m.distLock = mgr }

// modalityVerifyEnabled 读 kill switch。
func modalityVerifyEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(ModalityVerifyEnvKillSwitch)))
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// Run 是周期循环。停机由 ctx 取消驱动，与其它 bg worker 一致。
func (m *ModalityVerification) Run(ctx context.Context) {
	if m == nil || m.db == nil {
		return
	}
	if !modalityVerifyEnabled() {
		slog.Info("modality_verification: disabled by env kill switch",
			"env", ModalityVerifyEnvKillSwitch)
		return
	}
	slog.Info("modality_verification: started",
		"interval", m.interval,
		"stale_after", m.staleAfter,
		"batch_limit", m.batchLimit,
		"daily_budget", m.dailyBudget)

	if _, err := m.VerifyOnce(ctx); err != nil {
		slog.Warn("modality_verification: startup cycle failed", "error", err)
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("modality_verification: stopped")
			return
		case <-ticker.C:
			if !modalityVerifyEnabled() {
				slog.Info("modality_verification: disabled mid-run by env kill switch",
					"env", ModalityVerifyEnvKillSwitch)
				continue
			}
			if _, err := m.VerifyOnce(ctx); err != nil {
				slog.Warn("modality_verification: cycle failed", "error", err)
			}
		}
	}
}

// VerifyOnce 跑一轮核实，返回写入了多少行证据。
func (m *ModalityVerification) VerifyOnce(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	if h := acquireSweepDistLock(ctx, m.distLock, "modality_verification",
		modalityVerifyDistLockTTL, "modality_verification"); h != nil {
		defer h.Release(context.WithoutCancel(ctx))
		if !h.IsLeader() {
			slog.Debug("modality_verification: follower instance skips cycle")
			return 0, nil
		}
	}
	scan := m.scan
	if scan == nil {
		if m.db == nil {
			return 0, nil
		}
		scan = m.dueTargets
	}
	targets, err := scan(ctx)
	if err != nil {
		return 0, err
	}

	written, probed, backedOff := 0, 0, 0
	budgetExhausted := false
	for _, t := range targets {
		if probed >= m.batchLimit {
			break
		}
		if m.attemptedRecently(t) {
			backedOff++
			continue
		}
		ok, didProbe, err := m.probeAndPersist(ctx, t)
		if err != nil {
			slog.Warn("modality_verification: target skipped",
				"credential_id", t.CredentialID,
				"raw_model", t.RawModel,
				"modality", t.Modality,
				"error", err)
			continue
		}
		if didProbe {
			probed++
		}
		if ok {
			written++
		}
		if rem := m.budgetRemaining(time.Now()); rem == 0 {
			budgetExhausted = true
			break
		}
	}

	attrs := []any{
		"scanned", len(targets), "probed", probed, "written", written,
		"backed_off", backedOff, "batch_limit", m.batchLimit,
		"daily_budget", m.dailyBudget,
		"daily_remaining", m.budgetRemaining(time.Now()),
	}
	if budgetExhausted {
		slog.Warn("modality_verification: daily probe budget exhausted, cycle stopped early", attrs...)
	} else {
		slog.Info("modality_verification: cycle done", attrs...)
	}
	return written, nil
}

// probeAndPersist 探测单条绑定并写回结论。
// 返回 (是否写了一行, 是否真实出过网, 错误)。
func (m *ModalityVerification) probeAndPersist(ctx context.Context, t modalityVerifyTarget) (bool, bool, error) {
	// 闸门先走，且必须在解密与出网之前。
	if admit, why := modalityVerifyAdmit(t); !admit {
		slog.Debug("modality_verification: target skipped by admission gate",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"modality", t.Modality,
			"reason", why)
		return false, false, nil
	}
	if m.probe == nil {
		return false, false, fmt.Errorf("modality_verification: probe seam is not wired")
	}
	decryptFn := m.decryptFn
	if decryptFn == nil {
		decryptFn = m.decrypt
	}
	apiKey, err := decryptFn(t)
	if err != nil {
		return false, false, err
	}

	// SQL 与 Go 是同一份模态规则的两份实现。选队列由 SQL 决定（它要和
	// 证据表 join），但这里再核一遍并把不一致**喊出来**：两边漂移时
	// 症状是「某个模态的证据永远收集不到」，没有日志就完全不可见。
	// 不用 Go 的值覆盖 SQL 的值——那会让证据写在 SQL 的模态下、而下轮
	// 扫描又按另一个模态去找，白白反复出网。
	if want := resolveModalityToProbe(t.StoredModality, t.RawModel); want != t.Modality {
		slog.Warn("modality_verification: scan SQL and resolveModalityToProbe disagree "+
			"on the modality to probe — the SSOT for this rule is drifting",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"stored_modality", t.StoredModality,
			"sql_modality", t.Modality,
			"go_modality", want)
	}
	modelField := t.OutboundModel
	if modelField == "" {
		modelField = t.RawModel
	}

	pCtx, cancel := context.WithTimeout(ctx, modalityVerifyProbeTimeout)
	defer cancel()

	// 记账在出网之前，与 capability_backfill 同理：零出网路径（admission
	// 拒绝 / 解密失败）不该进账单，也不该进退避台账。
	if !m.chargeProbe(time.Now()) {
		return false, false, nil
	}
	m.recordAttempt(t)

	res := m.probe(pCtx, modalityVerifyTarget{
		CredentialID:  t.CredentialID,
		CanonicalID:   t.CanonicalID,
		CanonicalName: t.CanonicalName,
		RawModel:      modelField,
		Modality:      t.Modality,
		BaseURL:       t.BaseURL,
		Protocol:      t.Protocol,
		Ciphertext:    t.Ciphertext,
	}, apiKey)
	// 探测用的是 outbound 模型名，但证据行按 raw 名归位（与
	// credential_model_capabilities 同一命名面）。
	res.resolvedModel = modelField

	// 无证据不写：carry 与 read 都 unknown 时这一行不动。静默跳过会让
	// 「为什么这批模型还是没结论」变成不可查的问题，所以记一条日志。
	if res.Carry == ModalityLevelUnknown && res.Read == ModalityLevelUnknown {
		slog.Info("modality_verification: no modality evidence, row left untouched",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"modality", t.Modality,
			"err_code", res.ErrCode,
			"http_status", res.HTTPStatus)
		return false, true, nil
	}

	persist := m.persist
	if persist == nil {
		if m.db == nil {
			return false, true, nil
		}
		persist = m.persistRow
	}
	if err := persist(ctx, t, res); err != nil {
		return false, true, err
	}
	rollup := m.rollup
	if rollup == nil {
		if m.db == nil {
			return false, true, nil
		}
		rollup = m.rollupVerdict
	}
	if err := rollup(ctx, t); err != nil {
		// 证据已落库、汇总裁定失败不是致命：下一轮会重算。但要让运维
		// 看见，否则「证据在、标注没跟上」会静默很久。
		slog.Warn("modality_verification: rollup failed, evidence persisted",
			"credential_id", t.CredentialID,
			"canonical_id", t.CanonicalID,
			"modality", t.Modality,
			"error", err)
		return true, true, nil
	}
	return true, true, nil
}

// applyStreak 把一次新判词并进连续计数，返回 (新的 read_level, 新计数)。
//
// 规则：
//   - confirmed：pos+1，neg 清零。pos 达到 streakGoal 才落 confirmed。
//   - negative：neg+1，pos 清零。neg 达到 streakGoal 才落 negative。
//   - unknown / inconclusive：**不动任何计数**。「没探到」不是一次
//     失败，把它算进负向连击就会让一个间歇性超时的模型被降级。
func applyStreak(prevLevel string, pos, neg int, newLevel string) (level string, newPos, newNeg int) {
	// 归一：SQL 侧一律 COALESCE 成 'unknown'，但这个函数不假设调用方
	// 守规矩——空串透传出去会让「未确认」与「没读过」在证据行里长得
	// 不一样，而那正是判读失败时最难查的一种。
	if strings.TrimSpace(prevLevel) == "" {
		prevLevel = ModalityLevelUnknown
	}
	switch newLevel {
	case ModalityLevelConfirmed:
		pos++
		if pos >= modalityVerifyStreakGoal {
			return ModalityLevelConfirmed, pos, 0
		}
		return prevLevel, pos, 0
	case ModalityLevelNegative:
		neg++
		if neg >= modalityVerifyStreakGoal {
			return ModalityLevelNegative, 0, neg
		}
		return prevLevel, 0, neg
	default:
		return prevLevel, pos, neg
	}
}

// dueTargets 选出到期核实的绑定。
//
// 排序刻意把「一条证据都没有」的行排在最前：存量模型的
// modality_verified_at 恒 NULL，若按 checked_at 排，新老证据行会互相
// 挤占额度，未核实队列可能永远排不上——那正是本任务要修的缺口。
//
// ⚠ 模态决策写在 SQL 里（probe_modality 那个表达式），Go 侧
// resolveModalityToProbe 是同一份规则的第二实现。两份实现必须一致，
// 否则一行会在两轮之间来回换模态、永不停歇。TestModalitySQLMatchesGoRule
// 把这个约定钉住。
func (m *ModalityVerification) dueTargets(ctx context.Context) ([]modalityVerifyTarget, error) {
	qCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rows, err := m.db.Query(qCtx, `
		SELECT pp.credential_id,
		       pp.canonical_id,
		       pp.canonical_name,
		       pp.secret_ciphertext,
		       pp.outbound_model,
		       pp.raw_model_name,
		       pp.probe_modality,
		       pp.base_url,
		       pp.protocol,
		       pp.catalog_code,
		       pp.stored_modality,
		       pp.credential_status,
		       pp.lifecycle_status,
		       pp.credential_disabled,
		       pp.provider_enabled,
		       pp.provider_disabled,
		       pp.binding_available,
		       COALESCE(v.carry_level, 'unknown'),
		       COALESCE(v.read_level, 'unknown'),
		       COALESCE(v.read_pos_streak, 0),
		       COALESCE(v.read_neg_streak, 0)
		FROM (
		    SELECT cmb.credential_id,
		           cmb.provider_model_id,
		           COALESCE(pm.canonical_id, 0) AS canonical_id,
		           COALESCE(mc.canonical_name, pm.canonical_raw_name) AS canonical_name,
		           c.secret_ciphertext,
		           COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, '') AS outbound_model,
		           pm.raw_model_name,
		           COALESCE(mc.modality, pm.modality, 'text') AS stored_modality,
		           -- 探哪个模态。**stored='text' 的行走 vision 分支**，那
		           -- 正是 text→多模态 升级唯一的发现入口：现状
		           -- verifyTargetModality 在 t.Modality == "text" 时就
		           -- return 了，升级在设计上不可发现。
		           CASE
		             WHEN COALESCE(mc.modality, pm.modality, 'text') IN ('audio', 'video')
		               THEN COALESCE(mc.modality, pm.modality, 'text')
		             WHEN pm.raw_model_name ~* '(asr|tts|whisper|transcri|stt)'
		               THEN 'audio'
		             ELSE 'vision'
		           END AS probe_modality,
		           p.base_url,
		           COALESCE(p.protocol, 'openai-completions') AS protocol,
		           COALESCE(p.catalog_code, '') AS catalog_code,
		           COALESCE(c.status, 'active') AS credential_status,
		           COALESCE(c.lifecycle_status, 'active') AS lifecycle_status,
		           COALESCE(c.manual_disabled, FALSE) AS credential_disabled,
		           COALESCE(p.enabled, TRUE) AS provider_enabled,
		           COALESCE(p.manual_disabled, FALSE) AS provider_disabled,
		           COALESCE(cmb.available, TRUE) AS binding_available
		    FROM credential_model_bindings cmb
		    JOIN credentials c ON c.id = cmb.credential_id
		    JOIN providers p ON p.id = c.provider_id
		    JOIN provider_models pm ON pm.id = cmb.provider_model_id
		    LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		    WHERE cmb.available = TRUE
		      AND COALESCE(mc.status, 'active') <> 'disabled'
		      AND c.lifecycle_status = 'active'
		      AND c.status IN ('active', 'cooling', 'degraded')
		      AND COALESCE(c.manual_disabled, FALSE) = FALSE
		      AND COALESCE(p.enabled, TRUE) = TRUE
		      AND COALESCE(p.manual_disabled, FALSE) = FALSE
		) pp
		LEFT JOIN model_modality_verification v
		       ON v.credential_id = pp.credential_id
		      AND v.raw_model_name = pp.raw_model_name
		      AND v.modality = pp.probe_modality
		WHERE v.id IS NULL
		   OR v.checked_at < now() - $1::interval
		ORDER BY (v.id IS NULL) DESC, v.checked_at ASC NULLS FIRST
		LIMIT $2
	`,
		fmt.Sprintf("%d seconds", int(m.staleAfter.Seconds())),
		m.batchLimit*modalityVerifyScanFactor)
	if err != nil {
		return nil, fmt.Errorf("modality_verification scan: %w", err)
	}
	defer rows.Close()

	var out []modalityVerifyTarget
	for rows.Next() {
		var t modalityVerifyTarget
		if err := rows.Scan(
			&t.CredentialID, &t.CanonicalID, &t.CanonicalName, &t.Ciphertext,
			&t.OutboundModel, &t.RawModel, &t.Modality, &t.BaseURL,
			&t.Protocol, &t.CatalogCode, &t.StoredModality,
			&t.CredentialStatus, &t.LifecycleStatus, &t.CredentialDisabled,
			&t.ProviderEnabled, &t.ProviderDisabled, &t.BindingAvailable,
			&t.ExistingCarry, &t.ExistingRead, &t.PosStreak, &t.NegStreak,
		); err != nil {
			return nil, fmt.Errorf("modality_verification scan row: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("modality_verification scan rows: %w", err)
	}
	return out, nil
}

// persistRow 把一次探测的分级结论 upsert 进 model_modality_verification。
func (m *ModalityVerification) persistRow(ctx context.Context, t modalityVerifyTarget, res SemanticProbeResult) error {
	level, pos, neg := applyStreak(t.ExistingRead, t.PosStreak, t.NegStreak, res.Read)

	carryEvidence, _ := json.Marshal(map[string]any{
		"level":       res.Carry,
		"http_status": res.HTTPStatus,
		"err_code":    res.ErrCode,
		"err_msg":     res.ErrMsg,
		"observed_at": time.Now().UTC().Format(time.RFC3339),
	})
	readEvidence, _ := json.Marshal(map[string]any{
		"level":          res.Read,
		"expected":       res.Expected,
		"mentioned":      res.Mentioned,
		"position_score": res.Score,
		"answer":         res.Answer,
		"model":          res.resolvedModel,
		"http_status":    res.HTTPStatus,
		"err_code":       res.ErrCode,
		"observed_at":    time.Now().UTC().Format(time.RFC3339),
	})

	_, err := m.db.Exec(ctx, `
		INSERT INTO model_modality_verification AS v
		       (canonical_id, canonical_name, credential_id, raw_model_name,
		        modality, carry_level, read_level,
		        read_pos_streak, read_neg_streak,
		        carry_evidence, read_evidence, checked_at, updated_at)
		VALUES (NULLIF($1, 0), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now(), now())
		ON CONFLICT (credential_id, raw_model_name, modality) DO UPDATE
		   SET carry_level     = EXCLUDED.carry_level,
		       read_level      = EXCLUDED.read_level,
		       read_pos_streak = EXCLUDED.read_pos_streak,
		       read_neg_streak = EXCLUDED.read_neg_streak,
		       carry_evidence  = EXCLUDED.carry_evidence,
		       read_evidence   = EXCLUDED.read_evidence,
		       checked_at      = now(),
		       updated_at      = now()
		WHERE v.carry_level IS DISTINCT FROM EXCLUDED.carry_level
		   OR v.read_level  IS DISTINCT FROM EXCLUDED.read_level
		   OR v.read_pos_streak IS DISTINCT FROM EXCLUDED.read_pos_streak
		   OR v.read_neg_streak IS DISTINCT FROM EXCLUDED.read_neg_streak
	`,
		t.CanonicalID, t.CanonicalName, t.CredentialID, t.RawModel,
		t.Modality, res.Carry, level, pos, neg, carryEvidence, readEvidence)
	if err != nil {
		return fmt.Errorf("persist modality verification: %w", err)
	}
	return nil
}

// rollupVerdict 把模型级判词写回 models_canonical。
//
// 规则（见文件头「路由信任语义级」）：
//   - verdict=confirmed 才允许升级；
//   - verdict=negative 才允许降级，且不覆盖 modality_source='manual'；
//   - unknown 一律不动。
func (m *ModalityVerification) rollupVerdict(ctx context.Context, t modalityVerifyTarget) error {
	var verdict string
	var bindingsProbed, bindingsConfirmed, bindingsNegative int
	err := m.db.QueryRow(ctx, `
		SELECT verdict, bindings_probed, bindings_confirmed, bindings_negative
		  FROM v_model_modality_verdict
		 WHERE canonical_name = $1 AND modality = $2
	`, t.CanonicalName, t.Modality).
		Scan(&verdict, &bindingsProbed, &bindingsConfirmed, &bindingsNegative)
	if err != nil {
		if err == pgx.ErrNoRows {
			// 证据行刚写进去，视图本该有它；没有说明有别的问题。记日志
			// 而不是静默返回——「证据在、汇总没有」是个真 bug。
			return fmt.Errorf("no verdict row for %s/%s: %w", t.CanonicalName, t.Modality, err)
		}
		return err
	}
	if verdict == "unknown" {
		return nil
	}

	stored := strings.ToLower(strings.TrimSpace(t.StoredModality))
	newModality := ""
	switch verdict {
	case "confirmed":
		newModality = richerModality(stored, t.Modality)
	case "negative":
		if stored == t.Modality {
			newModality = "text"
		} else if stored == "multimodal" {
			// 只有在没有任何模态拿到语义正证据时才降级。混一个模态判负
			// 就把整个模型打成 text，会砍掉它仍然可用的那条腿。
			var anyConfirmed bool
			if err := m.db.QueryRow(ctx, `
				SELECT EXISTS (
				    SELECT 1 FROM v_model_modality_verdict
				     WHERE canonical_name = $1 AND verdict = 'confirmed'
				)
			`, t.CanonicalName).Scan(&anyConfirmed); err != nil {
				return err
			}
			if !anyConfirmed {
				newModality = "text"
			}
		}
	}
	if newModality == "" || newModality == stored {
		return nil
	}

	evidence, _ := json.Marshal(map[string]any{
		"modality":           t.Modality,
		"verdict":            verdict,
		"bindings_probed":    bindingsProbed,
		"bindings_confirmed": bindingsConfirmed,
		"bindings_negative":  bindingsNegative,
		"upgraded_at":        time.Now().UTC().Format(time.RFC3339),
	})

	// 守卫：modality_source='manual' 绝不覆盖（Layer 3 手工覆盖是运维的
	// 显式决定，探测结论无权推翻）。
	_, err = m.db.Exec(ctx, `
		UPDATE models_canonical
		   SET modality            = $1,
		       modality_source     = 'semantic',
		       modality_verified_at = now(),
		       modality_evidence   = $2,
		       updated_at          = now()
		 WHERE canonical_name = $3
		   AND COALESCE(modality_source, '') <> 'manual'
		   AND modality IS DISTINCT FROM $1
	`, newModality, evidence, t.CanonicalName)
	if err != nil {
		return fmt.Errorf("rollup modality: %w", err)
	}
	slog.Info("modality_verification: canonical modality changed by semantic verdict",
		"canonical_name", t.CanonicalName,
		"modality", t.Modality,
		"verdict", verdict,
		"from", stored,
		"to", newModality,
		"bindings_probed", bindingsProbed)
	return nil
}

// richerModality 把一个拿到语义正证据的模态合进当前值。
func richerModality(stored, confirmed string) string {
	switch {
	case stored == confirmed:
		return stored
	case stored == "text":
		return confirmed
	case stored == "multimodal" || confirmed == "multimodal":
		return "multimodal"
	case stored == "audio" && confirmed == "vision":
		return "multimodal"
	case stored == "vision" && confirmed == "audio":
		return "multimodal"
	default:
		return stored
	}
}

// attemptKey 是退避台账的主键。一条绑定可以有多行（不同模态），所以
// 模态必须进键。
func attemptKey(t modalityVerifyTarget) string {
	return fmt.Sprintf("%d|%s|%s", t.CredentialID, t.RawModel, t.Modality)
}

func (m *ModalityVerification) attemptedRecently(t modalityVerifyTarget) bool {
	key := attemptKey(t)
	m.attemptsMu.Lock()
	defer m.attemptsMu.Unlock()
	last, ok := m.attempts[key]
	if !ok {
		return false
	}
	return time.Since(last) < modalityVerifyAttemptBackoff
}

func (m *ModalityVerification) recordAttempt(t modalityVerifyTarget) {
	key := attemptKey(t)
	m.attemptsMu.Lock()
	defer m.attemptsMu.Unlock()
	if len(m.attempts) >= modalityVerifyAttemptLedgerMax {
		// 台账满了就整体清空而不是逐条淘汰：退避只需要「近期」的信息，
		// 清空的后果是多探一次（花钱），逐条淘汰的复杂度换不来更安全性。
		m.attempts = make(map[string]time.Time, modalityVerifyAttemptLedgerMax/4)
	}
	m.attempts[key] = time.Now()
}

// chargeProbe 是出网预算闸门。记账必须发生在真正出网之前。
func (m *ModalityVerification) chargeProbe(now time.Time) bool {
	if m == nil || m.dailyBudget <= 0 {
		return true
	}
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	m.pruneProbesLocked(now)
	if len(m.probes) >= m.dailyBudget {
		return false
	}
	m.probes = append(m.probes, now)
	return true
}

func (m *ModalityVerification) pruneProbesLocked(now time.Time) {
	cutoff := now.Add(-modalityVerifyDailyBudgetWindow)
	keep := m.probes[:0]
	for _, t := range m.probes {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	m.probes = keep
}

// budgetRemaining 返回滚动窗口内的剩余额度。<=0 或未设预算时返回 0，
// 与 chargeProbe 的语义对齐（调用方只把它当提前收尾的信号）。
func (m *ModalityVerification) budgetRemaining(now time.Time) int {
	if m == nil || m.dailyBudget <= 0 {
		return 0
	}
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	m.pruneProbesLocked(now)
	left := m.dailyBudget - len(m.probes)
	if left < 0 {
		return 0
	}
	return left
}

func modalityVerifyDailyBudget() int {
	raw := strings.TrimSpace(os.Getenv(modalityVerifyBudgetEnv))
	if raw == "" {
		return modalityVerifyDefaultDailyBudget
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("modality_verification: daily budget env is not an integer, using default",
			"env", modalityVerifyBudgetEnv, "value", raw,
			"default", modalityVerifyDefaultDailyBudget)
		return modalityVerifyDefaultDailyBudget
	}
	return n
}

// decrypt 复用 capability_backfill 的解密路径（secret.DecryptAny：AES-GCM
// 优先、v1:legacy 回落 Fernet），不新造解密。
func (m *ModalityVerification) decrypt(t modalityVerifyTarget) (string, error) {
	s := string(t.Ciphertext)
	if !secret.IsV1Envelope(s) {
		return "", fmt.Errorf("unsupported secret format")
	}
	if m.keyring == nil {
		return "", fmt.Errorf("keyring not configured")
	}
	pt, _, err := secret.DecryptAny(s, m.keyring, m.encKey)
	if err != nil {
		slog.Error("modality_verification: decrypt failed",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"envelope_kid", decryptEnvelopeKid(s),
			"envelope_len", len(s),
			"error", err.Error())
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// resolveModalityToProbe 是到期扫描用的模态决策，导出成纯函数便于单测。
// 它是「本任务能发现 text→多模态 升级」那条承诺的实现点。
func resolveModalityToProbe(storedModality, rawModelName string) string {
	stored := strings.ToLower(strings.TrimSpace(storedModality))
	if stored == "audio" {
		return "audio"
	}
	if stored == "video" {
		return "video"
	}
	// 规则表说它是 ASR/TTS 家族 ⇒ 探 audio（只落结构级）。这是存量 text
	// 行里「其实能听音」的那一类。
	if modelname.InferModality(rawModelName) == "audio" {
		return "audio"
	}
	// 其余一律探 vision —— **包括 stored='text' 的行**。那正是升级发现的
	// 唯一入口；漏掉它等于把本任务退化成现状那个只会确认、不会发现的形态。
	return "vision"
}
