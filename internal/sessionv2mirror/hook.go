// Package sessionv2mirror mirrors _request_logs into V2 sessions tables
// (public.sessions, public.session_turns, public.session_bodies,
// public.session_turn_logs).
//
// Why a separate package: domains/session/v2 cannot import
// domains/hooks/observability/telemetry without creating an import
// cycle (telemetry → dbdegradation → session). Putting the adapter
// in a leaf package that depends on BOTH avoids the cycle.
//
// Best-effort contract: the returned hook logs and continues on any
// error; it never propagates failures up to telemetry. The primary
// _request_logs INSERT must always succeed.
package sessionv2mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// PersistHook returns a telemetry "onPersisted" hook that mirrors
// _request_logs entries into the V2 sessions tables.
//
// The hook checks sessions_v2.shadow_write and sessions_v2.enabled flags.
// The hook is a no-op when:
//   - writer is nil,
//   - the feature flag disables new V2 shadow writes and this entry has not
//     already selected V2 as its canonical write source,
//   - the entry is a non-terminal (in_progress) state.
//
// 存储优化方案 v2 §3-D4：无 gw_session_id 的流量（探针/系统/匿名）不再被
// 丢弃，而是合成 client_type='system' 的系统会话（session_id=
// 'sys:{kind}:{cred|prov}:{yyyymmdd}'，按日聚合）继续走同一条 turns 写链——
// S4 停写 _request_logs 后这类流量才有落点。合成路径跳过 title/summary
// 排除与 session_dim 维护，其余闸门（终态、shadow 开关、超时预算）与常规
// 路径一致。
//
// On real DB errors it logs WARN with the request_id and continues.
//
// The writer parameter accepts the V2Writer interface so unit tests can
// inject a failing writer; production passes a *v2.SessionWriterV2,
// which satisfies V2Writer.
//
// Optional dims writers (e.g. *SessionDimWriter) run on the same gating and
// maintain the V1 session_dim dimension table (task/project/owner/client) —
// also best-effort, never blocking the primary INSERT.
func PersistHook(writer V2Writer, dims ...DimWriter) func(entry *telemetry.RequestLogEntry) {
	if writer == nil {
		// ★§9.93：一个 nil writer 几乎总是**初始化失败**，不是「有意关闭镜像」。
		// 此前它返回一个空函数，**与「影子写开关关掉了」在可观测性上完全等价**——
		// 两者都是「一个字节都不写、一行日志都不打」。
		// §9.59–§9.92 连续四轮追一批「v1 有、会话族无孪生」的请求时，
		// 这条路径是无法从任何证据里区分出来的候选之一。
		// ⇒ 构造期就喊出来：镜像静默失效必须是**启动日志里的一件事**，
		//   而不是「查三个月数据查不出来的一件事」。
		slog.Error("sessionv2mirror: PersistHook constructed with nil writer — "+
			"V2 mirror writes are SILENTLY disabled for the whole process lifetime",
			"hint", "session_v2 init likely failed; this is NOT the same as the shadow_write flag being off")
		return func(*telemetry.RequestLogEntry) {}
	}

	return func(entry *telemetry.RequestLogEntry) {
		if entry == nil {
			return
		}

		// _request_logs emits an in_progress INSERT before upstream execution.
		// V2 turns are idempotent on request_id and cannot be updated after the
		// first insert, so mirroring that placeholder would permanently record
		// success=false/status_code=500 and drop the later success enrichment.
		// Only terminal entries are valid shadow-write inputs.
		if !entry.Success && !isTerminalFailure(entry) {
			return
		}

		// R51 教训落地（R60 S2-F4，154 复审 §四.5）：无会话头的探针产出不进
		// mirror——它们合成 sys:probe:* 系统会话后集中打 advisory lock，实测
		// 失败噪声 ~115/min（99.6% 为该类）。判据是探针专有强特征（无会话头
		// + origin/task_type 探针标记，见 IsProbeSyntheticSession 注释），不
		// 误伤真实用户会话；探针事实仍留在 _request_logs/v1 面。与 replay.go
		// 的 gate parity 共用本谓词。
		if IsProbeSyntheticSession(entry) {
			return
		}

		// 存储优化方案 v2 §3-D4：无会话头流量合成系统会话（探针统计与计费
		// 归因随 turns 走单事实管道，plan §9 风险行 2 的前置落点）。
		synthetic := false
		sessionID := ""
		if entry.GwSessionID == nil || *entry.GwSessionID == "" {
			sessionID = SyntheticSessionID(entry)
			synthetic = true
		} else {
			sessionID = *entry.GwSessionID
		}

		// Gateway-internal title/summary loopbacks are not user turns. Business
		// auto-route requests also set IsAutoRequest, but carry TaskType and must
		// be mirrored so the task dimension is queryable from session_turns.
		// Synthetic sessions keep internal loopbacks（计费事实完整性，D7 前提）；
		// kind 已在 SyntheticSessionID 中标为 internal，分析侧按 client_type
		// 过滤。
		if !synthetic && telemetry.IsInternalAutoEntry(entry) {
			return
		}

		// Check feature flags via the shadowWriteEnabled seam (defaults to
		// the settings-backed reader; tests override it because
		// settings.Global is nil in the unit-test binary).
		// A completed telemetry transaction that skipped V1 has already chosen
		// V2 as its fact source. Finish that write even if a subsequent config
		// switch closes new shadow writes; durable replay has the same contract.
		if !entry.SessionV2WriteRequired && !shadowWriteEnabled() {
			return
		}

		// Convert the telemetry entry to a V2 ProcessedRequest
		req := entryToProcessedRequest(entry, sessionID)
		if req == nil {
			// ★§9.93：此前这是一次**逐条**丢弃且**零痕迹**——
			// 没有日志、没有指标、没有 outbox 行。
			// 当前 entryToProcessedRequest 只在 entry==nil || sessionID=="" 时返回 nil，
			// 走到这里通常意味着 sessionID 被算成了空串，即合成路径算不出会话 id。
			// 判据若不加这条日志，将来有人把入口条件改宽了，
			// 这里会从「几乎不可能」变成「静默丢一大片」而无人知晓。
			slog.Warn("sessionv2mirror: entryToProcessedRequest returned nil — "+
				"this request will NOT be mirrored",
				"request_id", entry.RequestID,
				"session_id", sessionID,
				"synthetic", synthetic)
			return
		}
		if synthetic {
			// D4：系统会话在 sessions.client_type 上显式标 'system'，分析侧
			// 过滤面（plan §3-D4）。
			req.ClientType = "system"
		}

		// Bound the shadow write so a slow DB cannot stall telemetry.
		//
		// 2026-09-10 (PG log audit): the 500ms default covered begin-tx →
		// two advisory locks → MAX(turn_no) → insert turn → insert bodies →
		// outbox → commit, and the hook runs ON the telemetry worker
		// goroutine (see Client.persistRequestLog) despite the documented
		// "must be cheap and non-blocking" contract. Local PG showed the
		// gateway emitting 246 `canceling statement due to user request`
		// errors in 6h (pgx cancelling on ctx deadline) while the database
		// itself sat at 1.76% CPU with 37 idle connections — i.e. the
		// budget, not the database, was the bottleneck. Dispatch the write
		// to a bounded goroutine pool so the worker only pays for the entry
		// → ProcessedRequest parse, and raise the default budget to 2000ms.
		// The setting stays runtime-tunable via
		// settings_kv['sessions_v2.write_timeout_ms'].
		timeout := time.Duration(settings.GetPlatformInt("sessions_v2.write_timeout_ms", defaultShadowWriteTimeoutMs)) * time.Millisecond

		run := func() { runShadowWrite(writer, req, entry, dims, timeout, synthetic) }
		if !shadowWriteDispatchAsync {
			run()
			return
		}
		select {
		case shadowWriteSema <- struct{}{}:
			go func() {
				defer func() { <-shadowWriteSema }()
				run()
			}()
		default:
			// All shadow-write slots are busy (DB slow / request burst). The
			// worker contract forbids blocking here, so persist the entry to
			// the durable outbox (migration 712) for the background reaper;
			// if even that fails (DB unreachable), fall back to the bounded
			// in-process backlog — pre-GAP-2 behaviour, observability only.
			metrics.Global().RecordShadowWriteFailure("session_v2")
			if !EnqueueMirrorFailure(entry, sessionID, "semaphore_full") {
				appendBacklog(BacklogItem{
					RequestID: entry.RequestID,
					Req:       req,
					Entry:     entry,
				})
			}
		}
	}
}

