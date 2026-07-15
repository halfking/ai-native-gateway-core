package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// ContextAttrsOp 区分"写入侧表"的初值与最终态。
// 与 RequestLogOp 同型，初值 + 最终态分离便于 upsert 补全。
type ContextAttrsOp string

const (
	ContextAttrsUpsert ContextAttrsOp = "upsert"
)

// ContextAttrsEntry 是 request_context_attrs 侧表的一行。
//
// 字段命名严格对齐迁移 407 的列名。零值（nil/""）即不写入；pointer 字段
// 保证 NULL 写入，与 request_logs 主表语义一致。
//
// 写入语义：INSERT INTO request_context_attrs ON CONFLICT (request_id)
// DO UPDATE SET ... — 幂等，支持 upsert 补全（同一 request_id 可多次 emit）。
//
// 与 RequestLogEntry 完全独立：本表写入失败不影响 request_logs 写入。
type ContextAttrsEntry struct {
	Op               ContextAttrsOp `json:"op,omitempty"`
	RequestID        string         `json:"request_id"`
	TenantID         string         `json:"tenant_id"`
	GwSessionID      *string        `json:"gw_session_id,omitempty"`
	GwTaskID         *string        `json:"gw_task_id,omitempty"`

	// ─── 客户端信息 ───
	IdentityHash      *string `json:"identity_hash,omitempty"`
	VirtualClientID  *string `json:"virtual_client_id,omitempty"`
	VirtualIP        *string `json:"virtual_ip,omitempty"`
	VirtualMAC       *string `json:"virtual_mac,omitempty"`
	AgentName        *string `json:"agent_name,omitempty"`
	AgentType        *string `json:"agent_type,omitempty"`
	ClientIP         *string `json:"client_ip,omitempty"`
	ClientForwardedFor *string `json:"client_forwarded_for,omitempty"`
	APIKeyFingerprint *string `json:"api_key_fingerprint,omitempty"`
	APIKeyID         *int    `json:"api_key_id,omitempty"`
	ApplicationID    *int    `json:"application_id,omitempty"`
	ApplicationCode  *string `json:"application_code,omitempty"`
	OwnerUser        *string `json:"owner_user,omitempty"`
	EndUserID        *string `json:"end_user_id,omitempty"`
	CustomerID       *int64  `json:"customer_id,omitempty"`
	ClientProtocol   *string `json:"client_protocol,omitempty"`

	// ─── 请求信息 ───
	IsRetry        bool    `json:"is_retry"`
	AttemptNo      *int    `json:"attempt_no,omitempty"`
	IsProbe        bool    `json:"is_probe"`
	OriginStage    *string `json:"origin_stage,omitempty"`
	TurnNo         *int    `json:"turn_no,omitempty"`
	SourceChannel  *string `json:"source_channel,omitempty"`
	ClientRequestID *string `json:"client_request_id,omitempty"`

	// ─── 会话扩展信息 ───
	ProjectID      *string `json:"project_id,omitempty"`
	SessionTitle   *string `json:"session_title,omitempty"`
	SessionSummary *string `json:"session_summary,omitempty"`
	TaskID         *string `json:"task_id,omitempty"`

	// ─── 取证原材 ───
	FingerprintRaw json.RawMessage `json:"fingerprint_raw,omitempty"`
}

// EmitContextAttrs 把侧表行入队（与 EmitRequestLog 同位 — 通过 channel+worker
// 异步持久化，写入失败仅日志，不阻塞主请求日志）。
//
// 调用方在 EmitRequestLogInsert/Update 旁同步调用即可，失败 best-effort。
func (c *Client) EmitContextAttrs(entry *ContextAttrsEntry) {
	if c == nil || entry == nil {
		return
	}
	if entry.RequestID == "" {
		slog.Warn("telemetry context_attrs: empty request_id, skipped")
		return
	}
	if entry.Op == "" {
		entry.Op = ContextAttrsUpsert
	}
	if c.dbPool == nil {
		slog.Debug("telemetry context_attrs: db pool nil; side-table write skipped",
			"request_id", entry.RequestID)
		return
	}
	if !c.Enabled() {
		return
	}
	select {
	case c.queue <- entry:
	default:
		slog.Warn("telemetry context_attrs: queue full, dropping entry",
			"request_id", entry.RequestID)
	}
}

// AttrsCtxKey is the string-typed context key namespace used by middleware
// or upstream producers to feed late-arriving values into ApplyAttrsFromContext.
//
// Convention matches originCtxKey (client.go) — string keys allow cross-package
// ctx propagation without import cycles. Reserved keys:
//
//	"attrs.identity_hash"        string
//	"attrs.virtual_client_id"    string
//	"attrs.virtual_ip"           string
//	"attrs.virtual_mac"          string
//	"attrs.agent_name"           string
//	"attrs.agent_type"           string
//	"attrs.client_ip"            string
//	"attrs.client_forwarded_for" string
//	"attrs.api_key_fingerprint"  string
//	"attrs.customer_id"          int64
//	"attrs.client_protocol"      string
//	"attrs.source_channel"       string
//	"attrs.project_id"           string
//	"attrs.origin_stage"         string
//	"attrs.client_request_id"    string
type AttrsCtxKey string

