// store.go — capture 三表族的事务写入与 body_ref 签发（P0-3 M1 D5）。
package userscriptcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
)

// ErrDuplicateIdempotencyKey 表示 (tenant, idempotency_key) 已存在——
// 调用方把它计为 duplicate 而非失败（重试语义）。
var ErrDuplicateIdempotencyKey = errors.New("userscript capture: idempotency key already captured")

// Store 依赖 pgx 事务执行器（生产注入 pool，realdb 测试注入 tx）。
type Store struct {
	db interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}
}

// NewStore 构造。
func NewStore(pool *pgxPoolShim) *Store { return &Store{db: pool} }

// OutboxWriter 是同事务写 outbox 事件的钩子（M2：ADR D6——用户 API
// 同事务写、非 fail-open）。装配层注入 insertOutboxEvent(tx, envelope)。
type OutboxWriter func(ctx context.Context, tx pgx.Tx, envelope outbox.EventEnvelope) error

// Persist 写入整个批次（单事务）：batch 头 + 逐条 message/body +（可选）
// 同事务的 userscript.captured.v1 outbox 事件。
// 返回逐条 ack（duplicate 标记重放命中的消息）。
func (s *Store) Persist(ctx context.Context, tenantID, endUserID string, batch Batch, writeOutbox OutboxWriter) (Ack, error) {
	ack := Ack{Messages: make([]MessageAck, 0, len(batch.Messages))}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ack, fmt.Errorf("userscript capture: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	batchID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.capture_batches (
			batch_id, tenant_id, end_user_id, source_system, client_session_key,
			correlation_id, schema_version, message_count
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		batchID, tenantID, endUserID, batch.SourceSystem, batch.ClientSessionKey,
		batch.CorrelationID, batch.SchemaVersion, len(batch.Messages),
	); err != nil {
		return ack, fmt.Errorf("userscript capture: insert batch: %w", err)
	}

	for i := range batch.Messages {
		m := &batch.Messages[i]
		msgID := uuid.NewString()
		bodyRef := BodyRefPrefix + msgID
		sum := sha256.Sum256([]byte(m.Content))

		// ★ 每条消息一个 SAVEPOINT：PG 事务内唯一约束失败（23505）会
		// abort 整个事务，"跳过重复继续处理其余消息"必须先回滚到本条
		// 的保存点（realdb 测试实测抓到：无保存点时后续 insert 报 25P02）。
		sp := fmt.Sprintf("capture_sp_%d", i)
		if _, err := tx.Exec(ctx, "SAVEPOINT "+sp); err != nil {
			return ack, fmt.Errorf("userscript capture: savepoint: %w", err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO public.capture_messages (
				capture_message_id, tenant_id, batch_id,
				source_conversation_id, source_message_id, parent_message_id,
				sequence_no, content_revision, role, body_ref, idempotency_key, occurred_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			msgID, tenantID, batchID,
			m.SourceConversationID, m.SourceMessageID, m.ParentMessageID,
			m.Sequence, m.ContentRevision, m.Role, bodyRef, m.IdempotencyKey, m.OccurredAt,
		)
		if err != nil {
			// 唯一约束去重路径：idempotency 或 source+revision 已捕获过。
			// PG 对 INSERT 违反唯一约束报 23505（不是 RowsAffected=0）。
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				if _, rbErr := tx.Exec(ctx, "ROLLBACK TO "+sp); rbErr != nil {
					return ack, fmt.Errorf("userscript capture: rollback to savepoint: %w", rbErr)
				}
				ack.Duplicates++
				ack.Messages = append(ack.Messages, MessageAck{
					SourceMessageID: m.SourceMessageID, Duplicate: true,
				})
				continue
			}
			return ack, fmt.Errorf("userscript capture: insert message %s: %w", m.SourceMessageID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.capture_bodies (body_ref, tenant_id, capture_message_id, content, sha256)
			VALUES ($1,$2,$3,$4,$5)`,
			bodyRef, tenantID, msgID, m.Content, hex.EncodeToString(sum[:]),
		); err != nil {
			return ack, fmt.Errorf("userscript capture: insert body %s: %w", m.SourceMessageID, err)
		}
		ack.Accepted++
		ack.Messages = append(ack.Messages, MessageAck{
			SourceMessageID: m.SourceMessageID, BodyRef: bodyRef,
		})
	}

	// M2（ADR D6）：同事务写 userscript.captured.v1——用户 API 非
	// fail-open，事件与数据同 commit。ack.Messages 与 batch.Messages
	// 按索引一一对齐（Persist 主循环顺序 append）；只广播本轮新接受
	// 的消息（重放全 duplicate 时不广播，事件 event_id 幂等兜底）。
	if writeOutbox != nil && ack.Accepted > 0 {
		full := make([]outbox.CapturedMessageHeader, 0, ack.Accepted)
		for i, m := range ack.Messages {
			if m.Duplicate || i >= len(batch.Messages) {
				continue
			}
			src := &batch.Messages[i]
			full = append(full, outbox.CapturedMessageHeader{
				SourceConversationID: src.SourceConversationID,
				SourceMessageID:      src.SourceMessageID,
				ParentMessageID:      derefString(src.ParentMessageID),
				Sequence:             src.Sequence,
				ContentRevision:      src.ContentRevision,
				Role:                 src.Role,
				OccurredAt:           src.OccurredAt,
				IdempotencyKey:       src.IdempotencyKey,
				BodyRef:              m.BodyRef,
			})
		}
		envelope, err := outbox.BuildUserscriptCapturedEventV1(
			tenantID, batchID, batch.SourceSystem, batch.ClientSessionKey,
			batch.CorrelationID, endUserID, full,
		)
		if err != nil {
			return ack, fmt.Errorf("userscript capture: build event: %w", err)
		}
		if err := writeOutbox(ctx, tx, envelope); err != nil {
			return ack, fmt.Errorf("userscript capture: outbox: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ack, fmt.Errorf("userscript capture: commit: %w", err)
	}
	ack.BatchID = batchID
	return ack, nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// SessionExists 报告 (tenant, session) 是否存在于 canonical 会话族
//（public.sessions，430 建）。M3：capture 响应 canonical_session 标注
// 的前提——不存在的会话不标注（防伪造标注）。
func (s *Store) SessionExists(ctx context.Context, tenantID, sessionID string) bool {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	err = tx.QueryRow(ctx,
		`SELECT 1 FROM public.sessions WHERE tenant_id = $1 AND session_id = $2 LIMIT 1`,
		tenantID, sessionID).Scan(&one)
	return err == nil
}

// pgxPoolShim 适配 *pgxpool.Pool 的 Begin（避免包级硬依赖 pgxpool 类型，
// 测试可注入自造 Beginner）。
type pgxPoolShim struct {
	begin func(ctx context.Context) (pgx.Tx, error)
}

// Begin implements the minimal pool surface Store needs.
func (p *pgxPoolShim) Begin(ctx context.Context) (pgx.Tx, error) { return p.begin(ctx) }

// PoolBeginner 由装配层构造：把 *pgxpool.Pool 的 Begin 适配进来。
func PoolBeginner(begin func(ctx context.Context) (pgx.Tx, error)) *pgxPoolShim {
	return &pgxPoolShim{begin: begin}
}