const (
	// defaultShadowWriteTimeoutMs is the wall-clock budget for one shadow
	// write (dims upsert + full V2 write). 500ms was exceeded 153 times in
	// ~2.8h on the local deployment (2026-09-10); 2000ms still bounds the
	// write strictly while absorbing bursts and cold-cache plans.
	defaultShadowWriteTimeoutMs = 2000

	// shadowWriteConcurrency caps concurrent shadow writes. Each slot holds
	// one pgxpool connection for up to the write budget; 8 keeps the mirror
	// well inside db.MaxConns=32 alongside the primary _request_logs path.
	shadowWriteConcurrency = 8
)

// shadowWriteSema bounds how many shadow writes run concurrently.
var shadowWriteSema = make(chan struct{}, shadowWriteConcurrency)

// shadowWriteDispatchAsync hands the DB write to a goroutine so the
// telemetry worker goroutine is never blocked by mirror latency. Tests set
// it to false to run writes inline and assert synchronously.
var shadowWriteDispatchAsync = true

// runShadowWrite executes one best-effort shadow write with its own timeout
// budget. Called either inline (tests) or on a semaphore-bounded goroutine.
// synthetic marks a D4 system session (no gw_session_id traffic): the V1
// session_dim dimension table stays untouched for it.
//
// Race note: this may run after the telemetry worker called
// entry.releaseBodies() — that nils RequestBody/ResponseBody/OutboundBody,
// and neither the dims writer nor the V2 writer reads those fields (req is
// the deep-parsed snapshot), so the hand-off is race-free.
func runShadowWrite(w V2Writer, req *v2.ProcessedRequest, entry *telemetry.RequestLogEntry, dims []DimWriter, timeout time.Duration, synthetic bool) {
	// ★ 2026-10-20：维度写与轮次写**各用各的预算**。
	//
	// 原来两者共用一个 ctx（也就是共用一个 deadline）。实测代价（本地
	// PG 修好 shared_buffers 之后仍然复现）：
	// session_dim 维度写是 `INSERT ... ON CONFLICT (gw_session_id) DO UPDATE`，
	// 它的触发器还要去 UPDATE session_summaries；这一步慢或多等几把锁，
	// 就会把**轮次写**的预算吃光 ⇒ session_turn_details 超时 ⇒
	//   ① 正文（session_bodies）一行不落；
	//   ② 整笔失败 ⇒ 走 outbox 异步重放 ⇒ turn_no 与请求顺序错位；
	//   ③ trace 找不到父行 ⇒ trace_events 丢失。
	//
	// ⇒ 这正是 LESSONS「同一次判定里的不同判据可以有不同的时间语义，
	// 不要因为共用一个函数就让它们共用同一个 gate」的形态：
	// **维度写是 best-effort 的旁路，轮次写才是这次判定的主体。**
	// 让旁路吃主体的预算 ⇒ 主体失败、旁路却「成功」了。
	//
	// 维度写仍保持「失败只记日志、不影响主体」的原契约，只是它不再能
	// 把主体的 deadline 耗尽。
	//
	// ⚠ 关键：**光「再开一个 ctx」不够**。两个 `WithTimeout(bg, timeout)`
	// 的 deadline 是同一个绝对时刻（t0+timeout），维度写照样把主体的
	// deadline 耗光 —— 第一版修法就是这样，实测仍然失败（轮次写拿到的
	// 剩余预算是 −0.8ms）。⇒ 轮次写的 ctx 必须在**维度写之后**才建：
	// 它的预算从「轮次写开始」起算，这才真的是「各用各的预算」。
	//
	// 代价（如实标注）：一次 shadow write 的**总**墙钟上限从 timeout
	// 变成 ≤ 2×timeout（维度 timeout + 轮次 timeout）。这是有意的取舍：
	// 上限变松，换来主体不再被旁路饿死。调用方是后台 goroutine
	//（shadowWriteSema 限流），worker 侧不受影响。

	// session_dim 维度维护（任务/项目/属主/客户端）：**独立**超时预算，
	// best-effort —— V2 写失败也不影响维度更新，维度写失败也不影响 V2 写。
	// 合成系统会话不维护 V1 维度表（session_dim 以 gw_session_id 为键，
	// 无会话头流量本就缺席于此）。
	if !synthetic && len(dims) > 0 {
		dimCtx, dimCancel := context.WithTimeout(context.Background(), timeout)
		defer dimCancel()
		for _, dim := range dims {
			if dim == nil {
				continue
			}
			if err := dim.UpsertSessionDim(dimCtx, entry); err != nil {
				slog.Warn("sessionv2mirror: session_dim upsert failed",
					"request_id", entry.RequestID,
					"session_id", req.SessionID,
					"error", err)
			}
		}
	}

	// 轮次写的预算从**这里**起算（维度写之后）。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := w.Write(ctx, req)
	if err == nil && entry.T0Missing {
		// The turn now exists and is committed, in this goroutine — the only
		// point at which the landing pad can be applied without racing the
		// insert. See abandoned_turn.go for why this is not done from the
		// telemetry side, and why tenantID is req.TenantID verbatim.
		markAbandonedTurnIfT0Missing(ctx, mirrorOutbox.Load(), entry.RequestID,
			req.TenantID, req.SessionID)
	}
	// 2026-10-05 审计 §9.203: 同一 goroutine、同样在 w.Write 之后, 给 v1 已
	// 认领的那一行在 session 族补上同一枚标记。与 T0Missing 分支**并列**而非
	// 互斥 —— 一次请求可以既没有 t0 又赢得本会话的最终成功, 两个标志都必须
	// 落到行上。
	if err == nil && entry.FinalSuccessClaimed {
		markTurnFinalSuccess(ctx, mirrorOutbox.Load(), entry.RequestID,
			req.TenantID, req.SessionID)
	}
	if err != nil {
		slog.Warn("sessionv2mirror: V2 shadow write failed",
			"request_id", entry.RequestID,
			"session_id", req.SessionID,
			"synthetic", synthetic,
			"error", err)
		// P0-2 (audit §3.6 R-3.3): count this lost-row event so the
		// Grafana rule in deploy/prometheus/alerts/shadow-write-failures.yaml
		// can fire. V2 sessions tables are migration 430 (shadow write
		// during cutover); losing rows during the cutover window is the
		// exact "data drift" failure mode the audit calls out.
		metrics.Global().RecordShadowWriteFailure("session_v2")
		// Spec §12 GAP 2 (closed 2026-09-15, migration 712): persist the
		// failed entry to the durable session_mirror_outbox so the replay
		// reaper recovers it across restarts. The payload carries the full
		// entry JSON, so replay does not depend on _request_logs surviving.
		// The in-process backlog remains the last-resort surface when the
		// outbox INSERT itself fails (DB unreachable), keeping the gauge and
		// DrainBacklog observability contract intact.
		if !EnqueueMirrorFailure(entry, req.SessionID, "write_failed") {
			appendBacklog(BacklogItem{
				RequestID: entry.RequestID,
				Req:       req,
				Entry:     entry,
			})
		}
	}
}

