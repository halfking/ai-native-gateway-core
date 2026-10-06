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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// 模态必须已算出，且**必须是我们真的会探的那个**。
	//
	// ⚠ 2026-10-05 修的缺陷：本闸初版对 vision/audio/video 一律放行。而唯一的
	// 探针实现 ProbeVisionSemantics **不接受 modality 参数**，它在函数体里把
	// 探针模态写死成 "vision"（ProbeModality(..., "vision", ...) +
	// NewVisionChallenge + visionChallengePayload），而生产接线
	// （NewModalityVerification 的 probe 闭包）**没有把 t.Modality 传进去**。
	// ⇒ 一条 audio 目标会被发**图像**挑战，而落库用的是 t.Modality，
	// 于是 ASR/TTS 模型会被记成「audio 被拒」。
	//
	// 那不是噪声，是**方向反了的假证据**：真库实测有 12 绑定 / 8 个模型会走到
	// 这条路（gpt-audio、gpt-audio-mini、gpt-4o-realtime-preview、
	// mimo-v2-tts、mimo-v2.5-asr、mimo-v2.5-tts、mimo-v2.5-tts-voiceclone、
	// mimo-v2.5-tts-voicedesign）—— 其中 mimo-v2.5-asr / mimo-v2.5-tts 正是
	// 迁移 820 刚把它们从 text 纠正成 audio 的那两个例子。worker 去证伪的，
	// 恰恰是那次纠正的结果。
	//
	// 为什么不反过来去实现一个音频探针：写它需要先定义「支持 audio」在
	// 能力层面意味着什么（能收 input_audio？能 TTS 输出？两者对不同供应商
	// 根本不是一回事），而那是个需要人拍板的产品定义。**在有定义之前，
	// 唯一诚实的动作是不写证据** —— 宁可这 8 个模型永远停在 inferred，
	// 也不要往证据表里灌一条「测过了，不支持」。
	//
	// 判据刻意按**探针实现**写死，而不是按「模态是否合法」：合法模态有三种，
	// 可探的只有一种。加一种探针实现时，这里要同步放开。
	switch t.Modality {
	case modalityVerifyProbeModality:
		return true, ""
	case "":
		return false, "modality_unresolved"
	// 合法但**没有探针**的模态。vision 已被上面的常量分支收走，不重复列举。
	case "audio", "video":
		return false, "modality_no_probe_" + t.Modality
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
		if probed >= m.effectiveBatchLimit() {
			break
		}
		if m.attemptedRecently(t) {
			backedOff++
			continue
		}
		// 准入闸门是**纯函数**（逐条闸门可测，见 modality_zero_egress_realdb_test.go），
		// 这里再算一次只为拿到「为什么被跳过」这个原因。零出网、零副作用，
		// 代价只是几十纳秒的字段比较。
		//
		// 为什么不改 probeAndPersist 的返回值把原因带出来：它有 5 处既有判据
		// 直接调用（零出网/预算/解密失败三条路径），改签名会连带它们一起动。
		// 而这里重算一次是**读同一个纯函数**，两份判据不可能漂移。
		admit, admitWhy := modalityVerifyAdmit(t)
		startedAt := time.Now()
		ok, didProbe, err := m.probeAndPersist(ctx, t)
		m.recordProbeLedger(ctx, t, admit, admitWhy, didProbe, err, startedAt)
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
		// 只在**确实配了预算**且用完时才提前收尾。
		//
		// 真正的修复在 budgetRemaining（未设预算时它返回 -1 而不是 0），所以
		// `m.dailyBudget > 0` 这一半**不承担承重**，留着是为了让这个判据自己把
		// 前提写出来：读代码的人不必先跳进 budgetRemaining 才知道 0 是「用完」
		// 而不是「没配」。若哪天有人把 budgetRemaining 改回返回 0，这里仍是对的。
		if m.dailyBudget > 0 && m.budgetRemaining(time.Now()) == 0 {
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

// probeLedgerMissingTableOnce 让「台账表还不存在」每进程只喊一次。
//
// 为什么是 Warn 而不是 Debug：835 未应用的环境上，这个写入会**每轮**失败。
// 静默（Debug）等于把「你的核实循环在盲飞」又藏回日志里，而 832 迁移的注释
// 已经为同一族问题写过判词 ——「没有这张表，一次持续数周的中断与一个健康系统
// 无法区分」。喊一次既不会刷屏，又足以让运维看见该应用 835。
//
// 为什么只认 42P01：与 bg/routing_health_checks.go 的 Optional 纪律同源。
// 其它错误码（权限、连接、约束）都是**真故障**，必须每次都喊。
var probeLedgerMissingTableOnce sync.Once

// recordProbeLedger 把一次核实尝试写进统一自检台账 system_probe_runs。
//
// # 为什么需要它（2026-10-06 实测）
//
// 目标里的「这个需要加入到自检任务中」此前**没有兑现**：核实循环是独立 ticker、
// 进不了 credential_probe_queue、语义探针经 internal/upstreamurl 直连上游
// （所以不产生 request_logs）、尝试记录只在进程内 m.attempts。⇒ 运维唯一能
// 问出「核实跑过什么」的通道是 slog，而这些日志活不过一次重启。
//
// 本方法是**只加出口、不改行为**的：核实结论仍然只写
// model_modality_verification / models_canonical。台账写失败绝不影响核实
// （见下面的错误处理），因为反过来才是危险的——为了记账把核实循环弄停。
//
// # 行的形状与两个刻意选择
//
//	task_id = 0
//	    多模态核实**不是**队列任务，没有 credential_probe_queue 的任务号。
//	    合成一个哈希任务号会在按 task_id 分组的看板上伪装成别的任务的执行。
//	    0 是「本行不属于任何队列任务」的显式标记。
//
//	逐目标一行，而不是一轮一行
//	    决策原文是「每轮写一行」，但这张表按 (credential_id, raw_model) 造：
//	    两列 NOT NULL，各带一个 btree 索引。一轮一行只能给它们塞哨兵值，
//	    那样每行既无归属也不可行动。逐目标写则每行都指向一个具体的
//	    (凭据, 模型) 与一个具体的跳过原因。差异在 835 的文件头里也记了一份。
//
// # 不写台账的情况（别把台账当整轮的完整账）
//
//	· attemptedRecently 命中的退避：同进程内的重复目标，写进去等于重复计数。
//	· 循环因 batchLimit / 日预算提前 break 后**未被扫描到**的目标：压根没尝试。
//
// 这两类只体现在 VerifyOnce 结尾那行 slog 的 scanned/probed/backed_off 上。
func (m *ModalityVerification) recordProbeLedger(
	ctx context.Context,
	t modalityVerifyTarget,
	admit bool,
	admitWhy string,
	didProbe bool,
	probeErr error,
	startedAt time.Time,
) {
	if m == nil || m.db == nil {
		return
	}

	status, skipReason, errDetail := "success", "", ""
	switch {
	case probeErr != nil:
		status = "failed"
		errDetail = truncateForLedger(probeErr.Error(), 500)
	case !admit:
		// 准入闸门拒绝。**不是**错误：一条被有意排除的绑定探它才是问题。
		status = "skipped"
		skipReason = "admission gate: " + admitWhy
	case !didProbe:
		// 过了闸门却没出网 ⇒ 日预算/批次内记账把它挡下了。
		status = "skipped"
		skipReason = "daily probe budget exhausted"
	}

	// 台账写入用独立超时：它绝不能继承一个已经超时的 ctx 而被静默丢弃，
	// 也不能拖住核实循环。
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	_, err := m.db.Exec(lctx, `
		INSERT INTO public.system_probe_runs
			(task_id, task_type, automaticity, credential_id, raw_model,
			 source, worker_id, status, attempt, max_attempts,
			 skip_reason, err_detail, started_at, finished_at)
		VALUES (0, 'modality_verify', 'automatic', $1, $2,
		        'modality_verification', 'modality-verification-worker', $3, 1, 1,
		        NULLIF($4, ''), NULLIF($5, ''), $6, $7)`,
		t.CredentialID, t.RawModel, status, skipReason, errDetail,
		startedAt, time.Now())
	if err == nil {
		return
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		probeLedgerMissingTableOnce.Do(func() {
			slog.Warn("modality_verification: cannot write the self-check ledger because "+
				"system_probe_runs does not exist — apply migration 835. Verification itself "+
				"keeps running and keeps writing its verdicts; what is missing is the durable "+
				"record that it ran (this message is logged once per process).",
				"error", err)
		})
		return
	}
	slog.Warn("modality_verification: failed to write the self-check ledger row",
		"credential_id", t.CredentialID,
		"raw_model", t.RawModel,
		"status", status,
		"error", err)
}

// truncateForLedger 截断进台账的长文本。
//
// err_detail / skip_reason 都会进运营看板上按维度筛选的表格，一个把上游
// 整个响应体带回来的错误会让单行膨胀到不可读，也把看板的列宽撑坏。
func truncateForLedger(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
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
// modalityVerifyAddressableSource 是**核实 worker 能触达的绑定集合**。
//
// ★ 抽成常量只有一个理由：新加的健康检查 `modality_gate_readiness_floor` 要回答
// 「严格门挡住的模型里，有多少是 worker 永远够不着的」。它必须用**这一份**
// 谓词 —— 抄第二份的话，两边一漂移，那条检查报出来的地板数就是**另一个集合**
// 的数，而它的全部价值就在于「这个数字永远降不下去」这个事实。
// （我第一版就是用自己手写的谓词量的，漏了 c.status IN ('cooling','degraded')、
// manual_disabled 与 p.enabled，量出 709/1576；换成这一份之后是 584/1080。）
//
// ⚠ 改这里等于改「谁会被核实」。`dueTargets` 与那条健康检查都引用它，
// 所以改动对两者同时生效 —— 这正是想要的。
const modalityVerifyAddressableSource = `
		(
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
		)`

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
FROM `+modalityVerifyAddressableSource+` pp
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
		m.scanLimit())
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
//
// 证据列（carry_evidence/read_evidence）必须经 capabilityEvidenceParam：
// 裸 []byte 在 SimpleProtocol 下内联为 bytea hex 字面量，jsonb 解析必炸
// （2026-10-05 R24 审计：252 真库 6 次失败，245 预生产 19:05 起带病运行）。
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
		t.Modality, res.Carry, level, pos, neg,
		capabilityEvidenceParam(carryEvidence), capabilityEvidenceParam(readEvidence))
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
			// 降级的前提是「**它没有任何多模态的腿了**」，不是「有一条腿坏了」。
			//
			// ⚠ 2026-10-05 修的第二个潜伏缺陷（实测复现，日志原文见
			// TestRollupDoesNotDowngradeMultimodalOnVisionOnlyNegative）：
			// 本分支初版只查 anyConfirmed。而 worker **只会探 vision** ——
			// SQL 的 probe_modality 对非 audio/video 一律给 vision，audio
			// 目标又被准入闸挡掉（modalityVerifyAdmit）⇒ confirmed 只可能
			// 来自 vision ⇒ 任何 multimodal 模型只要 vision 判负，anyConfirmed
			// 必然为 false ⇒ **一律降级成 text**。
			// 实测：`modality=vision verdict=negative from=multimodal to=text
			// bindings_probed=1` —— 凭**一条腿的坏**断言「另一条腿也没有」。
			//
			// 为什么方向不能反过来：留在 multimodal 的代价是**可能多一次
			// 失败路由**（图片请求打到一个不收图的模型）；降到 text 的代价是
			// **把仍然可用的 audio 腿砍掉**，而 modality='multimodal' 在路由
			// 侧恰恰意味着「收 audio 或图片」（820 文件头引的候选过滤
			// COALESCE(mc.modality,'text') IN ('audio','multimodal')）⇒
			// 降级会让一个能用的 ASR 模型从候选里消失。两个方向不对称，
			// 所以证据不足时**不动**。
			//
			// 门槛写成「≥2 个不同模态判负」而不是「全部模态」：后者需要一个
			// 「这个模型声明了哪些模态」的清单，而 modality 列只存 multimodal
			// 这一个值，没有可枚举的声明集。≥2 是**可判定的下界**——它保证
			// 结论不再只建立在一次探测上。
			//
			// ★ 现状：worker 只有 vision 一种探针，所以这个降级**当前不会发生**。
			//   这是刻意的保守空转，不是死代码：实现第二种探针后它自动开始工作，
			//   且 TestRollupDoesNotDowngradeMultimodalOnVisionOnlyNegative
			//   钉住「单一模态负证据不得降级」，任何人加探针时都会撞上它。
			var probedModalities, negativeModalities int
			if err := m.db.QueryRow(ctx, `
				SELECT count(DISTINCT modality),
				       count(DISTINCT modality) FILTER (WHERE verdict = 'negative')
				  FROM v_model_modality_verdict
				 WHERE canonical_name = $1
			`, t.CanonicalName).Scan(&probedModalities, &negativeModalities); err != nil {
				return err
			}
			if probedModalities < 2 || negativeModalities < probedModalities {
				// 证据只覆盖一条腿（或还没覆盖全）⇒ 不做结论。
				return nil
			}
			newModality = "text"
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
	tag, err := m.db.Exec(ctx, `
		UPDATE models_canonical
		   SET modality            = $1,
		       modality_source     = 'semantic',
		       modality_verified_at = now(),
		       modality_evidence   = $2,
		       updated_at          = now()
		 WHERE canonical_name = $3
		   AND COALESCE(modality_source, '') <> 'manual'
		   AND modality IS DISTINCT FROM $1
	`, newModality, capabilityEvidenceParam(evidence), t.CanonicalName)
	if err != nil {
		return fmt.Errorf("rollup modality: %w", err)
	}
	// ★ 只在**真的改了行**时才说改了。
	//
	// 上面那条 UPDATE 有三个可以否决它的 WHERE 条件：手工覆盖守卫
	// (`modality_source <> 'manual'`)、以及 `modality IS DISTINCT FROM $1`
	// （本来就等于目标值）。而这个 Info 原先是无条件打印的 ⇒ 一行都没改动也会
	// 喊「canonical modality changed … from=text to=vision」。
	//
	// 危害不是难看：**日志是运维判读「标注到底生效没有」的主要证据**，一条说改
	// 了而库里没改的记录会让人以为手工覆盖被探测推翻了（或者反过来，以为标注
	// 成功了而其实没生效），并据此做出错误处置。
	// 真库实测撞出来的：m-manual 的 source='manual' ⇒ 0 行受影响，日志照喊。
	if tag.RowsAffected() == 0 {
		slog.Info("modality_verification: verdict reached but the canonical row was not updated "+
			"(manual override, or the value already matches) — no label change",
			"canonical_name", t.CanonicalName,
			"modality", t.Modality,
			"verdict", verdict,
			"stored_modality", stored,
			"would_be", newModality,
			"modality_source", "unchanged (manual override or already equal)")
		return nil
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

// scanLimit 是单轮 SQL 扫描窗口：每轮上限 × modalityVerifyScanFactor。
//
// ⚠ 必须走 effectiveBatchLimit()，不能直接用 m.batchLimit：`batchLimit<=0` 时
// `0 * 4 == 0`，而这个值直接进 `LIMIT $2` ⇒ `LIMIT 0` ⇒ **扫不出任何行**。
// 于是「未设置」在扫描侧退化成「什么都不做」，而循环侧（已修）却在正常跑 ——
// 两个地方对同一个零值的解释不一致，整轮依然静默停摆。
// 与 CapabilityBackfill.scanLimit 同一函数、同一理由。
func (m *ModalityVerification) scanLimit() int {
	limit := m.effectiveBatchLimit()
	n := limit * modalityVerifyScanFactor
	if n <= limit || n < 0 {
		return limit
	}
	return n
}

// effectiveBatchLimit 是本轮真正生效的每轮探测上限。
//
// batchLimit<=0 读作「未设置」⇒ 回落到包常量。与 CapabilityBackfill 同一条规则。
// ⚠ 为什么不是「不限」：核实带挑战图，是这批周期任务里单次最贵的一种，
// 把上限配成 0 不该变成「无上限出网」；回落到有界默认值既修掉了
// 「静默什么都不做」，又不会把一个配置失误变成开销失控。
func (m *ModalityVerification) effectiveBatchLimit() int {
	if m == nil || m.batchLimit <= 0 {
		return modalityVerifyBatchLimit
	}
	return m.batchLimit
}

// budgetRemaining 返回滚动窗口内的剩余额度。
//
// **未设预算（dailyBudget<=0）时返回 -1，不返回 0。** 0 的含义是「额度用完了」。
//
// ⚠ 这里曾返回 0，而它的注释还写着「与 chargeProbe 的语义对齐」—— 并不对齐：
// `chargeProbe` 在 dailyBudget<=0 时**放行**（返回 true），而返回 0 的
// `budgetRemaining` 会让 `rem == 0` 那个提前收尾判据在「压根没设预算」时恒真。
// 实测后果：把日预算配成 0（`LLM_GATEWAY_MODALITY_VERIFY_BUDGET=0`，意图多半是
// 「不限制」）会让整个核实任务**每轮只探一条**，还外加一条声称预算耗尽的假日志。
//
// 现在与同族的 `CapabilityBackfill.budgetRemaining` 一致：那边本来就返回 -1，
// 注释也写明「<=0 表示不设预算，调用方按无限制处理」—— 同一个人写的两个函数，
// 一个守住了这个约定，另一个没守住。**同类函数的返回值约定要逐个核，不能靠「对齐」
// 这种说法推断。**
func (m *ModalityVerification) budgetRemaining(now time.Time) int {
	if m == nil || m.dailyBudget <= 0 {
		return -1
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
