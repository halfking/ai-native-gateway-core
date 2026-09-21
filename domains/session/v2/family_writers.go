// Package v2 — family_writers.go
//
// 存储优化方案 v2 S1a（migration 706）三新表的 Go 写入面。
// 目前承载 session_memora（会话初始环境/上下文快照）；session_censors 由
// security/sanitize 中间件双写（见 security/sanitize/db_sink.go），
// session_tools 由 domains/toolexecution 写链直写（postgres_store.go 已切表）。
package v2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// memoraDB is the minimal surface the memora writer needs; both
// *pgxpool.Pool and pgx.Tx satisfy it (same pattern as bodiesDB).
type memoraDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// SessionMemoraWriter persists the per-session initial environment/context
// snapshot into public.session_memora_hot (promoted to monthly partitions by
// PartitionManager). Insert-only: the plan fixes the write point at "first
// turn persisted" and the (tenant_id, session_id, partition_date) unique
// constraint makes retries/concurrent first turns no-ops.
//
// Boundary note (plan §9): session_memora is an initial-context snapshot, NOT
// a memory store — the memory body lives in the external kxmemory product and
// its memora_* tables must never be touched from here.
type SessionMemoraWriter struct {
	db memoraDB
}

// NewSessionMemoraWriter wires a memora writer on the session family pool.
func NewSessionMemoraWriter(db memoraDB) *SessionMemoraWriter {
	return &SessionMemoraWriter{db: db}
}

// MemoraSnapshot carries the initial environment/context of a session.
type MemoraSnapshot struct {
	SessionID string
	TenantID  string
	Ts        time.Time

	// ClientEnv: 客户端类型/版本/入口/项目/agent。
	ClientEnv map[string]any
	// InitialContext: system prompt digest + 初始消息指纹 + 可用工具清单。
	InitialContext map[string]any
}

// buildMemoraSnapshot derives the first-turn snapshot from the ProcessedRequest
// (kept next to the writer so the Write() call site stays small).
func buildMemoraSnapshot(req *ProcessedRequest) *MemoraSnapshot {
	env := map[string]any{
		"client_type": req.ClientType,
		"project_id":  req.ProjectID,
		"namespace":   req.Namespace,
		"agent_name":  req.AgentName,
		"agent_type":  req.AgentType,
	}
	if req.APIKeyID != "" {
		env["api_key_id"] = req.APIKeyID
	}
	if req.ApplicationID != "" {
		env["application_id"] = req.ApplicationID
	}

	ctxMap := map[string]any{
		"turn_no": 1,
	}
	// system prompt digest + 指纹：只落摘要/指纹，不落原文（sessions 家族
	// "主表不含请求内容" 的同一纪律，快照表亦最小化）。
	var systemTexts []string
	for _, msg := range req.RequestBody {
		if msg.Role == "system" {
			systemTexts = append(systemTexts, msg.Content)
		}
	}
	if len(systemTexts) > 0 {
		digest := sha256.Sum256([]byte(fmt.Sprint(systemTexts)))
		ctxMap["system_digest"] = hex.EncodeToString(digest[:16])
		ctxMap["system_msg_count"] = len(systemTexts)
	}
	allDigest := sha256.New()
	for _, msg := range req.RequestBody {
		fmt.Fprintf(allDigest, "%s\x00%s\x00", msg.Role, msg.Content)
	}
	ctxMap["first_messages_fingerprint"] = hex.EncodeToString(allDigest.Sum(nil)[:16])
	ctxMap["message_count"] = len(req.RequestBody)

	return &MemoraSnapshot{
		SessionID:      req.SessionID,
		TenantID:       req.TenantID,
		Ts:             req.Timestamp,
		ClientEnv:      env,
		InitialContext: ctxMap,
	}
}

// WriteMemoraSnapshotInTx inserts the snapshot within the caller's tx
// (SessionWriterV2.Write 的 turn+bodies 同一事务，spec §6.2)。
func (w *SessionMemoraWriter) WriteMemoraSnapshotInTx(ctx context.Context, tx memoraDB, snap *MemoraSnapshot) error {
	if w == nil || w.db == nil || tx == nil {
		return nil
	}
	envJSON, err := json.Marshal(snap.ClientEnv)
	if err != nil {
		return fmt.Errorf("marshal memora client_env: %w", err)
	}
	ctxJSON, err := json.Marshal(snap.InitialContext)
	if err != nil {
		return fmt.Errorf("marshal memora initial_context: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO public.session_memora_hot (
			session_id, tenant_id, client_env, initial_context,
			created_at, partition_date
		) VALUES (
			$1, $2, $3::text::jsonb, $4::text::jsonb, NOW(), $5
		)
		ON CONFLICT (tenant_id, session_id, partition_date) DO NOTHING
	`, snap.SessionID, snap.TenantID, string(envJSON), string(ctxJSON), calendarDate(snap.Ts))
	if err != nil {
		return fmt.Errorf("insert memora snapshot: %w", err)
	}
	return nil
}