// entryToProcessedRequest converts a telemetry.RequestLogEntry to a
// v2.ProcessedRequest. This is the bridge between the V1 telemetry
// pipeline and the V2 writer.
//
// sessionID is the resolved session key: the entry's GwSessionID for regular
// traffic, or the D4 synthetic system-session id for no-header traffic (pass
// SyntheticSessionID(entry); empty entries are rejected).
func entryToProcessedRequest(entry *telemetry.RequestLogEntry, sessionID string) *v2.ProcessedRequest {
	if entry == nil || sessionID == "" {
		return nil
	}

	eventTime := time.Now()
	if entry.EventAt != nil {
		eventTime = *entry.EventAt
	}
	req := &v2.ProcessedRequest{
		SessionID:       sessionID,
		TenantID:        entry.TenantID,
		RequestID:       entry.RequestID,
		Timestamp:       eventTime,
		ProjectID:       strVal(entry.ProjectID),
		Namespace:       strVal(entry.Namespace),
		ParentRequestID: strVal(entry.ParentRequestID),
		TaskType:        strVal(entry.TaskType),
		ClientModel:     strVal(entry.ClientModel),
		ProviderID:      providerID(entry.ProviderID),
		Success:         entry.Success,
		ErrorKind:       strVal(entry.ErrorKind),
		// Audit §9.150.4 / §9.155: copy the lifecycle label verbatim. It is
		// NOT recoverable from Success/ErrorKind — ResolveRequestStatus never
		// returns `rate_limited`, that value is set explicitly by the
		// rate-limit paths. Dropping it here is what made
		// model-catalog scan traffic indistinguishable from real failures on
		// the session side.
		RequestStatus: strVal(entry.RequestStatus),
		StatusCode:    statusCode(entry),
		StartedAt:     eventTime,
		CompletedAt:   eventTime,
	}

	// Usage & cost
	if entry.PromptTokens != nil {
		req.PromptTokens = *entry.PromptTokens
	}
	if entry.CompletionTokens != nil {
		req.CompletionTokens = *entry.CompletionTokens
	}
	if entry.CacheReadTokens != nil {
		req.CacheReadTokens = *entry.CacheReadTokens
	}
	if entry.CacheWriteTokens != nil {
		req.CacheWriteTokens = *entry.CacheWriteTokens
	}
	if entry.CostUSD != nil {
		req.CostUSD = *entry.CostUSD
	}

	// Timestamps
	if entry.EventAt != nil {
		req.StartedAt = *entry.EventAt
		req.CompletedAt = *entry.EventAt
	}
	if entry.LatencyMs != nil && *entry.LatencyMs > 0 && !req.StartedAt.IsZero() {
		req.CompletedAt = req.StartedAt.Add(time.Duration(*entry.LatencyMs) * time.Millisecond)
	}

	// V3.2 dual-write (migration 513): copy the 10 dispatch queue timestamps
	// from RequestLogEntry → ProcessedRequest so SessionWriterV2 can persist
	// them into public.session_turns alongside the existing turn columns.
	// nil-safe: pre-491 RequestLogEntries (older telemetry rows) carry nil
	// for all T0..T9, so the columns persist as NULL — no breakage.
	req.T0ArrivedAt = entry.T0ArrivedAt
	req.T1TotalEnqueuedAt = entry.T1TotalEnqueuedAt
	req.T2TotalDequeuedAt = entry.T2TotalDequeuedAt
	req.T3ModelEnqueuedAt = entry.T3ModelEnqueuedAt
	req.T4ModelDequeuedAt = entry.T4ModelDequeuedAt
	req.T5CredEnqueuedAt = entry.T5CredEnqueuedAt
	req.T6CredDequeuedAt = entry.T6CredDequeuedAt
	req.T7ForwardStartAt = entry.T7ForwardStartAt
	req.T8ResponseStartAt = entry.T8ResponseStartAt
	req.T9ResponseEndAt = entry.T9ResponseEndAt

	// Compression
	if entry.CompressionStrategy != nil {
		req.CompressionStrategy = *entry.CompressionStrategy
		req.CompressionApplied = *entry.CompressionStrategy != ""
	}
	if entry.CompressionReason != nil {
		if req.CompressionMeta == nil {
			req.CompressionMeta = make(map[string]interface{})
		}
		req.CompressionMeta["reason"] = *entry.CompressionReason
	}
	// Copy only the audited, body-free compression metadata. Raw request or
	// response payloads are intentionally excluded from the V2 mirror.
	for key, value := range safeCompressionMeta(entry.CompressionMeta, entry.TenantID, sessionID) {
		if req.CompressionMeta == nil {
			req.CompressionMeta = make(map[string]interface{})
		}
		req.CompressionMeta[key] = value
	}

	// Credential
	if entry.CredentialID != nil {
		req.CredentialID = intStr(*entry.CredentialID)
	}

	// Parse request body messages
	if entry.RequestBody != nil && *entry.RequestBody != "" {
		req.RequestBody = parseRequestBody(*entry.RequestBody)
	}

	// Parse response body messages
	if entry.ResponseBody != nil && *entry.ResponseBody != "" {
		req.ResponseBody = parseResponseBody(*entry.ResponseBody)
	}

	// ★★★★★★ FIXME-14374（2026-10-22 §143.74 轮，只登记、★ 零行为变更）：
	//	★★★★★ 本行的守卫条件 `len(entry.OutboundBody) > 0` 是【v2 正文表二次方放大】
	//	★ 的**根因锚点** ⇒ ⇒ 它的后果有【三个】，且【互不相同】：
	//	★ ① 存储：req.OutboundBody 为空 ⇒ 逐轮 outbound_body 落库为 jsonb 'null'
	//	★    ⇒ ⇒ 下一轮的 delta 差集【基准读空】⇒ ⇒ extractRequestDelta 走
	//	★    「len(LastOutboundBody) == 0 ⇒ return RequestBody」⇒ ⇒
	//	★    **每轮把客户端完整历史整份落成 request_delta** ⇒ ⇒ 二次方放大。
	//	★ ② 读侧：TurnReader.LoadChain 是【纯拼接、无去重】⇒ ⇒ 重建出的会话里
	//	★    **同一个 system prompt 会出现 N 次**（★ 实测：15 轮的会话里出现 14 次）。
	//	★ ③ 压缩器输入：session_cache.loadFromDB 先试 LoadLatestOutbound（★ 它把
	//	★    jsonb 'null' 当空 ⇒ 返回 nil）⇒ ⇒ 必然回落 LoadChain(..., 10)
	//	★    ⇒ ⇒ **压缩器拿到的上下文被放大约 10 倍**。
	//	★ 为什么这是【默认行为】而不是边缘情况：★ 未压缩流量（submit_mode == "full"）
	//	★ 在 10 月占 91.3%，在 E1 门开启后（2026-10-21 21:27:26+08）占 100%
	//	★ ⇒ ⇒ **压缩器没有产物 ⇒ 本行不填 ⇒ 基线恒空 ⇒ 形态 A 必然**。
	//	★ 实测（252 共享库，2026-10-22，只读）：
	//	★   ① E1 门开启后新写入的 4,411 行 turn_delta：
	//	★      **100% 的 outbound_body 是 jsonb 'null'**；
	//	★   ② E1 后的新会话 4,757 轮 **100% 是 submit_mode='full'**（未压缩 ⇒ 无压缩产物）；
	//	★   ③ 读侧放大（★ 用【仓内真实的 LoadChain】+ pgxmock 驱动生产导出行）：
	//	★      形态 A 会话 15 轮 → 链长 348 / 去重后 44（**7.9×**）、单条 system prompt
	//	★      **重复 14 次**；形态 B 会话 23 轮 → 链长 2,655 / 去重后 2,382（1.1×）。
	//	★ 处置：本轮【不改行为】—— 改这里会改变生产正文的【内容口径】（delta 的含义）
	//	★   ⇒ ⇒ 属主决策（⑮）拍板后再改 ⇒ ⇒ 本处只做登记，
	//	★   ⇒ ⇒ 并由门 scripts/ursmcheck/section_14374_outbound_baseline_test.go 钉住。
	// Outbound body (from session compressor)
	if len(entry.OutboundBody) > 0 {
		req.OutboundBody = parseMessagesJSON(entry.OutboundBody)
	}

	// Submit-mode header (X-Gw-Submit-Mode). This is the SubmitModeDetector's
	// P0 (authoritative) signal; without it the detector can only infer
	// "inferred_compressed" via LCS rather than emit a true "delta".
	if entry.SubmitModeHeader != nil {
		req.SubmitModeHeader = *entry.SubmitModeHeader
	}
	// LastOutboundBody deliberately remains empty here. SessionWriterV2 loads
	// the previous turn's persisted outbound snapshot when the telemetry entry
	// does not carry one; using the current request body as "previous" corrupts
	// multi-turn delta detection.

	// Attachments
	if len(entry.Attachments) > 0 {
		req.Attachments = parseAttachments(entry.Attachments)
		req.MultimodalTypes = multimodalTypes(req.Attachments)
	}

	// 存储优化方案 v2 S1a：_request_logs 独有的五类数据补采（计费/路由/
	// 诊断/检索·完整性/访问维度）+ client_type 断供修复。
	applyStorageS1AFields(req, entry)
	applyStorageS1BFields(req, entry)

	return req
}

