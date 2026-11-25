package promptinjection

// error_discovery.go — 请求错误发现器：把「请求过程中发生的错误」送进 PI 分析（2026-11-25 取证轮）。
//
// ★ owner 需求：「请求的过程中，如果发生错误，需要能调用 pi 自动检索服务的
//
//	log 及请求信息，进行检查分析」。
//
// ★ 为什么是「读表」而不是「挂请求路径」：
//
//	请求在上游的每次失败尝试，网关自己已经异步落进 candidate_failure_logs_hot
//	（candidate_failure_logger，带 request_id / session_id / 供应商 / 模型 /
//	状态码）。发现器只读这张**已经落库**的表 ⇒ 对请求热路径零改动、零延迟。
//	（往 7000 行的流式处理器热路径里加新生产者，风险与收益不成比例。）
//
// ★ 防噪三道闸（211 实测一小时 1400+ 条候选失败，不设闸会把队列冲垮）：
//
//	① 窗口内出现次数 ≥ 阈值（默认 5 次）——偶发的单次失败不值得调一次模型；
//	② 按 (错误类型, 供应商, 模型) 分组，**每组只入队一条样本**；采集器的去重
//	   指纹按同一分组算（见 calculateErrorHash）⇒ 同一组 24 小时内只分析一次；
//	③ 客户端主动取消类（canceled）直接排除——那不是故障（245 实测 10 条
//	   「canary 检测失败」全是 context canceled，每条却都触发了一次模型分析）。
//
// ★ 多实例安全：245 与 154 共享一个库、各跑一个发现器 ⇒ 两边会同时发现同一
//
//	组；采集器的「事务内 advisory lock + 查重」保证同一指纹只入队一次。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DiscoveryConfig 是发现器的运行配置（装配侧每轮从 settings 读出注入，热生效）。
type DiscoveryConfig struct {
	Enabled     bool
	Threshold   int           // 窗口内至少出现多少次
	Window      time.Duration // 统计窗口
	MaxPerRound int           // 单轮最多入队几组
}

// DefaultDiscoveryConfig 是默认配置。
func DefaultDiscoveryConfig() DiscoveryConfig {
	return DiscoveryConfig{Enabled: true, Threshold: 5, Window: time.Hour, MaxPerRound: 5}
}

const (
	discoveryMinWindow    = 5 * time.Minute
	discoveryMaxWindow    = 24 * time.Hour
	discoveryMaxPerRound  = 20
	discoveryQueryTimeout = 15 * time.Second
)

// clientCanceledKinds 是「调用方主动取消」类的错误类型——不是故障，不入队。
//
//	键是 candidate_failure_logs_hot.error_kind 的字面值；写成内联字面量而不是
//	数组参数（pgx stdlib 对 text[] 的绑定/扫描有坑，见 errorplaybook/worker.go 的记述）。
var clientCanceledKinds = []string{"canceled", "client_canceled", "context_canceled", "client_closed", "aborted", "client_cancel"}

// failureGroup 是窗口内的一个 (错误类型, 供应商, 模型) 分组。
type failureGroup struct {
	kind, provider, model string
	count                 int64
	last                  time.Time
}

// RequestErrorDiscoverer 周期性地把达到阈值的请求失败分组入队。
type RequestErrorDiscoverer struct {
	db        *sql.DB
	collector *ErrorCollector
	cfgFn     func() DiscoveryConfig
}

// NewRequestErrorDiscoverer 创建发现器。cfgFn 可为 nil（用默认配置）。
func NewRequestErrorDiscoverer(db *sql.DB, collector *ErrorCollector, cfgFn func() DiscoveryConfig) *RequestErrorDiscoverer {
	return &RequestErrorDiscoverer{db: db, collector: collector, cfgFn: cfgFn}
}

func discoveryExcludedKindsSQL() string {
	quoted := make([]string, 0, len(clientCanceledKinds))
	for _, k := range clientCanceledKinds {
		quoted = append(quoted, "'"+strings.ReplaceAll(k, "'", "''")+"'")
	}
	return strings.Join(quoted, ", ")
}

// discoveryGroupsSQL 按 (错误类型, 供应商, 模型) 聚合窗口内的候选失败。
//
//	★ provider_id 是 INT NOT NULL ⇒ `provider_id::text`（不能 COALESCE 成 ''——
//	  PG 会把 '' 强转 integer 报 22P02；errorplaybook 的同款坑）。
const discoveryGroupsSQL = `
	SELECT error_kind,
	       provider_id::text,
	       COALESCE(raw_model_name, ''),
	       COUNT(*),
	       MAX(ts)
	FROM candidate_failure_logs_hot
	WHERE ts > NOW() - $1::interval
	  AND error_kind IS NOT NULL AND error_kind <> ''
	  AND error_kind NOT IN (%s)
	GROUP BY error_kind, provider_id, raw_model_name
	HAVING COUNT(*) >= $2
	ORDER BY COUNT(*) DESC, MAX(ts) DESC
	LIMIT $3`

const discoverySampleSQL = `
	SELECT COALESCE(request_id, ''),
	       COALESCE(session_id, ''),
	       COALESCE(tenant_id, ''),
	       COALESCE(error_message, ''),
	       COALESCE(extracted_upstream_status_code, upstream_status_code, 0),
	       COALESCE(retryable, false),
	       COALESCE(latency_ms, 0),
	       COALESCE(attempt_index, 0),
	       COALESCE(credential_id::text, ''),
	       ts
	FROM candidate_failure_logs_hot
	WHERE ts > NOW() - $1::interval
	  AND error_kind = $2
	  AND provider_id::text = $3
	  AND COALESCE(raw_model_name, '') = $4
	ORDER BY ts DESC
	LIMIT 1`

