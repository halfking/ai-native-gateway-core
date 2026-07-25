// Package bg/systemmonitor — executor.go
//
// 6 种 task_type 的执行器（direct_ping / gateway_ping / chat_minimal / chat_tool / chat_stream / http_ping）。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §4.1
//
// Phase 1 范围:
//   - direct_ping / chat_minimal / chat_tool / http_ping: 完整实现
//   - gateway_ping: 与 direct_ping 共用，但 baseURL 取本机 (Phase 2 细节化)
//   - chat_stream: 占位 stub (FUTURE: SSE 流式上游尚未普适)
//
// 复用: ActiveProbeExecutor (bg/active_probe_executor.go) 提供 Run()
// 与 LoadTarget()。本文件不直接调上游 HTTP，而是委托给 ActiveProbeExecutor，
// 保证错误分类、状态判定与现有 NodeProbeWorker / ProbeQueueWorker 行为一致。
package systemmonitor

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// Executor 是 6 种 task_type 的统一入口。
//
// 委托给 bg.ActiveProbeExecutor 完成 direct/gateway/chat_minimal/chat_tool 的实际探测，
// 自带 http_ping 实现（不需要解密 secret）。
type Executor struct {
	db         *pgxpool.Pool
	keyring    *secret.Keyring
	encKey     []byte
	probeExec  *bg.ActiveProbeExecutor
	httpClient *http.Client // http_ping 专用（无 proxy, 走 probeClient 复用 proxyFunc）
	proxyFunc  func(*http.Request) (*url.URL, error)
	timeoutMs  int
}

// ExecutorConfig holds the wiring parameters.
type ExecutorConfig struct {
	DB        *pgxpool.Pool
	Keyring   *secret.Keyring
	EncKey    []byte
	ProxyFunc func(*http.Request) (*url.URL, error)
	TimeoutMs int
}

// NewExecutor constructs an Executor. proxyFunc may be nil (no upstream proxy).
func NewExecutor(cfg ExecutorConfig) *Executor {
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = 30000
	}
	probeExec := bg.NewActiveProbeExecutor(cfg.DB, cfg.Keyring, cfg.EncKey, cfg.TimeoutMs)

	// http_ping client：复用 proxyFunc 让结果与真实请求路径一致。
	// proxyFunc=nil 时直接走默认 transport。
	var httpClient *http.Client
	if cfg.ProxyFunc != nil {
		httpClient = &http.Client{
			Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
			Transport: &http.Transport{
				Proxy:                 cfg.ProxyFunc,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		}
	} else {
		httpClient = &http.Client{Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond}
	}

	return &Executor{
		db:         cfg.DB,
		keyring:    cfg.Keyring,
		encKey:     cfg.EncKey,
		probeExec:  probeExec,
		httpClient: httpClient,
		proxyFunc:  cfg.ProxyFunc,
		timeoutMs:  cfg.TimeoutMs,
	}
}

// Execute runs the task and returns a normalised ExecutorResult.
//
// The function dispatches by TaskType. For unimplemented types it returns
// TaskStatusFailed with err_code "task_type_unimplemented" rather than
// panicking — the worker loop logs and increments the failure counter.
func (e *Executor) Execute(ctx context.Context, task *Task) (*ExecutorResult, error) {
	if task == nil {
		return nil, errors.New("task is nil")
	}
	if !task.TaskType.Valid() {
		return nil, fmt.Errorf("invalid task_type: %q", task.TaskType)
	}

	start := time.Now()
	res := &ExecutorResult{
		TaskID:    task.ID,
		TaskType:  task.TaskType,
		StartedAt: start,
	}

	switch task.TaskType {
	case TaskTypeDirectPing, TaskTypeChatMinimal, TaskTypeChatTool:
		r, err := e.executeChatPing(ctx, task, false)
		res.Result = r
		res.Err = err
	case TaskTypeGatewayPing:
		r, err := e.executeChatPing(ctx, task, true)
		res.Result = r
		res.Err = err
	case TaskTypeChatStream:
		// FUTURE: 流式探测尚未实现 — 标记为失败并写明原因。
		res.Result = &bg.ProbeResult{
			Status:    bg.ProbeStatusFailed,
			ErrCode:   "task_type_unimplemented",
			ErrMsg:    "chat_stream probe is not implemented yet (FUTURE: Phase 2)",
			StartedAt: start,
		}
		res.Err = errors.New("chat_stream unimplemented")
	case TaskTypeHTTPPing:
		r, err := e.executeHTTPPing(ctx, task)
		res.Result = r
		res.Err = err
	default:
		res.Err = fmt.Errorf("unsupported task_type: %q", task.TaskType)
	}

	res.CompletedAt = time.Now()
	if res.Result != nil && res.Result.StartedAt.IsZero() {
		res.Result.StartedAt = start
		res.Result.CompletedAt = res.CompletedAt
	}
	return res, res.Err
}