// safeCompressionMeta returns the body-free compression metadata that is safe to
// mirror into V2. Request-log compression_meta is an extensible JSON object, so
// copying it wholesale would accidentally persist raw payloads or future PII
// fields. Nested cut_marker fields are filtered independently.
const (
	maxCompressionMetaBytes = 256 << 10
	maxMetadataRecords      = 4096
	maxMetadataString       = 256
)

func safeCompressionMeta(raw json.RawMessage, tenantID, sessionID string) map[string]interface{} {
	if len(raw) == 0 || len(raw) > maxCompressionMetaBytes || string(raw) == "null" {
		return nil
	}
	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	out := make(map[string]interface{})
	for key, value := range input {
		switch key {
		case "cut_marker":
			if filtered := safeCutMarkerMeta(value); len(filtered) > 0 {
				out[key] = filtered
				if psor, ok := filtered["pre_sanitize_offset_range"]; ok {
					out["pre_sanitize_offset_range"] = psor
				}
			}
		case "alignment_map":
			if filtered := safeAlignmentRecords(value); len(filtered) > 0 {
				out[key] = filtered
			}
		case "sanitize_message_refs":
			if filtered := safeSanitizeRefs(value); len(filtered) > 0 {
				out[key] = filtered
			}
		case "sanitize_map_ref":
			ref, ok := value.(string)
			expected := sanitizeMapRef(tenantID, sessionID)
			if ok && expected != "" && ref == expected {
				out[key] = ref
			}
		case "sanitize_map_generation":
			if text, ok := boundedHex(value, 32, 32); ok {
				out[key] = text
			}
		case "raw_snapshot", "sanitized_snapshot", "compression_source_snapshot":
			if snapshot, ok := value.(map[string]interface{}); ok {
				hash, h := boundedHex(snapshot["hash"], 64, 64)
				countValue := snapshot["message_count"]
				if countValue == nil {
					countValue = float64(0)
				}
				count, c := boundedNumber(countValue)
				tokenValue := snapshot["token_estimate"]
				if tokenValue == nil {
					tokenValue = float64(0)
				}
				tokens, t := boundedNumber(tokenValue)
				if h && c && t {
					out[key] = map[string]interface{}{"hash": hash, "message_count": count, "token_estimate": tokens}
				}
			}
		case "summary_marker", "compressed_prefix_hash":
			if text, ok := boundedHashLike(value); ok {
				out[key] = text
			}
		case "strategy", "compression_strategy", "reason", "compression_reason", "window_triggered", "lossiness":
			if text, ok := boundedLabel(value); ok && knownCompressionLabel(key, text) {
				out[key] = text
			}
		case "tokens_before", "tokens_after", "bytes_before", "bytes_after", "context_window_used", "msg_count", "token_est", "raw_token_est", "compressed_tokens", "compressed_msgs":
			if number, ok := boundedNumber(value); ok {
				out[key] = number
			}
		case "pre_sanitize_offset_range":
			if pair, ok := boundedPair(value); ok {
				out[key] = pair
			}
		case "window_source":
			// R36: the P0-1 provenance write side (buildOutboundProvenance)
			// emits this provider-window composition counter; whitelist it so
			// the telemetry survives into sessions_v2 metadata instead of
			// being silently dropped (alignment/sanitize arrays are already
			// whitelisted above; the two *_truncated flags below complete the
			// provenance block).
			if filtered := safeWindowSource(value); len(filtered) > 0 {
				out[key] = filtered
			}
		case "alignment_map_truncated", "sanitize_refs_truncated":
			if b, ok := value.(bool); ok && b {
				out[key] = b
			}
		}
	}
	// D03 P2 (2026-10-03): a whitelisted key that is present in the source,
	// non-null, and still absent from `out` was dropped by its validation
	// filter. Record it — without this marker a mirrored metadata map cannot
	// distinguish "producer never wrote the key" from "mirror dropped the key
	// as invalid". The marker key has no case in the switch above, so a
	// hostile payload can neither forge nor suppress it. The two *_truncated
	// booleans are excluded from the check on purpose: false is deliberately
	// not mirrored, so their absence carries no integrity meaning.
	var filteredKeys []string
	for _, key := range compressionMetaWhitelist {
		if v, present := input[key]; present && v != nil {
			if _, copied := out[key]; !copied {
				filteredKeys = append(filteredKeys, key)
			}
		}
	}
	if len(filteredKeys) > 0 {
		out["mirror_filtered_keys"] = filteredKeys
	}
	return out
}

