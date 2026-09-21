// Package sessionv2 — details_writer.go
//
// 会话存储解耦 v3（docs/storage/2026-09-20-session-storage-decoupling-plan.md
// §3.3，migration 731/732）：session_turn_details 特征层写入器。
//
// 职责：把 ProcessedRequest.Details（mirror bridge s1b_fields 从
// telemetry.RequestLogEntry 补采的 30+ 特征列）与 turn 行同事务落
// session_turn_details_hot。元数据（session_turns）/原文（session_bodies）
// 不动——本层只承载 710 视图 NULL 占位列与 S3 分析/安全/多维 token 储备列。
//
// 语义：
//
//	· 同事务：与 turn+bodies 共用 AppendTurn 的事务，任一失败整体回滚，
//	  不留孤儿（spec §6.2 同构）；
//	· 幂等 upsert：ON CONFLICT (tenant_id, request_id, partition_date)
//	  DO UPDATE——telemetry 晚到回填/重放取最新值（与 request_logs 的
//	  in_progress→终态 UPDATE 语义对齐）；
//	· 可选：表族缺席（731 未跑的陈旧库/极简测试桩）时整体跳过，
//	  零错误零降级（SetDetailsWriter(nil) 同效）。
package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DetailsRecord mirrors one session_turn_details(_hot) row. Zero values
// persist as SQL NULL (nilIf helpers at the SQL boundary), matching the
// turn_writer fill-rate semantics — acceptance SQL measures real collection.
type DetailsRecord struct {
	SessionID string
	TurnNo    int
	TenantID  string
	RequestID string
	Ts        time.Time

	// 视图契约组（732 LEFT JOIN 替换 NULL 占位的 30 列）
	ClientModel       *string
	ProviderID        *int64
	ClientProfile     *string
	VirtualIP         *string
	VirtualMAC        *string
	AffinityHit       *bool
	TransformRuleID   *string
	GwTaskID          *string
	APIKeyPrefix      *string
	OwnerUser         *string
	ApplicationCode   *string
	KeyAlias          *string
	APIKeyOwnerUser   *string
	AutoProfile       *string
	ConfidenceNum     *float64
	ModelChosen       *string
	StrategyUsed      *string
	CompressionReason *string
	OutboundMsgCount  *int
	OutboundTokenEst  *int
	OutboundMsgHashes []byte
	QualityFlags      []string
	QualityFixActions []byte
	QualityScore      *float64
	StreamChunkErrors *int
	StreamChunksSent  *int
	Attachments       []byte
	RequestType       *string
	RequestClass      *string
	DueAt             *time.Time
}

// insertDetailsSQL writes the hot layer only (mirror pattern: runtime writes
// go to session_turns_hot / session_turn_details_hot; promote moves cold rows
// to monthly partitions). ON CONFLICT DO UPDATE keeps the newest values for
// late enrichment replays.
const insertDetailsSQL = `
	INSERT INTO public.session_turn_details_hot (
		session_id, turn_no, tenant_id, request_id, ts, partition_date,
		client_model, provider_id, client_profile, virtual_ip, virtual_mac,
		affinity_hit, transform_rule_id, gw_task_id, api_key_prefix, owner_user,
		application_code, key_alias, api_key_owner_user, auto_profile, confidence_num,
		model_chosen, strategy_used, compression_reason, outbound_msg_count,
		outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions,
		quality_score, stream_chunk_errors, stream_chunks_sent, attachments,
		request_type, request_class, due_at
	) VALUES (
		$1, $2, $3, $4, $5, $6,
		$7, $8, $9, $10, $11,
		$12, $13, $14, $15, $16,
		$17, $18, $19, $20, $21,
		$22, $23, $24, $25,
		$26, $27::text::jsonb, $28, $29::text::jsonb,
		$30, $31, $32, $33::text::jsonb,
		$34, $35, $36
	)
	ON CONFLICT (tenant_id, request_id, partition_date) DO UPDATE SET
		session_id          = EXCLUDED.session_id,
		turn_no             = EXCLUDED.turn_no,
		ts                  = EXCLUDED.ts,
		client_model        = EXCLUDED.client_model,
		provider_id         = EXCLUDED.provider_id,
		client_profile      = EXCLUDED.client_profile,
		virtual_ip          = EXCLUDED.virtual_ip,
		virtual_mac         = EXCLUDED.virtual_mac,
		affinity_hit        = EXCLUDED.affinity_hit,
		transform_rule_id   = EXCLUDED.transform_rule_id,
		gw_task_id          = EXCLUDED.gw_task_id,
		api_key_prefix      = EXCLUDED.api_key_prefix,
		owner_user          = EXCLUDED.owner_user,
		application_code    = EXCLUDED.application_code,
		key_alias           = EXCLUDED.key_alias,
		api_key_owner_user  = EXCLUDED.api_key_owner_user,
		auto_profile        = EXCLUDED.auto_profile,
		confidence_num      = EXCLUDED.confidence_num,
		model_chosen        = EXCLUDED.model_chosen,
		strategy_used       = EXCLUDED.strategy_used,
		compression_reason  = EXCLUDED.compression_reason,
		outbound_msg_count  = EXCLUDED.outbound_msg_count,
		outbound_token_est  = EXCLUDED.outbound_token_est,
		outbound_msg_hashes = EXCLUDED.outbound_msg_hashes,
		quality_flags       = EXCLUDED.quality_flags,
		quality_fix_actions = EXCLUDED.quality_fix_actions,
		quality_score       = EXCLUDED.quality_score,
		stream_chunk_errors = EXCLUDED.stream_chunk_errors,
		stream_chunks_sent  = EXCLUDED.stream_chunks_sent,
		attachments         = EXCLUDED.attachments,
		request_type        = EXCLUDED.request_type,
		request_class       = EXCLUDED.request_class,
		due_at              = EXCLUDED.due_at`

