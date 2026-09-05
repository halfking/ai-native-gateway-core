// Package main — lite_telemetry_sink.go
//
// lite 模式 telemetry 请求日志 sink（2026-09-05 审计 B2 接线）。
//
// 背景：lite 部署跳过 PostgreSQL 后 telemetry.Client 的 Enabled() 恒为
// false，EmitRequestLog 全部短路，请求日志/会话内容完全不落盘；存储工厂
// 的五个 store（SQLite Session/Turns/RequestLog + FileBodies + MemoryState）
// 零生产调用方。本文件把工厂中的四个 store（RequestLog/Session/Turns/
// Bodies）经 telemetry.RequestLogSink 注入缝接入生产读写路径：
//
//   - 每条 telemetry 条目（in_progress INSERT 与终态/回填 UPDATE）幂等
//     UPSERT 到 SQLite request_logs；
//   - 终态且携带 GwSessionID 与请求/响应 body 的条目额外记一“轮”会话
//     journal：sessions 行（首次创建）+ session_turns 元数据 + FileBodies
//     的 turn_N.json.gz 原文文件（gzip）。
//
// 与 full 模式（PG sessionv2mirror 影子写）的语义差异：lite journal 的
// 轮号是按 (tenant, session) 的持久递增序号（重启后从已落盘 body 文件的
// 最大 turn+1 续排），不做 V2 的 submit-mode/去重推导——lite 面向单机
// 审计与回放，不参与双写对账。StateStore（MemoryStateStore）为进程内 KV，
// 无天然生产消费者，仍未接线（沿既有记录项）。
//
// 生命周期：由 storageRuntime 持有工厂单例；telemetryClient.Stop()（排空
// 队列）先于 storageRuntime.Shutdown()（排空 bodies 异步写队列并关 SQLite），
// 顺序由 main.go 优雅关闭段既有顺序保证。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/storage"
)

// maxJournaledEntries 已记轮 request_id 去重集的容量上限（FIFO 淘汰），
// 防止长生命周期进程内存无界增长。超过窗口的重复条目最坏情况会为同一
// request_id 多记一轮（body 覆盖同名 turn 文件），不会产生脏数据。
const maxJournaledEntries = 10000

// liteRequestLogSink 是 telemetry.RequestLogSink 的 lite 工厂实现。
// 并发安全：telemetry worker 与队列打满时的同步直写路径都可能调用。
type liteRequestLogSink struct {
	sessions storage.SessionStore
	turns    storage.TurnsStore
	logs     storage.RequestLogStore
	bodies   storage.BodiesStore
	// lister 可选：body 侧的 turn 清单（FileBodiesStore 实现），用于重启后
	// 续排轮号。工厂返回的接口不保证实现它，缺失时退化为进程内计数。
	lister storage.BodiesLister

	mu          sync.Mutex
	nextTurn    map[string]int      // "tenant/session" → 下一轮号（1 起）
	journaled   map[string]struct{} // 已记轮的 request_id（有界）
	journalFIFO []string            // 淘汰顺序
}

// 编译期断言：lite sink 满足 telemetry 注入缝。
var _ telemetry.RequestLogSink = (*liteRequestLogSink)(nil)

// newLiteRequestLogSink 从 lite 存储工厂构造 telemetry sink。
// 各 store 均由工厂分派（SQLite 为无状态视图；Bodies/State 为工厂惰性
// 单例，由 factory.Close 统一优雅关闭）。
func (r *storageRuntime) newLiteRequestLogSink() *liteRequestLogSink {
	if r == nil || r.factory == nil {
		return nil
	}
	bodies := r.factory.NewBodiesStore()
	s := &liteRequestLogSink{
		sessions: r.factory.NewSessionStore(),
		turns:    r.factory.NewTurnsStore(),
		logs:     r.factory.NewRequestLogStore(),
		bodies:   bodies,
		nextTurn: make(map[string]int),
	}
	if lister, ok := bodies.(storage.BodiesLister); ok {
		s.lister = lister
	}
	return s
}