// compressionMetaWhitelist mirrors the switch cases in safeCompressionMeta;
// keep it in lockstep when adding a case. "alignment_map_truncated" and
// "sanitize_refs_truncated" are intentionally absent (see the post-pass
// comment in safeCompressionMeta).
var compressionMetaWhitelist = []string{
	"cut_marker", "alignment_map", "sanitize_message_refs",
	"sanitize_map_ref", "sanitize_map_generation",
	"raw_snapshot", "sanitized_snapshot", "compression_source_snapshot",
	"summary_marker", "compressed_prefix_hash",
	"strategy", "compression_strategy", "reason", "compression_reason",
	"window_triggered", "lossiness",
	"tokens_before", "tokens_after", "bytes_before", "bytes_after",
	"context_window_used", "msg_count", "token_est", "raw_token_est",
	"compressed_tokens", "compressed_msgs",
	"pre_sanitize_offset_range", "window_source",
}

// safeWindowSource filters the producer-side window_source composition
// counter (label → count). Labels are bounded words; counts bounded
// integers; the whole map capped to keep metadata size predictable.
func safeWindowSource(value interface{}) map[string]interface{} {
	input, ok := value.(map[string]interface{})
	if !ok || len(input) == 0 || len(input) > 16 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for k, v := range input {
		label, ok := boundedCompressionWord(k)
		if !ok {
			continue
		}
		if n, ok := boundedNumber(v); ok {
			out[label] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func safeAlignmentRecords(value interface{}) []map[string]interface{} {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > maxMetadataRecords {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		original, ok1 := nonNegativeIndex(record["original_index"])
		compressed, ok2 := signedIndex(record["compressed_index"])
		into, ok3 := signedIndex(record["compressed_into"])
		isCompressed, ok4 := record["is_compressed"].(bool)
		hash, ok5 := boundedHex(record["hash"], 32, 64)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			return nil
		}
		entry := map[string]interface{}{
			"original_index": original, "compressed_index": compressed,
			"is_compressed": isCompressed, "compressed_into": into, "hash": hash,
		}
		// R35 (2026-09-17 audit P2): keep the identity-disambiguation and
		// target-kind fields too — occurrence disambiguates duplicate hashes
		// and target_kind/target_space carry the retained/summary/dropped
		// semantics the provenance validators reason over. Optional fields:
		// absent on legacy producers.
		if occ, ok := nonNegativeIndex(record["occurrence"]); ok {
			entry["occurrence"] = occ
		}
		if kind, ok := boundedCompressionWord(record["target_kind"]); ok {
			entry["target_kind"] = kind
		}
		if space, ok := boundedCompressionWord(record["target_space"]); ok {
			entry["target_space"] = space
		}
		out = append(out, entry)
	}
	return out
}

// boundedCompressionWord accepts short lowercase identifier words
// ("retained", "summary", "dropped", "none", ...) used by the alignment
// provenance records — letters/digits/underscore/-, max 24 chars.
func boundedCompressionWord(value interface{}) (string, bool) {
	s, ok := value.(string)
	if !ok || s == "" || len(s) > 24 {
		return "", false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", false
		}
	}
	return s, true
}

func safeSanitizeRefs(value interface{}) []map[string]interface{} {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > maxMetadataRecords {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		rawIndex, ok1 := nonNegativeIndex(record["raw_index"])
		sanitizedIndex, ok2 := nonNegativeIndex(record["sanitized_index"])
		rawHash, ok3 := boundedHex(record["raw_hash"], 32, 64)
		sanitizedHash, ok4 := boundedHex(record["sanitized_hash"], 32, 64)
		changed, ok5 := record["changed"].(bool)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			return nil
		}
		filtered := map[string]interface{}{
			"raw_index": rawIndex, "sanitized_index": sanitizedIndex,
			"raw_hash": rawHash, "sanitized_hash": sanitizedHash, "changed": changed,
		}
		if count, ok := boundedNumber(record["placeholder_count"]); ok {
			filtered["placeholder_count"] = count
		}
		out = append(out, filtered)
	}
	return out
}