// ApplyAttrsFromContext 读取 ctx 中的 attrs.* 值并填充到 entry（nil-safe + first-write-wins）。
//
// 与 ApplyOriginFromContext 模式一致：在 ctx 中已有但 entry 为 nil 的字段被
// 填充；entry 已有的字段（first-write-wins）不被 ctx 覆盖。用于兜底
// middleware/agent 在 fillAttemptMeta 之后才产生的字段。
func (e *ContextAttrsEntry) ApplyAttrsFromContext(ctx context.Context) {
	if e == nil || ctx == nil {
		return
	}
	maybeSetString := func(key string, dst **string) {
		if *dst != nil {
			return
		}
		if v, ok := ctx.Value(AttrsCtxKey(key)).(string); ok && v != "" {
			*dst = &v
		}
	}
	maybeSetInt64 := func(key string, dst **int64) {
		if *dst != nil {
			return
		}
		if v, ok := ctx.Value(AttrsCtxKey(key)).(int64); ok {
			*dst = &v
		}
	}

	maybeSetString("attrs.identity_hash", &e.IdentityHash)
	maybeSetString("attrs.virtual_client_id", &e.VirtualClientID)
	maybeSetString("attrs.virtual_ip", &e.VirtualIP)
	maybeSetString("attrs.virtual_mac", &e.VirtualMAC)
	maybeSetString("attrs.agent_name", &e.AgentName)
	maybeSetString("attrs.agent_type", &e.AgentType)
	maybeSetString("attrs.client_ip", &e.ClientIP)
	maybeSetString("attrs.client_forwarded_for", &e.ClientForwardedFor)
	maybeSetString("attrs.api_key_fingerprint", &e.APIKeyFingerprint)
	maybeSetInt64("attrs.customer_id", &e.CustomerID)
	maybeSetString("attrs.client_protocol", &e.ClientProtocol)
	maybeSetString("attrs.source_channel", &e.SourceChannel)
	maybeSetString("attrs.project_id", &e.ProjectID)
	maybeSetString("attrs.origin_stage", &e.OriginStage)
	maybeSetString("attrs.client_request_id", &e.ClientRequestID)
}

// flush 分发增加 case。
// 注意：flush 函数位于 client.go 内；我们在这里只提供 persistContextAttrs
// 实现，flush 的 switch case 由 client.go 注册（见 patch 注释）。

// persistContextAttrs 把侧表行写入 request_context_attrs，ON CONFLICT 幂等 upsert。
func (c *Client) persistContextAttrs(entry *ContextAttrsEntry) error {
	if entry == nil || entry.RequestID == "" {
		return fmt.Errorf("empty request_id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fpRaw := entry.FingerprintRaw
	if len(fpRaw) == 0 {
		fpRaw = json.RawMessage("null")
	}

	_, err := c.dbPool.Exec(ctx, `
		INSERT INTO request_context_attrs (
			request_id, ts, tenant_id, gw_session_id, gw_task_id,
			identity_hash, virtual_client_id, virtual_ip, virtual_mac,
			agent_name, agent_type, client_ip, client_forwarded_for,
			api_key_fingerprint, api_key_id, application_id, application_code,
			owner_user, end_user_id, customer_id, client_protocol,
			is_retry, attempt_no, is_probe, origin_stage, turn_no, source_channel,
			client_request_id, project_id, session_title, session_summary, task_id,
			fingerprint_raw
		) VALUES (
			$1, now(), $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19, $20,
			$21, $22, $23, $24, $25, $26,
			$27, $28, $29, $30, $31,
			$32
		)
		ON CONFLICT (request_id) DO UPDATE SET
			ts = now(),
			tenant_id = EXCLUDED.tenant_id,
			gw_session_id = EXCLUDED.gw_session_id,
			gw_task_id = EXCLUDED.gw_task_id,
			identity_hash = EXCLUDED.identity_hash,
			virtual_client_id = EXCLUDED.virtual_client_id,
			virtual_ip = EXCLUDED.virtual_ip,
			virtual_mac = EXCLUDED.virtual_mac,
			agent_name = EXCLUDED.agent_name,
			agent_type = EXCLUDED.agent_type,
			client_ip = EXCLUDED.client_ip,
			client_forwarded_for = EXCLUDED.client_forwarded_for,
			api_key_fingerprint = EXCLUDED.api_key_fingerprint,
			api_key_id = EXCLUDED.api_key_id,
			application_id = EXCLUDED.application_id,
			application_code = EXCLUDED.application_code,
			owner_user = EXCLUDED.owner_user,
			end_user_id = EXCLUDED.end_user_id,
			customer_id = EXCLUDED.customer_id,
			client_protocol = EXCLUDED.client_protocol,
			is_retry = EXCLUDED.is_retry,
			attempt_no = EXCLUDED.attempt_no,
			is_probe = EXCLUDED.is_probe,
			origin_stage = EXCLUDED.origin_stage,
			turn_no = EXCLUDED.turn_no,
			source_channel = EXCLUDED.source_channel,
			client_request_id = EXCLUDED.client_request_id,
			project_id = EXCLUDED.project_id,
			session_title = EXCLUDED.session_title,
			session_summary = EXCLUDED.session_summary,
			task_id = EXCLUDED.task_id,
			fingerprint_raw = EXCLUDED.fingerprint_raw
	`,
		entry.RequestID,
		nonEmpty(entry.TenantID, "default"),
		entry.GwSessionID,
		entry.GwTaskID,
		entry.IdentityHash,
		entry.VirtualClientID,
		entry.VirtualIP,
		entry.VirtualMAC,
		entry.AgentName,
		entry.AgentType,
		entry.ClientIP,
		entry.ClientForwardedFor,
		entry.APIKeyFingerprint,
		entry.APIKeyID,
		entry.ApplicationID,
		entry.ApplicationCode,
		entry.OwnerUser,
		entry.EndUserID,
		entry.CustomerID,
		entry.ClientProtocol,
		entry.IsRetry,
		entry.AttemptNo,
		entry.IsProbe,
		entry.OriginStage,
		entry.TurnNo,
		entry.SourceChannel,
		entry.ClientRequestID,
		entry.ProjectID,
		entry.SessionTitle,
		entry.SessionSummary,
		entry.TaskID,
		fpRaw,
	)
	return err
}