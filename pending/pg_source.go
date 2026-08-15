// pg_source.go — durable 任务表的 PostgreSQL 回源读取（doc 18 §12.1）。
//
// durable_llm_tasks 是任务状态与最终结果的 SSoT（表结构属 Wave 2，
// 字段按 doc 18 §11.1：id/tenant_id/request_id/session_id/status/
// result_ciphertext/result_hash/content_type/completed_at/reason_code 等）。
// 本类型实现 pending.FallbackSource：Redis 投影丢失/不可用时，
// 查询接口从任务表回源返回状态与规范化结果。
//
// 安全：result_ciphertext 用 secret.DecryptWithAAD 以 durable-result 域
// 解密，AAD 绑定 (tenant, task, request hash)；无密钥、AAD/hash 不匹配
// 一律 fail closed（返回错误，不降级、不返回明文）。
//
// 注：durable_llm_tasks 建表 migration 与请求路径接线均属 Wave 2；
// 本轨只验证回源路径的代码级可用性（pgxmock 单测），未联真实 PG。
package pending

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// PGQuerier 是 PGSource 依赖的最小查询接口；*pgxpool.Pool 与
// pgxmock.PgxPoolIface 均满足，便于无真实 PG 的代码级验证。
type PGQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGSource 从 durable_llm_tasks 回源读取 pending 投影。
type PGSource struct {
	db PGQuerier
	kr *secret.Keyring
}

// NewPGSource 构造回源 reader。kr 为 nil 时任何带密文的结果都 fail
// closed（doc 18 §11.2：无密钥不得声称已接管）。
func NewPGSource(db PGQuerier, kr *secret.Keyring) *PGSource {
	return &PGSource{db: db, kr: kr}
}

// pgSourceRow 是回源 SELECT 的目标列（doc 18 §11.1 字段子集）。
const pgSourceColumns = `SELECT id, request_id, tenant_id, status, result_ciphertext, result_hash, content_type, completed_at, reason_code`

// Get 按 (sessionID, requestID) 回源读取最新任务行。
func (p *PGSource) Get(ctx context.Context, sessionID, requestID string) (*Response, bool, error) {
	if p == nil || p.db == nil {
		return nil, false, errors.New("pending: pg source not configured")
	}
	if sessionID == "" || requestID == "" {
		return nil, false, errors.New("pending: SessionID and RequestID required")
	}
	row := p.db.QueryRow(ctx,
		pgSourceColumns+` FROM durable_llm_tasks
			WHERE session_id = $1 AND request_id = $2
			ORDER BY updated_at DESC
			LIMIT 1`, sessionID, requestID)
	return p.scanRow(row, sessionID)
}

// GetLatest 取会话最近更新的任务行。
func (p *PGSource) GetLatest(ctx context.Context, sessionID string) (*Response, string, bool, error) {
	if p == nil || p.db == nil {
		return nil, "", false, errors.New("pending: pg source not configured")
	}
	if sessionID == "" {
		return nil, "", false, errors.New("pending: SessionID required")
	}
	row := p.db.QueryRow(ctx,
		pgSourceColumns+` FROM durable_llm_tasks
			WHERE session_id = $1
			ORDER BY GREATEST(completed_at, updated_at) DESC
			LIMIT 1`, sessionID)
	r, found, err := p.scanRow(row, sessionID)
	rid := ""
	if r != nil {
		rid = r.RequestID
	}
	return r, rid, found, err
}

// scanRow 把任务行投影为 pending.Response；completed 且带密文时解密。
func (p *PGSource) scanRow(row pgx.Row, sessionID string) (*Response, bool, error) {
	var (
		id          string
		requestID   string
		tenantID    string
		dbStatus    string
		resultCT    pgtype.Text
		resultHash  string
		contentType string
		completedAt pgtype.Timestamptz
		reasonCode  string
	)
	if err := row.Scan(&id, &requestID, &tenantID, &dbStatus, &resultCT, &resultHash,
		&contentType, &completedAt, &reasonCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("pending: pg source scan: %w", err)
	}

	r := &Response{
		SessionID:   sessionID,
		TenantID:    tenantID,
		RequestID:   requestID,
		RequestHash: resultHash,
	}
	if completedAt.Valid {
		r.CompletedAt = completedAt.Time.Unix()
	}

	switch dbStatus {
	case "completed":
		r.Status = StatusCompleted
		r.ContentType = contentType
		if !resultCT.Valid || resultCT.String == "" {
			// completed 但无密文：文档不允许（终态必须原子提交加密结果），
			// 视为数据异常，fail closed。
			return nil, false, fmt.Errorf("pending: pg source task %s completed without result ciphertext", id)
		}
		if p.kr == nil {
			return nil, false, fmt.Errorf("pending: pg source task %s: %w", id, secret.ErrAADNoKey)
		}
		binding := secret.AADBinding{TenantID: tenantID, TaskID: id, RequestHash: resultHash}
		pt, _, err := secret.DecryptWithAAD(resultCT.String, p.kr, secret.AADDomainDurableResult, binding)
		if err != nil {
			return nil, false, fmt.Errorf("pending: pg source task %s: %w", id, err)
		}
		r.Body = string(pt)
	case "failed", "expired", "canceled":
		r.Status = StatusFailed
		r.ErrorMessage = reasonCode
	default:
		// running / retry_scheduled / waiting_recovery 等非终态。
		r.Status = StatusInProgress
	}
	return r, true, nil
}