func safeCutMarkerMeta(value interface{}) map[string]interface{} {
	input, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	version, ok1 := boundedNumber(input["version"])
	createdAt, ok2 := boundedNumber(input["created_at"])
	source, ok3 := boundedNumber(input["source_msg_count"])
	system, ok4 := boundedNumber(input["system_msg_count"])
	cut, ok5 := boundedNumber(input["cut_index"])
	strategy, ok6 := boundedLabel(input["strategy"])
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !knownCompressionLabel("strategy", strategy) || cut <= 0 || system+cut > source {
		return nil
	}
	out := map[string]interface{}{
		"version": version, "created_at": createdAt, "source_msg_count": source,
		"system_msg_count": system, "cut_index": cut, "strategy": strategy,
	}
	if number, ok := boundedNumber(input["bytes_before"]); ok {
		out["bytes_before"] = number
	}
	if number, ok := boundedNumber(input["bytes_after"]); ok {
		out["bytes_after"] = number
	}
	if marker, ok := boundedHashLike(input["summary_marker"]); ok {
		out["summary_marker"] = marker
	}
	if pair, ok := boundedPair(input["pre_sanitize_offset_range"]); ok && pair[1] <= source {
		out["pre_sanitize_offset_range"] = pair
	}
	return out
}

func boundedNumber(value interface{}) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && n >= 0 && n <= 1<<53 && n == float64(int64(n))
}

func nonNegativeIndex(value interface{}) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && n >= 0 && n <= maxMetadataRecords && n == float64(int64(n))
}

func signedIndex(value interface{}) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && n >= -1 && n <= maxMetadataRecords && n == float64(int64(n))
}

func boundedPair(value interface{}) ([]float64, bool) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return nil, false
	}
	start, ok1 := boundedNumber(items[0])
	end, ok2 := boundedNumber(items[1])
	return []float64{start, end}, ok1 && ok2 && start <= end
}

func boundedLabel(value interface{}) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) == 0 || len(text) > maxMetadataString {
		return "", false
	}
	for _, r := range text {
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r == '/' || r == ' ' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", false
		}
	}
	return text, true
}

func knownCompressionLabel(key, text string) bool {
	known := map[string]bool{
		"none": true, "noop": true, "delta_append": true, "mechanical_trim": true,
		"smart_window": true, "smart_window_llm": true, "smart_window_mechanical": true,
		"sliding_window_token": true, "sliding_window_token_absolute": true,
		"sliding_window_msg_count": true, "sliding_window_idle": true,
		"incremental_cache": true, "incremental_cache_tail": true, "incremental_v2_metadata": true,
		"strategy_runner": true, "memora_l1": true, "memora_l1_inject": true,
		"llm_summary": true, "auto_threshold": true, "provider_window": true,
		"aggressive": true, "mode_1_auto_threshold": true, "mode_2_on_4xx": true,
		"mode_warmup_skipped": true, "token_threshold_forced_absolute": true,
		"token_threshold_preliminary": true, "tail": true, "whole": true,
		"below": true, "preliminary": true, "forced": true,
		"size": true, "count": true, "idle": true, "token": true,
	}
	if known[text] {
		return true
	}
	if key == "window_triggered" && strings.HasPrefix(text, "sliding_window_") {
		suffix := strings.TrimPrefix(text, "sliding_window_")
		return suffix == "token" || suffix == "token_absolute" || suffix == "msg_count" || suffix == "idle"
	}
	return false
}

func boundedHashLike(value interface{}) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) == 0 || len(text) > maxMetadataString {
		return "", false
	}
	if strings.HasPrefix(text, "[smm_v1:") && strings.HasSuffix(text, "]") {
		inner := strings.TrimSuffix(strings.TrimPrefix(text, "[smm_v1:"), "]")
		if validated, ok := boundedHex(inner, 16, 16); ok {
			return "[smm_v1:" + validated + "]", true
		}
		return "", false
	}
	return boundedHex(value, 16, 128)
}

func boundedHex(value interface{}, minLen, maxLen int) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) < minLen || len(text) > maxLen {
		return "", false
	}
	for _, r := range text {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return "", false
		}
	}
	return text, true
}

func sanitizeMapRef(tenantID, sessionID string) string {
	if tenantID == "" || sessionID == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(tenantID))
	return fmt.Sprintf("session:%s:%s:sanitize", hex.EncodeToString(hash[:8]), sessionID)
}

// ── JSON body parsing ──────────────────────────────────────────────────────

type requestProbe struct {
	Messages []msgProbe `json:"messages"`
}

type responseProbe struct {
	Choices []choiceProbe `json:"choices"`
}

type choiceProbe struct {
	Message msgProbe `json:"message"`
}

type msgProbe struct {
	Role       string                   `json:"role"`
	Content    json.RawMessage          `json:"content"`
	ToolCallID string                   `json:"tool_call_id"`
	Name       string                   `json:"name"`
	ToolCalls  []map[string]interface{} `json:"tool_calls"`
}

