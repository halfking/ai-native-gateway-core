// Package bg — candidate_failure_monitor.go
//
// 2026-06-23 Phase 3 (P2) of the minimax-m3 transient-error fix: alerting
// + auto-cooling based on candidate_failure_logs.
//
// The Phase 2 work recorded every per-candidate failure to
// candidate_failure_logs. This worker periodically rolls up those rows into:
//
//  1. Alert events: when a (credential, model, error_kind) combination
//     accumulates more than `alertThreshold` failures in
//     `alertWindow`, the worker fires a slog.Warn (and a webhook if
//     configured). The alert is debounced — same combo fires at most
//     once per cooldown — so a sustained outage doesn't spam the log.
//  2. Auto-cool: when a credential's failure ratio over the last N
//     attempts exceeds `coolRatio`, the worker flips the credential's
//     availability_state to 'cooling' with a short recover_at so the
//     router stops selecting it. Admin can still force-recover.
//
// Configuration via env (parsed at Start time, no hot reload):
//
//	CANDIDATE_FAILURE_MONITOR_INTERVAL=1m         run period
//	CANDIDATE_FAILURE_ALERT_THRESHOLD=10          failures to trigger alert
//	CANDIDATE_FAILURE_ALERT_WINDOW=5m             window for alert
//	CANDIDATE_FAILURE_ALERT_COOLDOWN=15m          debounce window
//	CANDIDATE_FAILURE_COOL_RATIO=0.8              ratio over recent attempts
//	CANDIDATE_FAILURE_COOL_MIN_SAMPLES=20         min attempts before cool
//	CANDIDATE_FAILURE_COOL_MINUTES=2              cool duration
//	CANDIDATE_FAILURE_STALENESS_THRESHOLD=30m     max age of the last failure row
//	CANDIDATE_FAILURE_WEBHOOK_URL=""               optional webhook for alerts
package bg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/dbrows"
)

// CandidateFailureAlert is the row shape inserted into the in-memory
// alert log + (optionally) the webhook payload.
// CandidateFailureAlert represents a detected anomaly in candidate selection failures.
type CandidateFailureAlert struct {
	Ts            time.Time `json:"ts"`
	CredentialID  int       `json:"credential_id"`
	ProviderID    int       `json:"provider_id"`
	RawModelName  string    `json:"raw_model_name"`
	ErrorKind     string    `json:"error_kind"`
	Count         int       `json:"count"`
	WindowSec     int       `json:"window_sec"`
	DistinctCodes int       `json:"distinct_status_codes"`
	LastBody      string    `json:"last_response_preview,omitempty"`
}

// CandidateFailureMonitor runs the alert + auto-cool loop. nil-safe.
type CandidateFailureMonitor struct {
	db      *pgxpool.Pool
	cancel  context.CancelFunc
	done    chan struct{}
	stateMu sync.Mutex
	started bool
	stopped bool

	interval           time.Duration
	alertThresh        int
	alertWindow        time.Duration
	alertCooldown      time.Duration
	coolRatio          float64
	coolMinSamples     int
	coolMinutes        int
	stalenessThreshold time.Duration
	webhookURL         string

	// alertLastFired debounces duplicate alerts. Keyed by
	// "credential_id|raw_model_name|error_kind".
	alertLastFired map[string]time.Time
	alertMu        sync.Mutex

	// Recent alerts kept in-memory for the admin UI to display. Capped
	// at 200 entries so a long outage doesn't blow up memory.
	recentAlerts []CandidateFailureAlert
}