// SessionTurnDetailsWriter writes the 731 details feature layer, sharing the
// turn+bodies transaction.
type SessionTurnDetailsWriter struct {
	// available gates the writer on table presence (probe once at wiring);
	// false makes UpsertDetailsInTx a no-op for pre-731 databases.
	available bool
}

// NewSessionTurnDetailsWriter returns a writer; available comes from the
// wiring layer's one-time probe (details family present = migration 731
// applied). False keeps every write a no-op — pre-731 databases degrade to
// the 710 view shape with zero errors.
func NewSessionTurnDetailsWriter(available bool) *SessionTurnDetailsWriter {
	return &SessionTurnDetailsWriter{available: available}
}

// Available reports whether the underlying details family exists.
func (w *SessionTurnDetailsWriter) Available() bool {
	return w != nil && w.available
}

// UpsertDetailsInTx writes one details row inside the shared turn
// transaction. Zero-value pointers persist as SQL NULL.
func (w *SessionTurnDetailsWriter) UpsertDetailsInTx(ctx context.Context, tx pgx.Tx, rec DetailsRecord) error {
	if w == nil || !w.available {
		return nil
	}
	partitionDate := calendarDate(rec.Ts)
	if _, err := tx.Exec(ctx, insertDetailsSQL,
		rec.SessionID, rec.TurnNo, rec.TenantID, rec.RequestID, rec.Ts, partitionDate,
		rec.ClientModel, rec.ProviderID, rec.ClientProfile, rec.VirtualIP, rec.VirtualMAC,
		rec.AffinityHit, rec.TransformRuleID, rec.GwTaskID, rec.APIKeyPrefix, rec.OwnerUser,
		rec.ApplicationCode, rec.KeyAlias, rec.APIKeyOwnerUser, rec.AutoProfile, rec.ConfidenceNum,
		rec.ModelChosen, rec.StrategyUsed, rec.CompressionReason, rec.OutboundMsgCount,
		rec.OutboundTokenEst, jsonStringOrNil(rec.OutboundMsgHashes), stringArrayOrNil(rec.QualityFlags),
		jsonStringOrNil(rec.QualityFixActions),
		rec.QualityScore, rec.StreamChunkErrors, rec.StreamChunksSent,
		jsonStringOrNil(rec.Attachments),
		rec.RequestType, rec.RequestClass, rec.DueAt,
	); err != nil {
		return fmt.Errorf("write session_turn_details: %w", err)
	}
	return nil
}

// jsonStringOrNil renders a raw JSON payload as a SQL NULL when empty, else a
// text cast input (::text::jsonb at the SQL boundary, 22P02-safe per the
// turn_writer convention).
func jsonStringOrNil(raw []byte) interface{} {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// stringArrayOrNil renders a Go string slice as a PG text[] literal, NULL
// when empty.
func stringArrayOrNil(vals []string) interface{} {
	if len(vals) == 0 {
		return nil
	}
	out := "{"
	for i, v := range vals {
		if i > 0 {
			out += ","
		}
		out += `"` + pgArrayEscape(v) + `"`
	}
	out += "}"
	return out
}

// pgArrayEscape doubles backslashes and quotes per PG array literal syntax.
func pgArrayEscape(s string) string {
	out := make([]byte, 0, len(s)+2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}