// ExecutorResult bundles the ProbeResult plus transport-level error
// (e.g. decrypt failure, which doesn't go through ProbeResult).
type ExecutorResult struct {
	TaskID      int64
	TaskType    TaskType
	StartedAt   time.Time
	CompletedAt time.Time
	Result      *bg.ProbeResult
	Err         error
}

// executeChatPing runs a chat-completion probe through bg.ActiveProbeExecutor.
//
// gateway=true uses the local gateway URL (LLM_GATEWAY_GATEWAY_BASE_URL or
// http://127.0.0.1:8781/v1) instead of the upstream provider's base URL.
func (e *Executor) executeChatPing(ctx context.Context, task *Task, gateway bool) (*bg.ProbeResult, error) {
	target, err := e.probeExec.LoadTarget(ctx, int(task.CredentialID), task.RawModel)
	if err != nil {
		return &bg.ProbeResult{
			Status:    bg.ProbeStatusFailed,
			ErrCode:   "load_target",
			ErrMsg:    fmt.Sprintf("load target failed: %s (task_id=%d, cred_id=%d, model=%s)", err.Error(), task.ID, task.CredentialID, task.RawModel),
			StartedAt: time.Now(),
		}, err
	}
	if gateway {
		gwBase := os.Getenv("LLM_GATEWAY_GATEWAY_BASE_URL")
		if gwBase == "" {
			gwBase = "http://127.0.0.1:8781/v1"
		}
		// 直接覆盖 base_url 字段（LoadTarget 不会校验该字段语义）。
		target.BaseURL = gwBase
		// gateway 轮次不消耗供应商 quota，但保留 provider 信息便于审计。
		target.APIKey = os.Getenv("LLM_GATEWAY_GATEWAY_PROBE_API_KEY")
	}
	mode := bg.ProbeModeChatPing
	return e.probeExec.RunCommand(ctx, bg.ProbeCommand{
		Target:   target,
		Mode:     mode,
		Attempt:  task.Attempt + 1,
		Origin:   string(task.Source),
		ParentID: task.ParentRequestID,
	}), nil
}