func NewCandidateFailureMonitor(db *pgxpool.Pool) *CandidateFailureMonitor {
	m := &CandidateFailureMonitor{
		db:                 db,
		done:               make(chan struct{}),
		interval:           parseDurationEnv("CANDIDATE_FAILURE_MONITOR_INTERVAL", time.Minute),
		alertThresh:        parseIntEnv("CANDIDATE_FAILURE_ALERT_THRESHOLD", 10),
		alertWindow:        parseDurationEnv("CANDIDATE_FAILURE_ALERT_WINDOW", 5*time.Minute),
		alertCooldown:      parseDurationEnv("CANDIDATE_FAILURE_ALERT_COOLDOWN", 15*time.Minute),
		coolRatio:          parseFloatEnv("CANDIDATE_FAILURE_COOL_RATIO", 0.8),
		coolMinSamples:     parseIntEnv("CANDIDATE_FAILURE_COOL_MIN_SAMPLES", 20),
		coolMinutes:        parseIntEnv("CANDIDATE_FAILURE_COOL_MINUTES", 2),
		stalenessThreshold: parseDurationEnv("CANDIDATE_FAILURE_STALENESS_THRESHOLD", 30*time.Minute),
		webhookURL:         os.Getenv("CANDIDATE_FAILURE_WEBHOOK_URL"),
		alertLastFired:     make(map[string]time.Time),
		recentAlerts:       make([]CandidateFailureAlert, 0, 64),
	}
	slog.Info("candidate_failure_monitor configured",
		"interval", m.interval,
		"alert_threshold", m.alertThresh,
		"alert_window", m.alertWindow,
		"cool_ratio", m.coolRatio,
		"cool_min_samples", m.coolMinSamples,
		"cool_minutes", m.coolMinutes,
		"staleness_threshold", m.stalenessThreshold,
		"webhook_set", m.webhookURL != "",
	)
	return m
}

func (m *CandidateFailureMonitor) Start(ctx context.Context) {
	m.stateMu.Lock()
	if m.started || m.stopped {
		m.stateMu.Unlock()
		return
	}
	m.started = true
	ctx, m.cancel = context.WithCancel(ctx)
	m.stateMu.Unlock()
	Go("candidate_failure_monitor.run", func() { m.run(ctx) })
	slog.Info("candidate_failure_monitor started")
}

func (m *CandidateFailureMonitor) Stop() {
	m.stateMu.Lock()
	if m.stopped {
		m.stateMu.Unlock()
		return
	}
	m.stopped = true
	cancel := m.cancel
	started := m.started
	m.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-m.done
	}
}