func parseRequestBody(body string) []v2.Message {
	return parseProtocolMessages(json.RawMessage(body), false)
}

func parseResponseBody(body string) []v2.Message {
	return parseProtocolMessages(json.RawMessage(body), true)
}

// parseProtocolMessages normalizes the common request/response envelopes before
// handing message content to the shared IR decoder. This keeps legacy string
// messages and native multimodal protocol bodies on one parsing path.
func parseProtocolMessages(raw json.RawMessage, response bool) []v2.Message {
	if len(raw) == 0 {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	var items []json.RawMessage
	if !response {
		if b := root["messages"]; len(b) > 0 {
			_ = json.Unmarshal(b, &items)
		} else if b := root["contents"]; len(b) > 0 {
			_ = json.Unmarshal(b, &items)
		} else if b := root["input"]; len(b) > 0 {
			items = responsesWireItems(root, b)
		}
	} else if b := root["choices"]; len(b) > 0 {
		var choices []struct {
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(b, &choices) == nil {
			for _, c := range choices {
				if len(c.Message) > 0 {
					items = append(items, c.Message)
				}
			}
		}
	} else if b := root["content"]; len(b) > 0 {
		// Anthropic response content is a block array; wrap it as one message.
		items = []json.RawMessage{json.RawMessage(`{"role":"assistant","content":` + string(b) + `}`)}
	} else if b := root["candidates"]; len(b) > 0 {
		var candidates []struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(b, &candidates) == nil {
			for _, c := range candidates {
				if len(c.Content) > 0 {
					var content map[string]json.RawMessage
					if json.Unmarshal(c.Content, &content) == nil {
						content["role"] = json.RawMessage(`"model"`)
						items = append(items, marshalObject(content))
					}
				}
			}
		}
	} else if b := root["output"]; len(b) > 0 {
		// Responses API output items already carry type/role/content fields.
		_ = json.Unmarshal(b, &items)
	}
	if len(items) == 0 {
		return nil
	}
	canonical, err := json.Marshal(map[string]any{"messages": items})
	if err != nil {
		return nil
	}
	return v2.IRMessagesToV2(v2.IRMessagesFromJSON(canonical))
}

func marshalObject(obj map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(obj)
	return b
}

// responsesWireItems extracts the message list from an OpenAI-Responses request
// envelope, producing the same []json.RawMessage shape the chat (`messages`)
// and Gemini (`contents`) branches hand to the shared IR decoder.
//
// 2026-10-16 (defect: request bodies vanished on the Responses leg). `input` is
// the Responses-wire counterpart of `messages`; `instructions` carries what the
// chat envelope used to put in a leading system message. Neither key was in the
// whitelist above, so any request served over the Responses wire parsed to nil
// and its turn persisted request_delta = JSON null.
//
// Two production paths reach the mirror in that shape, and both were losing the
// whole inbound body:
//   - a genuine `/v1/responses` client (the entry's RequestBody IS the Responses
//     envelope), and
//   - a `/v1/chat/completions` client whose model is Responses-capable, because
//     the proactive chat→Responses upgrade replaces the executor's InboundBody
//     with the converted body (domains/streaming/executors/executor_chat.go:
//     `sourceBody = proactiveResponsesBody`, then `InboundBody: sourceBody`).
//     Verified against the local PG: exactly the two such requests
//     (grok-4.6, gpt-5.6-luna) carry request_delta = JSON null while their
//     chat-leg peers (deepseek-v4-flash, claude-sonnet-5-5) carry arrays.
//
// The response side never showed the gap because the chat→Responses bridge
// converts the reply back into a chat `choices` envelope before it is persisted,
// which is why the two broken rows read req=null / resp=array.
//
// `input` has two documented shapes: a bare string (one user turn) and an item
// array ({"type":"message","role":..,"content":[{"type":"input_text",..}]} plus
// function_call / function_call_output items). Only the string form needs
// synthesizing; array items already carry role/content and are handed to the IR
// decoder untouched — it preserves input_text/output_text block payloads verbatim.
func responsesWireItems(root map[string]json.RawMessage, input json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		items = nil
	}
	if len(items) == 0 {
		var text string
		if err := json.Unmarshal(input, &text); err == nil && text != "" {
			if msg, err := json.Marshal(map[string]string{"role": "user", "content": text}); err == nil {
				items = []json.RawMessage{msg}
			}
		}
	}
	var instructions string
	if err := json.Unmarshal(root["instructions"], &instructions); err != nil || instructions == "" {
		return items
	}
	// `instructions` is emitted FIRST so the reconstructed conversation keeps
	// the original ordering: the chat→Responses converter lifts the leading
	// system message out of `messages` into `instructions`, so without this the
	// system prompt would silently vanish from the delta even after the body
	// starts parsing.
	sys, err := json.Marshal(map[string]string{"role": "system", "content": instructions})
	if err != nil {
		return items
	}
	return append([]json.RawMessage{sys}, items...)
}

// parseMessagesJSON extracts messages from the session compressor's
// OutboundBody. That value is the FULL upstream request object
// ({"model":...,"messages":[...]} — see compression.spliceBodyMessages), so we
// try the object shape first and fall back to a bare [] array for callers /
// tests that pass just the messages slice.
//
// 2026-08-05 (v2 mirror bug): the previous implementation only handled the bare
// array. In production OutboundBody is always the object form, so the unmarshal
// failed silently and req.OutboundBody was always empty — every
// session_bodies.outbound_body persisted NULL, which in turn broke multi-turn
// submit-mode/delta detection (getPreviousBody found no prior outbound).
func parseMessagesJSON(raw json.RawMessage) []v2.Message {
	// Object form: {"messages":[...]} (also tolerates model/tools/etc.).
	var obj requestProbe
	if err := json.Unmarshal(raw, &obj); err == nil && len(obj.Messages) > 0 {
		return toMessages(obj.Messages)
	}
	// Bare array form: [{"role":...,"content":...}, ...].
	var arr []msgProbe
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return toMessages(arr)
	}
	// 2026-10-16: Responses-wire outbound object ({"input":[...],
	// "instructions":..., no "messages"}). Same envelope gap as the inbound
	// request body, so it is delegated to the shared IR decoder rather than
	// growing a second, divergent msgProbe-shaped decoder here: outbound and
	// inbound messages must be decoded by ONE path or they drift again.
	if hasResponsesWireInput(raw) {
		return parseProtocolMessages(raw, false)
	}
	return nil
}