// PersistRequestLog 把一条 telemetry 请求日志条目持久化到 lite 存储：
//
//  1. UPSERT SQLite request_logs 行（in_progress → 终态 → 用量回填均覆盖
//     为最新值，Body 仅在确实落了 body 文件时置位 has_body）；
//  2. 终态 + 有 GwSessionID + 有请求/响应原文 + 未记过轮的条目，写一轮
//     会话 journal（session 行 + turn 元数据 + gzip body 文件）。
//
// 返回错误时由 telemetry flush 侧告警并计入 failPermanent，不阻塞请求路径。
func (s *liteRequestLogSink) PersistRequestLog(ctx context.Context, entry *telemetry.RequestLogEntry) error {
	if s == nil || entry == nil || entry.RequestID == "" {
		return nil
	}

	tenantID := entry.TenantID
	if tenantID == "" {
		tenantID = "default" // 与 telemetry insert/update 路径的 nonEmpty 兜底一致
	}
	sessionID := strPtrValue(entry.GwSessionID)
	eventAt := time.Now()
	if entry.EventAt != nil {
		eventAt = *entry.EventAt
	}

	reqBody := strPtrValue(entry.RequestBody)
	respBody := strPtrValue(entry.ResponseBody)
	journal := sessionID != "" &&
		liteEntryTerminal(entry) &&
		(reqBody != "" || respBody != "") &&
		!s.alreadyJournaled(entry.RequestID)

	// 1) request_logs 行
	row := &storage.RequestLog{
		RequestID:  entry.RequestID,
		TenantID:   tenantID,
		SessionID:  sessionID,
		Timestamp:  eventAt,
		Method:     "POST", // 网关业务端点均为 POST（chat/embeddings/responses）
		Path:       strPtrValue(entry.ClientEndpoint),
		StatusCode: liteEntryStatusCode(entry),
		Duration:   time.Duration(intPtrValue(entry.LatencyMs)) * time.Millisecond,
	}
	if journal {
		// has_body 仅在 body 文件确实会落盘时置位，避免悬空标记
		// （request_logs 行读回 Body 恒 nil，由 bodies store 按 session+turn 读）。
		row.Body = json.RawMessage("{}")
	}
	if err := s.logs.WriteRequest(ctx, row); err != nil {
		return fmt.Errorf("lite sink: request_logs %s: %w", entry.RequestID, err)
	}
	if !journal {
		return nil
	}

	// 2) 会话 journal：session 行 → body 文件 → turn 元数据。
	// 元数据在 body 文件成功落盘之后写，保证 ReconcileTurnArtifacts 的
	// “有 meta 必有 body”方向不出现缺 body 的孤儿元数据。
	if err := s.ensureSession(ctx, tenantID, sessionID, eventAt); err != nil {
		return fmt.Errorf("lite sink: session %s/%s: %w", tenantID, sessionID, err)
	}
	turnNo, err := s.nextTurnNo(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("lite sink: turn alloc %s/%s: %w", tenantID, sessionID, err)
	}
	body := &storage.SessionBody{
		TenantID:  tenantID,
		SessionID: sessionID,
		TurnNo:    turnNo,
		Timestamp: eventAt,
		Metadata: map[string]interface{}{
			"request_id": entry.RequestID,
			"success":    entry.Success,
		},
	}
	if reqBody != "" {
		body.Request = liteRawJSON([]byte(reqBody))
	}
	if respBody != "" {
		body.Response = liteRawJSON([]byte(respBody))
	}
	if err := s.bodies.Write(ctx, body); err != nil {
		return fmt.Errorf("lite sink: bodies turn %s/%s#%d: %w", tenantID, sessionID, turnNo, err)
	}
	if err := s.turns.WriteTurnMeta(ctx, &storage.TurnMeta{
		TenantID:            tenantID,
		SessionID:           sessionID,
		TurnNo:              turnNo,
		Timestamp:           eventAt,
		CompressionStrategy: strPtrValue(entry.CompressionStrategy),
		PromptTokens:        intPtrValue(entry.PromptTokens),
		CompletionTokens:    intPtrValue(entry.CompletionTokens),
	}); err != nil {
		return fmt.Errorf("lite sink: turn meta %s/%s#%d: %w", tenantID, sessionID, turnNo, err)
	}
	s.markJournaled(entry.RequestID)
	return nil
}