func (m *CandidateFailureMonitor) run(ctx context.Context) {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	// Run once at start so a freshly-started gateway catches existing
	// failure patterns without waiting a full interval.
	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// tick runs one pass: detect alert conditions + auto-cool triggers.
// Each step is independent; failures in one don't block the other.
func (m *CandidateFailureMonitor) tick(ctx context.Context) {
	if m.db == nil {
		return
	}
	stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := m.checkAlerts(stepCtx); err != nil {
		slog.Warn("candidate_failure_monitor: checkAlerts failed", "error", err)
	}
	if err := m.checkAutoCool(stepCtx); err != nil {
		slog.Warn("candidate_failure_monitor: checkAutoCool failed", "error", err)
	}
	if err := m.checkStaleness(stepCtx); err != nil {
		slog.Warn("candidate_failure_monitor: checkStaleness failed", "error", err)
	}
}

// checkStalenessSQL: 见 checkStaleness 内的两条子查询的形态约束。
//
// 2026-10-18 (24h 审计轮：S4 停写 + 假阳性 ERROR)：
// 原实现读 `request_logs_with_current_month` 当 "最近请求活动" 信号，
// 但该视图 UNION 了 `session_turns_hot + session_turns + request_logs 派生`
// 三腿 —— S4 停写时 `_request_logs_hot` 冻结，但 session_log 臂照写
// ⇒ lastRequest 恒有值（最近 session turn 行）⇒ staleness 永远"有最近活动"。
// 本机实测：S4 停写后 candidate_failure_logs_hot 0 行（无真实上游调用），
// `request_logs_with_current_month` 仍 838 行（来自 session_turns），
// staleness 22h+ 假阳性。
// 修：lastRequest 改读 `request_logs_with_current_month_without_request_class_due_at`，
// 该视图仅含 request_logs 腿（不含 session_turns），与 failure writer 写
// 入口同源；S4 停写后该视图也 0 行 ⇒ lastRequest=nil ⇒ 不告警。
// 写门禁（TestStalenessLastRequestSurfaceExcludesSessionTurns）锁定表名序列，
// 防止后人把视图改回 session_turns 混合形态。
//
// ⚠ 依赖登记（2026-10-19 审计补）：该视图是 577/734 为规避投影缺失而**故意**
// 建成的 v1-only 中间层，不是承重对象，随时可能被清理迁移删掉。它在
// cmd/gateway/request_logs_view_session_arm_pin_test.go 的 v1OnlyRequestLogsViews
// 里登记（那是该事实的唯一真相来源，由真库 pg_get_viewdef 重算核对）。
// ⇒ 若哪天该视图被删，本检查会**每 tick 报错**（而不是静默漏告警），
//
//	处置顺序是：先改这条常量，再同步那份登记表，两处一起改才算闭环。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：**那一天到了**——890 删掉了这张
// v1-only 视图（连同另外三张 v1-only 包装视图），本检查的源已换成会话族原生源
// （见下面的 checkStalenessLastRequestSurface）。
// ⚠ 那个「靠冻结腿关掉假阳性」的 workaround 随之作废：890 之后只剩一个请求面，
// 不存在「v1 冻结、session 照写」的分裂态 ⇒ 本检查重新变回它本来的形状。
// ⇒ cmd/gateway/request_logs_view_session_arm_pin_test.go 的 v1OnlyRequestLogsViews
// 登记表里那一项、以及 admin/request_logs_stop_write_classification_test.go 里
// 「staleness 读的是 v1-only 腿」的断言，**都需要父会话同步**（不在本文件范围）。
//
// checkStalenessLastRequestSurface 是「最近请求活动」信号所读的源。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：原值是 v1-only 视图
// `request_logs_with_current_month_without_request_class_due_at`（读 v1 族的
// hot ∪ parent）——**该视图随 890 一起被 DROP**（它是 577/734 为规避投影缺失
// 而故意建成的 v1-only 中间层，见 890 迁移第 4 步）⇒ 继续读它必然 42P01。
// ⇒ 换成会话族原生源（热腿）。
//
// ⚠ 为什么是**热腿**而不是 hot ∪ parent（与被删视图的读取范围相比是收窄的）：
//
//	① 语义上等价：这条查询只问「最近 5 分钟有没有请求活动」，而
//	   promote_session_turns_hot_to_partition 的保留期默认 **7 天**
//	   （实测该函数签名 p_retention interval DEFAULT '7 days'）⇒ 5 分钟内的行
//	   必然还在热表里，冷腿不可能持有更新的行。真库核对：两条腿返回的
//	   lastRequest 完全相同（2026-10-20 13:11:03）。
//	② 成本上不等价：会话族原生源是 118 列投影 + UNION ALL + LEFT JOIN 特征层，
//	   **规划**成本实测 ~390 ms（其中执行仅 0.7 ms；热腿 5 ms）。这条查询按
//	   monitor 的 tick（默认 1 分钟）跑 ⇒ 每次 tick 付 0.4 s 规划只为算一个
//	   max(ts)，不划算。热腿实测 5 ms。
//
// ⚠⚠ **语义变化，父会话须知悉**——这是本轮里唯一一处「原设计前提被抽掉」的
// 改动，结论不是「换个源就行」，而是「**原来那条 v1-only 腿是在替谁挡**」：
//
//	2026-10-18 那一轮特意挑 v1-only 腿（不含 session_turns UNION），是因为
//	S4 停写期「v1 冻结、session 臂照写」⇒ 读 v1 腿能让 lastRequest 恒为
//	NULL，从根上关掉「网关没流量却报管道僵死」的假阳性。**它挡的其实是探针
//	流量**：停写期会话族照写的那批行，是 model-probe / credential-selfcheck
//	这些探针写出来的（探针也走 telemetry ⇒ 也进会话族），而真用户流量确实
//	没有 ⇒ 于是 lastRequest 非 nil、lastFailure 为 nil ⇒ 假阳性。
//
//	⇒ 890 之后**只剩一个请求面**（会话族），「靠冻结腿把探针挡在外面」这条
//	路不存在了 ⇒ 挡探针这件事必须**显式**做：下面这条查询加了会话族的探针
//	排除谓词（origin_stage / task_type / origin_actor 三臂，见
//	bg/probe_policy.go 的 probeTrafficExclusionPredicateSession）。
//	⇒ 换句话说：**「只允许 read 端 request_logs 形态」这条旧纪律的意图被保留
//	下来了，实现方式从「换一族」变成了「同一族里排探针」。**
//
//	⚠ 由此产生的两处父会话动作（不在本文件范围）：
//	① bg/candidate_failure_monitor_logic_test.go 的
//	   TestStalenessLastRequestSurfaceExcludesSessionTurns 断言「源里不能出现
//	   session_turns」——这条断言的前提（存在一条非 session 的请求面）已不存在，
//	   需改写成「源是会话族热腿 **且** 带会话族探针排除谓词」。
//	② admin/request_logs_stop_write_classification_test.go 里
//	   「staleness 读的是 v1-only 腿」的证据登记同样需要更新。
var checkStalenessLastRequestSurface = db.SessionFamilyTurnsHotSourceSQL()

// checkStaleness detects a dead or unwired failure-log pipeline when the
// gateway has recent request activity. A quiet gateway must not alert merely
// because no candidate failure has ever been recorded.
func (m *CandidateFailureMonitor) checkStaleness(ctx context.Context) error {
	var lastFailure, lastRequest *time.Time
	var failureSourceMaxID, requestSourceMaxID *int64
	// 2026-09-12: candidate_failure_logs 视图必需 — bare 父表只 hold cold
	// rows，partition promote 失败时（09-10 P5 duplicate key）父表 max(ts)
	// 会 8h+ stale 而 hot 表每分钟在写，旧 surface 报警假阴性。
	// 2026-10-18: lastRequest 改读 v1-only 腿，剔除 session_turns UNION。
	// ★ 2026-10-20（迁移 890，v1 族退役）：那个 v1-only 视图已被 890 删掉 ⇒
	// 源换成会话族热腿（见 checkStalenessLastRequestSurface 的注释）。
	// ⚠ 探针排除谓词是**必需**的，不是锦上添花：v1-only 腿原来顺带把探针流量
	// 挡在了外面（停写期会话族照写的那批行是探针写的 ⇒ 假阳性）；890 之后只有
	// 一个请求面，必须显式排探针才能保住同一条纪律。
	// 5 分钟窗口保留：它把「最近有请求活动」这个前提钉死，也是这条查询的
	// 成本边界（谓词可下推到 session_turns_hot 的 ts 索引）。
	//
	// 2026-10-20 (24h 审计轮) 加 source_max_*_id 子查询：S4 退役 / 主动停写
	// 与 failure writer 真坏 两种成因在 max(ts) 形态上一致，需要用历史
	// aggregation_id / id 把它们区分开（见 candidateFailureMonitorClassifyStale）。
	checkStalenessSQL := `
		SELECT
			(SELECT max(ts) FROM candidate_failure_logs_with_current_month),
			(SELECT max(ts) FROM ` + checkStalenessLastRequestSurface + ` rl
			  WHERE rl.ts >= now() - interval '5 minutes'
			    AND ` + fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl") + `
			),
			(SELECT max(aggregation_id) FROM candidate_failure_logs_with_current_month),
			(SELECT max(id) FROM ` + checkStalenessLastRequestSurface + ` rl
			  WHERE ` + fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl") + `
			)
	`
	if err := m.db.QueryRow(ctx, checkStalenessSQL).Scan(&lastFailure, &lastRequest, &failureSourceMaxID, &requestSourceMaxID); err != nil {
		return err
	}
	if !failureLogIsStale(lastFailure, lastRequest, time.Now(), m.stalenessThreshold) {
		return nil
	}

	now := time.Now()
	key := "candidate_failure_logs|staleness"
	if !m.shouldFire(key, now) {
		return nil
	}

	age := "no rows"
	if lastFailure != nil {
		age = time.Since(*lastFailure).Round(time.Second).String()
	}
	alert := CandidateFailureAlert{
		Ts:        now,
		ErrorKind: "candidate_failure_log_stale",
		WindowSec: int(m.stalenessThreshold.Seconds()),
		LastBody:  age,
	}
	// S4 退役 / 主动停写 与 failure writer 真坏 在 max(ts) 形态上**完全一致**
	// （lastRequest=alive, lastFailure=stale）⇒ 单纯看时间戳没法区分。本轮选
	// 「**按 source_max_failure_id 是否 > 0 二分类**」：
	//   - source_max_failure_id > 0 ⇒ 曾经活跃过 ⇒ 视作 S4 退役/停写 ⇒ WARN
	//   - source_max_failure_id = 0 ⇒ 从未写入过 ⇒ 视作 writer 真坏 ⇒ ERROR
	//
	// ⚠ 这是个**有偏判据**：(b) 的早期阶段也会短暂 source_max=0，与「刚启动
	// 还没失败过」不易区分。但 stalenessThreshold 默认 30 分钟，已经滤掉了
	// 「刚启动」场景（前 30 分钟 gateway 真没失败是正常的）；30 分钟+ 仍
	// source_max=0 ⇒ wiring 真坏。
	//
	// 同时把 source_max_failure_id / source_max_request_id 两个诊断字段透传到
	// 日志与告警体，让告警接收端能根据业务上下文（S4 退役窗口表）决定是否抑制。
	fid := int64(0)
	if failureSourceMaxID != nil {
		fid = *failureSourceMaxID
	}
	rid := int64(0)
	if requestSourceMaxID != nil {
		rid = *requestSourceMaxID
	}
	switch candidateFailureMonitorClassifyStale(fid) {
	case stalenessLikelyStopWrite:
		slog.Warn("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)",
			"last_insert_at", lastFailure,
			"last_request_at", lastRequest,
			"source_max_failure_id", fid,
			sourceMaxRequestIDKey, rid,
			"age", age,
			"threshold", m.stalenessThreshold,
		)
	default:
		slog.Error("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)",
			"last_insert_at", lastFailure,
			"last_request_at", lastRequest,
			"source_max_failure_id", fid,
			sourceMaxRequestIDKey, rid,
			"age", age,
			"threshold", m.stalenessThreshold,
		)
	}
	m.fireAlert(alert)
	return nil
}

// stalenessVerdict is the per-tick classification returned by
// candidateFailureMonitorClassifyStale. Exposed as a typed enum so the
// classifier stays a pure function and the test (in
// candidate_failure_monitor_s4_stop_test.go) can pin both the binary outcome
// (warn vs error) and the regression that motivated the split.
type stalenessVerdict int

const (
	// stalenessWithinBaseline: gateway active + failure writer recently wrote;
	// the staleness predicate should not have fired in the first place.
	// Defensive default — the caller should not invoke the classifier in
	// this state, so this constant is only reachable from tests.
	stalenessWithinBaseline stalenessVerdict = iota
	// stalenessLikelyStopWrite: failure log is stale but the writer was
	// previously active (source_max_failure_id > 0). The most likely cause
	// is intentional stop-write (S4 退役 / ops hold). Log at WARN.
	stalenessLikelyStopWrite
	// stalenessWriterNeverWrote: failure log is stale AND source_max_failure_id
	// is 0 (or the underlying table never had a row). The most likely cause
	// is a real writer-side break (schema drift / replica down / wrong pool).
	// Log at ERROR.
	stalenessWriterNeverWrote
)

// sourceMaxRequestIDKey is the structured log field name for the request
// surface's max id, mirroring source_max_failure_id on the failure side. The
// two fields are emitted together so readers can correlate "is the request
// side still producing" against "did the failure side ever produce".
const sourceMaxRequestIDKey = "source_max_request_id"

// candidateFailureMonitorClassifyStale decides whether a stale failure-log
// observation is more likely an intentional stop-write (S4 退役) or a real
// writer-side break.
//
// It is a pure function so the S4-stop scenario (writer previously active,
// now frozen) and the never-wrote scenario (writer broken from boot) can be
// pinned by a unit test without spinning up a DB.
//
// ⚠ 已知有偏：当 stalenessThreshold 短于「gateway 从启动到首次失败」的最大
// 间隔时，分类会把「刚启动、还没失败过」误判为 stalenessWriterNeverWrote。
// 默认阈值 30 分钟 + 真实生产常在前几分钟就有失败 ⇒ 在默认配置下偏判的
// 概率低；运营上有「先静默启动再接流量」的部署节奏时需调大阈值。
func candidateFailureMonitorClassifyStale(failureSourceMaxID int64) stalenessVerdict {
	if failureSourceMaxID > 0 {
		return stalenessLikelyStopWrite
	}
	return stalenessWriterNeverWrote
}

func failureLogIsStale(lastFailure, lastRequest *time.Time, now time.Time, threshold time.Duration) bool {
	if lastRequest == nil {
		return false
	}
	return lastFailure == nil || now.Sub(*lastFailure) >= threshold
}

// checkAlerts scans candidate_failure_logs over the alert window and
// fires an alert for any (credential, model, error_kind) tuple whose
// count >= alertThresh. Debounced via alertLastFired so a single outage
// doesn't spam the log every interval.
func (m *CandidateFailureMonitor) checkAlerts(ctx context.Context) error {
	since := time.Now().Add(-m.alertWindow)

	rows, err := m.db.Query(ctx, `
		SELECT
			credential_id, provider_id, raw_model_name, error_kind,
			COUNT(*) AS cnt,
			COUNT(DISTINCT upstream_status_code) AS distinct_codes,
			(SELECT upstream_response_preview
			   FROM candidate_failure_logs_with_current_month c2
			   WHERE c2.credential_id = c.credential_id
			     AND c2.raw_model_name = c.raw_model_name
			     AND c2.error_kind = c.error_kind
			   ORDER BY ts DESC LIMIT 1) AS last_body
		FROM candidate_failure_logs_with_current_month c
		WHERE ts >= $1
		GROUP BY credential_id, provider_id, raw_model_name, error_kind
		HAVING COUNT(*) >= $2
	`, since, m.alertThresh)
	if err != nil {
		return err
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var a CandidateFailureAlert
		var lastBody *string
		if err := rows.Scan(
			&a.CredentialID, &a.ProviderID, &a.RawModelName, &a.ErrorKind,
			&a.Count, &a.DistinctCodes, &lastBody,
		); err != nil {
			slog.Warn("candidate_failure_monitor: scan alert row", "error", err)
			continue
		}
		a.Ts = now
		a.WindowSec = int(m.alertWindow.Seconds())
		if lastBody != nil {
			a.LastBody = *lastBody
		}

		key := alertKey(a.CredentialID, a.RawModelName, a.ErrorKind)
		if m.shouldFire(key, now) {
			m.fireAlert(a)
		}
	}
	// R66: 聚合分组被截断 = 少发若干告警，监控面看不到。
	if err := rows.Err(); err != nil {
		slog.Warn("candidate_failure_monitor: alert row iteration aborted; batch truncated",
			"error", err, "window_sec", int(m.alertWindow.Seconds()))
	}
	return nil
}

// checkAutoCool looks for credentials whose recent attempts are mostly
// failing. When failure_ratio >= coolRatio AND sample_count >= coolMinSamples,
// the credential is flipped to availability_state='cooling' with
// recover_at = now() + coolMinutes. The recovery worker (credential_recovery.go)
// will restore 'ready' once recover_at passes — unless auto-cool fires again.
//
// 窗口契约（2026-10-13 252 审计轮）：本查询两条臂只读 hot 双写表，其语义
// 等价性成立的前提是窗口字面量（'5 minutes'）≤ hot 保留期——
// session_turns_hot / _request_logs_hot / candidate_failure_logs_hot 的
// promote retention 名义 8h（settings lifecycle.*，R16 fact 85），5 分钟窗内
// 的行只可能存在于 hot 侧，canonical 视图的分区腿与 v1 腿反连接在窗内恒
// 贡献空集（与 admin.slidingWindowHotQuery 的 ≤120min 闸门同一论证，R32
// 定版）。视图形态在本轮 54h 窗实测 ×1,548 条慢查、饱和期 avg 3.5s
// （auto-cool 每实例每 tick 都跑，是持续第一大自营慢查族）；hot 形态
// EXPLAIN ANALYZE 1.16ms（~93×），且在 252 生产库对拍 EXCEPT 双向差集
// 0/0（轮报告 §修复一）。⚠️ 若把窗口调大到保留期量级（≥8h），分区腿不再
// 为空，必须退回 canonical 视图形态——否则 attempts/fails 会静默漏掉已晋升行。
func (m *CandidateFailureMonitor) checkAutoCool(ctx context.Context) error {
	// Pull recent candidate_failure_logs + the request-log attempt count
	// for the same window. Two CTEs joined by credential.
	// 2026-09-11: the 5-minute attempt window must read the current-month
	// surface — on the bare parent (cold rows only) win was always empty, so
	// auto-cool never saw current traffic and never fired.
	// 2026-10-13 252 审计轮：再从 current-month 视图收窄到 hot 双写表
	// ——视图三臂（turns_hot ∪ turns 分区 ∪ v1 反连接）对 5 分钟窗只有
	// hot 臂有产出，分区臂的索引下降+反连接是纯税（见
	// autoCoolWindowSQLContract 头注）。两处镜像必须与部署视图逐字对齐：
	//   - session 腿的 request_status 派生 =
	//     db/request_logs_view_schema.go sessionRequestStatusExpr（824 定版，
	//     由 db/view_schema_v2_contract_test.go 钉住）；改动视图投影时同步这里。
	//   - v1 腿的反连接 = 视图 v1 臂的 NOT EXISTS(session_turns*)；
	//     窗口 ⊂ hot 保留期时对 turns_hot 反连接已足够（分区腿空集）。
	rows, err := m.db.Query(ctx, `
		WITH win AS (
		    SELECT credential_id,
		           COUNT(*) FILTER (
		               WHERE lower(COALESCE(request_status, '')) = 'failure'
		                 -- 2026-09-25: exclude node-probe failures from the
		                 -- auto-cool failure ratio. The probe worker hits every
		                 -- model on a credential (146 models on a multi-model
		                 -- provider), and transient probe errors (rate_limit,
		                 -- 5xx, timeout) were counted as user-request failures,
		                 -- cooling healthy credentials that had zero real-user
		                 -- failures. Probe results live in node_probe_state and
		                 -- drive per-model gating via v_routable_credential_models;
		                 -- they must not feed the credential-level auto-cool.
		                 AND COALESCE(error_kind, '') NOT LIKE 'probe_direct_%'
		           ) AS fails,
		           COUNT(*) AS attempts
		    FROM (
		        SELECT (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id,
		               (CASE WHEN t.success IS NULL THEN NULL
		                     WHEN t.success THEN 'success'
		                     WHEN t.error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited'
		                     WHEN t.status_code = 429 THEN 'rate_limited'
		                     ELSE 'failure' END) AS request_status,
		               t.error_kind
		        FROM session_turns_hot t
		        WHERE t.ts >= now() - interval '5 minutes'
		        -- ★ 890（v1 族退役）：原先的第二条臂（v1 hot 表 + 对会话族热腿的
		        -- 反连接）整条删除。v1 族被 DROP 后，同一条 FROM 里提到 v1 关系
		        -- 会让**整条语句** 42P01 ⇒ auto-cool 巡检整体挂掉。
		        -- 删除不损失口径：那条臂在停写后本就恒空（v1 hot 冻结）。
		    ) cand
		    WHERE credential_id IS NOT NULL
		    GROUP BY credential_id
		),
		cfl AS (
		    SELECT credential_id, COUNT(*) AS recent_failures
		    -- 2026-09-12: current-month surface — the bare parent's 5-minute
		    -- window was always empty (writer feeds the hot table), so
		    -- recent_failures was pinned at 0.
		    -- 2026-10-13: 直读 hot 表（promote 是「先拷贝后延迟删」，5 分钟窗
		    -- 行只可能在 hot 侧；视图分区腿窗内恒空）。
		    FROM candidate_failure_logs_hot
		    WHERE ts >= now() - interval '5 minutes'
		    GROUP BY credential_id
		)
		SELECT w.credential_id, w.attempts, w.fails, COALESCE(c.recent_failures, 0)
		FROM win w
		LEFT JOIN cfl c ON c.credential_id = w.credential_id
		WHERE w.attempts >= $1
		  AND w.fails::float / w.attempts >= $2
	`, m.coolMinSamples, m.coolRatio)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var credID, attempts, fails, recentFails int
		if err := rows.Scan(&credID, &attempts, &fails, &recentFails); err != nil {
			if dbrows.SkipOrFail("bg.CandidateFailureMonitor.checkAutoCool", err) {
				continue
			}
		}
		ratio := float64(fails) / float64(attempts)
		slog.Warn("candidate_failure_monitor: auto-cool trigger",
			"credential_id", credID,
			"attempts_5m", attempts,
			"fails_5m", fails,
			"failure_ratio", ratio,
			"candidate_failure_logs_count_5m", recentFails,
		)
		if err := m.applyAutoCool(ctx, credID); err != nil {
			slog.Warn("candidate_failure_monitor: applyAutoCool failed",
				"credential_id", credID, "error", err)
		}
	}
	// R66: 被截断 = 少冷却若干本该冷却的 credential，失败率会继续冲高。
	if err := rows.Err(); err != nil {
		slog.Warn("candidate_failure_monitor: auto-cool row iteration aborted; batch truncated",
			"error", err)
	}
	return nil
}

// applyAutoCool flips a credential to availability_state='cooling' with
// a short recover_at. Only cools active credentials; manual_disable and
// lifecycle_status are preserved.
func (m *CandidateFailureMonitor) applyAutoCool(ctx context.Context, credID int) error {
	tag, err := m.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'cooling',
		    availability_recover_at = now() + ($1 || ' minutes')::interval,
		    state_reason_code = 'auto_cool_high_failure_rate',
		    state_reason_detail = 'candidate_failure_monitor: high failure rate detected',
		    state_updated_at = now()
		WHERE id = $2
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND availability_state NOT IN ('cooling')
		  -- R21 (2026-09-13) audit P2: never steal reason ownership from the
		  -- balance-floor guard — rewriting a floor-pulled row to
		  -- auto_cool_high_failure_rate re-enables the probe/write-through
		  -- un-pull path and re-creates the pull/un-pull ping-pong the
		  -- ownership invariant exists to prevent.
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'
	`, m.coolMinutes, credID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		slog.Info("candidate_failure_monitor: credential cooled",
			"credential_id", credID,
			"cool_minutes", m.coolMinutes,
		)
	}
	return nil
}