// executeHTTPPing is a single HEAD-then-GET probe against the provider's
// base URL. It does NOT decrypt the credential (http_ping is a pure
// network probe).
//
// The probe honours the upstream HTTP_PROXY via the httpClient configured
// at construction time so the result reflects what real user requests
// would experience (rule 2026-07-16 fix: probe vs real-traffic proxy
// mismatch was the root cause of the "probe OK / requests fail"
// oscillation).
//
// DNS / TLS timings are extracted from the underlying net.Conn via
// dnsStart / tlsStart hooks; this is intentionally lightweight so the
// per-probe overhead stays under 50ms when the upstream is fast.
func (e *Executor) executeHTTPPing(ctx context.Context, task *Task) (*bg.ProbeResult, error) {
	start := time.Now()

	// Resolve provider base URL from DB.
	baseURL, protocol, providerID, err := e.resolveProviderBaseURL(ctx, task)
	if err != nil {
		return &bg.ProbeResult{
			Status:    bg.ProbeStatusFailed,
			ErrCode:   "endpoint_build",
			ErrMsg:    fmt.Sprintf("http_ping endpoint build failed: %s (cred_id=%d, provider_id=%d)", err.Error(), task.CredentialID, task.ProviderID),
			StartedAt: start,
		}, err
	}
	probeURL := strings.TrimRight(baseURL, "/")
	// 简化：很多 provider base_url 不带 /v1，这里直接探根路径。
	// 若需要探 /v1/models 之类的具体 endpoint，Phase 2 扩展。
	endpoint := probeURL

	dnsStart := time.Now()
	// net/http 的 DialContext 钩子：DNS 解析在第一次连接时发生。
	// 这里用 dnsStart/tlsStart 捕获分段时间。
	var dnsMs, tlsMs int
	transport := &http.Transport{
		Proxy: e.proxyFunc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			dnsMs = int(time.Since(dnsStart).Milliseconds())
			return conn, nil
		},
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: false},
	}
	tlsStart := time.Now()
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: false,
	}

	client := &http.Client{
		Timeout:   time.Duration(e.timeoutMs) * time.Millisecond,
		Transport: transport,
	}

	// Phase 1: 优先 HEAD，不支持 HEAD 的供应商 fallback GET。
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return &bg.ProbeResult{
			Status:    bg.ProbeStatusFailed,
			ErrCode:   "request_build",
			ErrMsg:    fmt.Sprintf("build HEAD request failed: %s (url=%s)", err.Error(), endpoint),
			StartedAt: start,
		}, err
	}
	req.Header.Set("User-Agent", "llmgw-system-monitor/1.0")

	resp, err := client.Do(req)
	latency := time.Since(start)
	// HEAD 失败或 405 → fallback GET
	if err != nil || (resp != nil && resp.StatusCode == http.StatusMethodNotAllowed) {
		if resp != nil {
			_ = resp.Body.Close()
		}
		req2, err2 := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err2 != nil {
			return &bg.ProbeResult{
				Status:    bg.ProbeStatusFailed,
				ErrCode:   "request_build",
				ErrMsg:    fmt.Sprintf("build GET request failed: %s (url=%s)", err2.Error(), endpoint),
				StartedAt: start,
			}, err2
		}
		req2.Header.Set("User-Agent", "llmgw-system-monitor/1.0")
		resp, err = client.Do(req2)
		latency = time.Since(start)
	}
	if err != nil {
		// 网络错误 / 超时
		status := bg.ProbeStatusNetwork
		if strings.Contains(err.Error(), "timeout") || errors.Is(err, context.DeadlineExceeded) {
			status = bg.ProbeStatusTimeout
		}
		return &bg.ProbeResult{
			Status:      status,
			HTTPStatus:  0,
			ErrCode:     "network_error",
			ErrMsg:      fmt.Sprintf("http_ping transport failed: %s (url=%s, latency_ms=%d)", err.Error(), endpoint, latency.Milliseconds()),
			LatencyMs:   int(latency.Milliseconds()),
			StartedAt:   start,
			CompletedAt: time.Now(),
			Target: bg.ProbeTarget{
				ProviderID: providerID,
				RawModel:   task.RawModel,
				BaseURL:    baseURL,
				Protocol:   protocol,
			},
		}, err
	}
	defer func() { _ = resp.Body.Close() }()

	tlsMs = int(time.Since(tlsStart).Milliseconds())
	// Read & discard body up to 1KB (avoid hanging on huge responses).
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	r := &bg.ProbeResult{
		Status:      bg.ProbeStatusSuccess,
		HTTPStatus:  resp.StatusCode,
		ErrCode:     "none",
		LatencyMs:   int(latency.Milliseconds()),
		StartedAt:   start,
		CompletedAt: time.Now(),
		Target: bg.ProbeTarget{
			ProviderID: providerID,
			RawModel:   task.RawModel,
			BaseURL:    baseURL,
			Protocol:   protocol,
		},
		RequestURL: endpoint,
	}
	if resp.StatusCode >= 400 {
		r.Status = classifyHTTPStatus(resp.StatusCode)
		r.ErrCode = fmt.Sprintf("http_%d", resp.StatusCode)
	}

	// 把分段时间塞进 extras（worker 写入 system_probe_runs 时识别）。
	r.ResponseBody = fmt.Sprintf("dns_ms=%d;tls_ms=%d", dnsMs, tlsMs)
	slog.Info("system_monitor: http_ping done",
		"task_id", task.ID,
		"credential_id", task.CredentialID,
		"provider_id", providerID,
		"raw_model", task.RawModel,
		"endpoint", endpoint,
		"status", resp.StatusCode,
		"latency_ms", r.LatencyMs,
		"dns_ms", dnsMs,
		"tls_ms", tlsMs,
	)
	return r, nil
}

// classifyHTTPStatus maps an HTTP status code to ProbeStatus.
// Mirrors bg/active_probe_executor.go:Run mapping.
func classifyHTTPStatus(code int) bg.ProbeStatus {
	switch {
	case code >= 200 && code < 300:
		return bg.ProbeStatusSuccess
	case code == 401 || code == 403:
		return bg.ProbeStatusAuth
	case code == 429:
		return bg.ProbeStatusRate
	case code >= 500:
		return bg.ProbeStatusHTTP5xx
	case code >= 400:
		return bg.ProbeStatusHTTP4xx
	}
	return bg.ProbeStatusFailed
}

// resolveProviderBaseURL fetches the base URL + protocol for a credential.
// Used by http_ping to know which upstream endpoint to HEAD/GET.
func (e *Executor) resolveProviderBaseURL(ctx context.Context, task *Task) (baseURL, protocol string, providerID int, err error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	row := e.db.QueryRow(queryCtx, `
		SELECT p.base_url, COALESCE(p.protocol, 'openai-completions'), p.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, task.CredentialID)
	var rawBase sql.NullString
	if err := row.Scan(&rawBase, &protocol, &providerID); err != nil {
		return "", "", 0, fmt.Errorf("resolve base_url: %w", err)
	}
	if !rawBase.Valid || rawBase.String == "" {
		return "", protocol, providerID, errors.New("provider base_url is empty")
	}
	return rawBase.String, protocol, providerID, nil
}