// RunOnce 跑一轮发现；返回本轮交给采集器的分组数（查重之后实际入库的可能更少）。
func (d *RequestErrorDiscoverer) RunOnce(ctx context.Context) (int, error) {
	if d == nil || d.db == nil || d.collector == nil {
		return 0, nil
	}
	cfg := DefaultDiscoveryConfig()
	if d.cfgFn != nil {
		cfg = d.cfgFn()
	}
	if !cfg.Enabled {
		return 0, nil
	}
	threshold := cfg.Threshold
	if threshold < 1 {
		threshold = 1
	}
	window := cfg.Window
	if window < discoveryMinWindow {
		window = discoveryMinWindow
	}
	if window > discoveryMaxWindow {
		window = discoveryMaxWindow
	}
	limit := cfg.MaxPerRound
	if limit < 1 {
		limit = 1
	}
	if limit > discoveryMaxPerRound {
		limit = discoveryMaxPerRound
	}

	ctx, cancel := context.WithTimeout(ctx, discoveryQueryTimeout)
	defer cancel()
	windowArg := fmt.Sprintf("%d seconds", int(window.Seconds()))

	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(discoveryGroupsSQL, discoveryExcludedKindsSQL()), windowArg, threshold, limit)
	if err != nil {
		if isTableNotFound(err, "candidate_failure_logs_hot") {
			slog.Warn("pi_request_error_discovery: candidate_failure_logs_hot not provisioned; skipping", "error", err)
			return 0, nil
		}
		return 0, fmt.Errorf("pi_request_error_discovery: query failure groups: %w", err)
	}
	var groups []failureGroup
	for rows.Next() {
		var g failureGroup
		if err := rows.Scan(&g.kind, &g.provider, &g.model, &g.count, &g.last); err != nil {
			rows.Close()
			return 0, fmt.Errorf("pi_request_error_discovery: scan failure group: %w", err)
		}
		groups = append(groups, g)
	}
	// ★ rows 终检：中途出错时把「读了一半的分组」当全部 ⇒ 静默漏掉错误。
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("pi_request_error_discovery: iterate failure groups: %w", err)
	}
	rows.Close()
	if len(groups) == 0 {
		return 0, nil
	}

	enqueued := 0
	for _, g := range groups {
		ce, err := d.sample(ctx, windowArg, g, int(window.Minutes()))
		if err != nil {
			slog.Warn("pi_request_error_discovery: sample lookup failed",
				"error_kind", g.kind, "provider_id", g.provider, "model", g.model, "error", err)
			continue
		}
		if ce == nil {
			continue
		}
		d.collector.CollectAsync(ctx, ce)
		enqueued++
	}
	if enqueued > 0 {
		slog.Info("pi_request_error_discovery: groups handed to collector", "groups", enqueued, "window", window.String())
	}
	return enqueued, nil
}

// sample 取一个分组里最近的一条失败记录，转成待入队的 CollectedError。
func (d *RequestErrorDiscoverer) sample(ctx context.Context, windowArg string, g failureGroup, windowMinutes int) (*CollectedError, error) {
	var (
		requestID  string
		sessionID  sql.NullString
		tenantID   string
		message    sql.NullString
		statusCode sql.NullInt64
		retryable  sql.NullBool
		latencyMs  sql.NullInt64
		attemptIdx sql.NullInt64
		credential sql.NullString
		ts         time.Time
	)
	err := d.db.QueryRowContext(ctx, discoverySampleSQL, windowArg, g.kind, g.provider, g.model).
		Scan(&requestID, &sessionID, &tenantID, &message, &statusCode, &retryable,
			&latencyMs, &attemptIdx, &credential, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // 窗口边界上行被搬走/清理了 ⇒ 本轮跳过
	}
	if err != nil {
		return nil, fmt.Errorf("query sample row: %w", err)
	}

	if tenantID == "" {
		tenantID = defaultTenantID
	}
	msg := strings.TrimSpace(message.String)
	ce := &CollectedError{
		ErrorType: ErrorTypeRequestFailed,
		TenantID:  tenantID,
		SessionID: sessionID.String,
		RequestID: requestID,
		// 不带输入原文：请求失败的分析材料是账本行 / 上游尝试 / 服务日志（分析前取证）。
		ErrorMessage: fmt.Sprintf("%s: %s", g.kind, truncateRunesSafe(msg, 200)),
		ErrorDetails: map[string]interface{}{
			"error_kind":           g.kind,
			"provider_id":          g.provider,
			"model":                g.model,
			"occurrences":          g.count,
			"window_minutes":       windowMinutes,
			"last_seen":            g.last.UTC().Format(time.RFC3339),
			"upstream_status_code": statusCode.Int64,
			"retryable":            retryable.Bool,
			"sample_latency_ms":    latencyMs.Int64,
			"sample_attempt_index": attemptIdx.Int64,
			"credential_id":        credential.String,
			"sample_ts":            ts.UTC().Format(time.RFC3339),
		},
		Priority: 5,
		Metadata: map[string]interface{}{
			"source": "request_error_discovery",
		},
	}
	if !retryable.Valid || !retryable.Bool {
		ce.Priority = 6 // 不可重试的失败更值得先看
	}
	return ce, nil
}