// ensureSession 保证 sessions 行存在：不存在则创建（首轮），存在则只触碰
// updated_at（保留既有 user_id/metadata，避免用空值覆盖）。
func (s *liteRequestLogSink) ensureSession(ctx context.Context, tenantID, sessionID string, at time.Time) error {
	existing, err := s.sessions.GetSession(ctx, tenantID, sessionID)
	switch {
	case err == nil:
		existing.UpdatedAt = at
		return s.sessions.UpdateSession(ctx, existing)
	case errors.Is(err, storage.ErrNotFound):
		return s.sessions.CreateSession(ctx, &storage.Session{
			ID:        sessionID,
			TenantID:  tenantID,
			CreatedAt: at,
			UpdatedAt: at,
		})
	default:
		return err
	}
}

// nextTurnNo 分配 (tenant, session) 的下一个轮号。首次分配时若能访问
// body 侧清单（BodiesLister），从已落盘 turn 的最大值 +1 续排，保证进程
// 重启后轮号不回退、不覆盖既有轮文件。
func (s *liteRequestLogSink) nextTurnNo(ctx context.Context, tenantID, sessionID string) (int, error) {
	key := tenantID + "/" + sessionID
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nextTurn[key]; ok {
		s.nextTurn[key] = n + 1
		return n, nil
	}
	next := 1
	if s.lister != nil {
		turns, err := s.lister.ListTurns(ctx, tenantID, sessionID)
		if err != nil {
			return 0, err
		}
		for _, t := range turns {
			if t >= next {
				next = t + 1
			}
		}
	}
	s.nextTurn[key] = next + 1
	return next, nil
}

// alreadyJournaled 报告该 request_id 是否已记过轮（有界去重集）。
func (s *liteRequestLogSink) alreadyJournaled(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.journaled[requestID]
	return ok
}

// markJournaled 记录已记轮的 request_id，超上限按 FIFO 淘汰。
func (s *liteRequestLogSink) markJournaled(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journaled == nil {
		s.journaled = make(map[string]struct{}, 64)
	}
	if _, ok := s.journaled[requestID]; ok {
		return
	}
	if len(s.journalFIFO) >= maxJournaledEntries {
		oldest := s.journalFIFO[0]
		s.journalFIFO = s.journalFIFO[1:]
		delete(s.journaled, oldest)
	}
	s.journaled[requestID] = struct{}{}
	s.journalFIFO = append(s.journalFIFO, requestID)
}

// liteEntryTerminal 判定条目是否终态（对齐 internal/sessionv2mirror 的
// 门槛）：success、显式 failure/rate_limited 状态或携带 ErrorKind 均视为
// 终态；in_progress 占位条目不记轮。
func liteEntryTerminal(entry *telemetry.RequestLogEntry) bool {
	if entry.Success {
		return true
	}
	if entry.RequestStatus != nil {
		switch *entry.RequestStatus {
		case telemetry.RequestStatusFailure, telemetry.RequestStatusRateLimited:
			return true
		}
	}
	return entry.ErrorKind != nil && *entry.ErrorKind != ""
}

// liteEntryStatusCode 推导 lite request_logs 行的状态码：优先上游实测
// 状态码；终态按成败取 200/500；in_progress 占位记 0（未定）。
func liteEntryStatusCode(entry *telemetry.RequestLogEntry) int {
	if entry.UpstreamStatusCode != nil {
		return *entry.UpstreamStatusCode
	}
	if !liteEntryTerminal(entry) {
		return 0
	}
	if entry.Success {
		return 200
	}
	return 500
}

// liteRawJSON 把 body 字符串规范化为合法 JSON：合法 JSON 原样保留；
// 非法（多协议转换残片/截断流）包装为 JSON 字符串，保证 SessionBody 可
// 序列化落盘且可无损读回。
func liteRawJSON(body []byte) json.RawMessage {
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	wrapped, err := json.Marshal(string(body))
	if err != nil { // 理论不可达（string 必可序列化），防御性兜底
		return json.RawMessage(`""`)
	}
	return wrapped
}

// strPtrValue 解引用 *string，nil 返回空串。
func strPtrValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// intPtrValue 解引用 *int，nil 返回 0。
func intPtrValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