// hasResponsesWireInput reports whether raw is a JSON object carrying the
// Responses-wire `input` envelope (and therefore has no `messages` to find).
func hasResponsesWireInput(raw json.RawMessage) bool {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return false
	}
	return len(root["input"]) > 0 && len(root["messages"]) == 0
}

func toMessages(raw []msgProbe) []v2.Message {
	msgs := make([]v2.Message, 0, len(raw))
	for _, r := range raw {
		msgs = append(msgs, toMessage(r))
	}
	return msgs
}

func toMessage(r msgProbe) v2.Message {
	m := v2.Message{
		Role:       r.Role,
		ToolCallID: r.ToolCallID,
		Name:       r.Name,
	}
	if len(r.Content) > 0 && string(r.Content) != "null" {
		if err := json.Unmarshal(r.Content, &m.Content); err != nil {
			m.ContentRaw = append(json.RawMessage(nil), r.Content...)
		}
	} else if len(r.Content) > 0 {
		m.ContentRaw = append(json.RawMessage(nil), r.Content...)
	}
	if len(r.ToolCalls) > 0 {
		m.ToolCalls = r.ToolCalls
	}
	return m
}

// ── Attachment parsing ─────────────────────────────────────────────────────

type attachProbe struct {
	Name           string    `json:"name"`
	ObjectKey      string    `json:"object_key"`
	LegacyPath     string    `json:"path"`
	MIMEType       string    `json:"mime_type"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	LegacySize     int64     `json:"size"`
	SHA256         string    `json:"sha256"`
	LegacyHash     string    `json:"hash"`
	OriginalURL    string    `json:"original_url"`
	SourceProtocol string    `json:"source_protocol"`
	DeclaredMIME   string    `json:"declared_mime"`
	SniffedMIME    string    `json:"sniffed_mime"`
	ProviderFileID string    `json:"provider_file_id"`
	ExpiresAt      time.Time `json:"expires_at"`
	Replayable     bool      `json:"replayable"`
}

func parseAttachments(raw json.RawMessage) []v2.AttachmentRef {
	var probes []attachProbe
	if err := json.Unmarshal(raw, &probes); err != nil {
		return nil
	}
	refs := make([]v2.AttachmentRef, 0, len(probes))
	for _, p := range probes {
		refs = append(refs, v2.AttachmentRef{
			Name:      p.Name,
			ObjectKey: firstNonEmpty(p.ObjectKey, p.LegacyPath),
			MIMEType:  firstNonEmpty(p.MIMEType, p.ContentType),
			SizeBytes: firstNonZero(p.SizeBytes, p.LegacySize),
			SHA256:    firstNonEmpty(p.SHA256, p.LegacyHash),

			SourceProtocol: p.SourceProtocol,
			DeclaredMIME:   p.DeclaredMIME,
			SniffedMIME:    p.SniffedMIME,
			ProviderFileID: p.ProviderFileID,
			ExpiresAt:      p.ExpiresAt,
			Replayable:     p.Replayable,
		})
	}
	return refs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func multimodalTypes(attachments []v2.AttachmentRef) []string {
	seen := make(map[string]struct{})
	var types []string
	for _, a := range attachments {
		t := mimeClass(a.MIMEType)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			types = append(types, t)
		}
	}
	return types
}

func mimeClass(mime string) string {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return "image"
	case "audio/mpeg", "audio/wav", "audio/ogg":
		return "audio"
	case "video/mp4", "video/webm":
		return "video"
	case "application/pdf", "text/plain", "text/csv":
		return "document"
	default:
		if mime == "" {
			return ""
		}
		return "document"
	}
}

// ── Small helpers ──────────────────────────────────────────────────────────

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func providerID(v *int) string {
	if v == nil || *v == 0 {
		return ""
	}
	return intStr(*v)
}

func intStr(v int) string {
	if v == 0 {
		return ""
	}
	buf := make([]byte, 0, 10)
	for v > 0 {
		buf = append(buf, byte('0'+v%10))
		v /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// EligibleForTurnMirrorPolicy reports whether the request belongs to the
// policy population that should produce a session_turn. It intentionally does
// NOT apply the runtime shadow-write switch: turning session writes off while
// business traffic continues is exactly what data-horizon must detect.
//
// PersistHook keeps the individual early returns below for observability and
// exclusion-ledger tests; this exported classifier is the shared contract for
// independent consumers such as the per-instance data-horizon canary.
func EligibleForTurnMirrorPolicy(entry *telemetry.RequestLogEntry) bool {
	if entry == nil {
		return false
	}
	if !entry.Success && !isTerminalFailure(entry) {
		return false
	}
	if IsProbeSyntheticSession(entry) {
		return false
	}
	synthetic := entry.GwSessionID == nil || strings.TrimSpace(*entry.GwSessionID) == ""
	if !synthetic && telemetry.IsInternalAutoEntry(entry) {
		return false
	}
	return true
}

func isTerminalFailure(entry *telemetry.RequestLogEntry) bool {
	if entry == nil || entry.Success {
		return false
	}
	if entry.RequestStatus != nil {
		switch strings.TrimSpace(*entry.RequestStatus) {
		case telemetry.RequestStatusFailure, telemetry.RequestStatusRateLimited:
			return true
		}
	}
	return entry.ErrorKind != nil && strings.TrimSpace(*entry.ErrorKind) != ""
}

// statusCode derives the client-facing HTTP status persisted as
// session_turns.status_code.
//
// Precedence (2026-10-16): ClientStatusCode is what the client actually
// received, so it wins over everything derived. UpstreamStatusCode is the
// provider's status, which is NOT the client's when the gateway authored the
// response — it is kept as the second choice for proxy-shaped failures where
// the upstream status *was* relayed verbatim. The 200/500 pair is the last
// resort and means "no received-status information was recorded", not a claim
// that the client saw 500: gateway-authored rejections (auth / budget /
// session / policy) never reach an upstream, so UpstreamStatusCode is nil and
// the old three-step chain turned every 401 into a persisted 500.
func statusCode(entry *telemetry.RequestLogEntry) int {
	if entry.ClientStatusCode != nil {
		return *entry.ClientStatusCode
	}
	if entry.UpstreamStatusCode != nil {
		return *entry.UpstreamStatusCode
	}
	if entry.Success {
		return 200
	}
	return 500
}