// shouldFire returns true when no alert has fired for this key within
// the cooldown window. The map is locked briefly to keep concurrent
// ticks consistent.
func (m *CandidateFailureMonitor) shouldFire(key string, now time.Time) bool {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	last, ok := m.alertLastFired[key]
	if !ok || now.Sub(last) >= m.alertCooldown {
		m.alertLastFired[key] = now
		return true
	}
	return false
}

// fireAlert logs the alert, appends to the in-memory ring buffer, and
// posts to the optional webhook. The webhook call is best-effort with a
// 5s timeout; a failure is logged but does not affect the alert state.
func (m *CandidateFailureMonitor) fireAlert(a CandidateFailureAlert) {
	slog.Warn("candidate_failure_alert",
		"credential_id", a.CredentialID,
		"provider_id", a.ProviderID,
		"raw_model_name", a.RawModelName,
		"error_kind", a.ErrorKind,
		"count_in_window", a.Count,
		"window_sec", a.WindowSec,
		"distinct_status_codes", a.DistinctCodes,
		"last_response_preview", truncateStr(a.LastBody, 200),
	)

	m.alertMu.Lock()
	m.recentAlerts = append(m.recentAlerts, a)
	if len(m.recentAlerts) > 200 {
		// Drop oldest 50 to bound memory on a sustained outage.
		m.recentAlerts = m.recentAlerts[len(m.recentAlerts)-200:]
	}
	m.alertMu.Unlock()

	if m.webhookURL == "" {
		return
	}
	Go("candidate_failure_monitor.postWebhook", func() { m.postWebhook(a) })
}

func (m *CandidateFailureMonitor) postWebhook(a CandidateFailureAlert) {
	body, err := json.Marshal(a)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.webhookURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("candidate_failure_monitor: webhook post failed",
			"url", m.webhookURL, "error", err)
		return
	}
	//nolint:errcheck // best-effort close
	resp.Body.Close()
}

// RecentAlerts returns a snapshot of the in-memory alert ring buffer.
// Safe to call from any goroutine; the underlying slice is copied.
func (m *CandidateFailureMonitor) RecentAlerts() []CandidateFailureAlert {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	out := make([]CandidateFailureAlert, len(m.recentAlerts))
	copy(out, m.recentAlerts)
	return out
}

func alertKey(credID int, model, kind string) string {
	return strconv.Itoa(credID) + "|" + model + "|" + kind
}

func parseDurationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func parseIntEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func parseFloatEnv(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
