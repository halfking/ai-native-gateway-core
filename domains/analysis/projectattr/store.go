package projectattr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Store 是 session_project_attribution 的持久化接口。
// 抽成接口是为了让 Hook 的测试不需要真实数据库。
type Store interface {
	// HasAttribution 报告该会话是否已有归属记录。已有则不再重复推断——
	// 包括已被人工 rejected 的，那是明确的"别再猜了"。
	HasAttribution(ctx context.Context, tenantID, gwSessionID string) (bool, error)
	// SaveAttribution 落库；同一会话重复写入按 gw_session_id 幂等。
	SaveAttribution(ctx context.Context, tenantID, gwSessionID string, r Result) error
	// LoadProjects 读 project_dim 快照，供规则层匹配。
	LoadProjects(ctx context.Context, tenantID string) ([]Project, error)
	// LoadSignals 组装推断输入（工种、system prompt、首轮用户文本等）。
	LoadSignals(ctx context.Context, tenantID, gwSessionID string) (Signals, error)
	// HasAuthoritativeProject 报告该会话是否已有 ACC 传入的权威 project_id
	// （session_dim.project_id）。为 true 时整条推断链路直接跳过。
	HasAuthoritativeProject(ctx context.Context, tenantID, gwSessionID string) (bool, error)
}

// PGStore 是 Store 的 PostgreSQL 实现。
//
// 用最小的 Querier 接口而不是直接依赖 *pgxpool.Pool，是为了和仓库里
// domains/analysis 其余组件保持一致，也让单测能注入桩。
type PGStore struct {
	q Querier
}

// Querier 是 PGStore 需要的最小数据库能力。
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Exec(ctx context.Context, sql string, args ...any) error
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Row 抽象单行扫描。
type Row interface {
	Scan(dest ...any) error
}

// Rows 抽象多行扫描。
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// NewPGStore 构造存储层。
func NewPGStore(q Querier) *PGStore { return &PGStore{q: q} }

// ErrNoStore 表示存储层未配置。
var ErrNoStore = errors.New("projectattr: store not configured")

// HasAuthoritativeProject 查 session_dim.project_id。
//
// 这是整条链路的第一道闸：ACC 认领路径已经给了权威值，推断必须让路，
// 否则就会出现"猜测覆盖真实值"这种最坏情况。
func (s *PGStore) HasAuthoritativeProject(ctx context.Context, tenantID, gwSessionID string) (bool, error) {
	if s == nil || s.q == nil {
		return false, ErrNoStore
	}
	const q = `
		SELECT COALESCE(NULLIF(TRIM(project_id), ''), '') <> ''
		FROM public.session_dim
		WHERE tenant_id = $1 AND gw_session_id = $2
		LIMIT 1`
	var has bool
	if err := s.q.QueryRow(ctx, q, tenantID, gwSessionID).Scan(&has); err != nil {
		// 没有 session_dim 行时视作"无权威值"，让推断继续。
		return false, nil //nolint:nilerr // absent row is a valid negative
	}
	return has, nil
}

// HasAttribution 查是否已推断过。
func (s *PGStore) HasAttribution(ctx context.Context, tenantID, gwSessionID string) (bool, error) {
	if s == nil || s.q == nil {
		return false, ErrNoStore
	}
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM public.session_project_attribution
			WHERE tenant_id = $1 AND gw_session_id = $2
		)`
	var exists bool
	if err := s.q.QueryRow(ctx, q, tenantID, gwSessionID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// SaveAttribution 幂等写入。
//
// ON CONFLICT DO NOTHING 而不是 DO UPDATE：人工确认过的结果是最终事实，
// 一次后台重跑不该把它覆盖回 pending。
func (s *PGStore) SaveAttribution(ctx context.Context, tenantID, gwSessionID string, r Result) error {
	if s == nil || s.q == nil {
		return ErrNoStore
	}
	var evidence []byte
	if len(r.Evidence) > 0 {
		b, err := json.Marshal(r.Evidence)
		if err != nil {
			return fmt.Errorf("marshal evidence: %w", err)
		}
		evidence = b
	}
	const q = `
		INSERT INTO public.session_project_attribution
			(gw_session_id, tenant_id, project_ref, project_label, task_ref,
			 method, confidence, status, evidence, created_at, updated_at)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''),
			 $6, $7, $8, $9, NOW(), NOW())
		ON CONFLICT (gw_session_id) DO NOTHING`
	return s.q.Exec(ctx, q,
		gwSessionID, tenantID, r.ProjectRef, r.ProjectLabel, r.TaskRef,
		string(r.Method), r.Confidence, string(r.Status), evidence)
}

// LoadProjects 读取启用中的项目维表。
func (s *PGStore) LoadProjects(ctx context.Context, tenantID string) ([]Project, error) {
	if s == nil || s.q == nil {
		return nil, ErrNoStore
	}
	const q = `
		SELECT project_ref, name,
		       COALESCE(match_keywords, '{}'), COALESCE(repo_paths, '{}')
		FROM public.project_dim
		WHERE enabled AND (tenant_id IS NULL OR tenant_id = $1)`
	rows, err := s.q.Query(ctx, q, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Ref, &p.Name, &p.MatchKeywords, &p.RepoPaths); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LoadSignals 组装推断输入。
//
// 只读 request_logs 已有列，不新增任何采集点。取会话首条请求：智能体的
// 仓库路径/项目上下文几乎总出现在第一轮。
//
// 注意 request_logs 没有独立的 system_prompt 列，请求全文在 request_body
// (jsonb) 里；这里只取轻量的 request_preview 作为文本信号，避免为了归集
// 去反序列化整个 body。identity_hash 代替设备种子做历史继承的关联键。
func (s *PGStore) LoadSignals(ctx context.Context, tenantID, gwSessionID string) (Signals, error) {
	if s == nil || s.q == nil {
		return Signals{}, ErrNoStore
	}
	const q = `
		SELECT COALESCE(work_type, ''), COALESCE(request_preview, ''),
		       COALESCE(identity_hash, ''), COALESCE(api_key_id, 0)
		FROM public.request_logs
		WHERE tenant_id = $1 AND gw_session_id = $2
		ORDER BY ts ASC
		LIMIT 1`
	sig := Signals{GwSessionID: gwSessionID, TenantID: tenantID}
	err := s.q.QueryRow(ctx, q, tenantID, gwSessionID).
		Scan(&sig.WorkType, &sig.UserText, &sig.IdentityHash, &sig.APIKeyID)
	if err != nil {
		return sig, err
	}
	return sig, nil
}

// InheritFromHistory 构造 InheritLookup：找同一客户端指纹近期已确认的项目。
//
// 关联键用 request_logs.identity_hash（会话首条请求的客户端指纹），因为
// session_dim 里没有设备种子列。window 控制回溯窗口：太长会把换了项目的
// 客户端错误继承到旧项目，建议 24h 量级。
//
// 只继承 status='confirmed' 的记录——未经人工确认的推断不应再成为下一次
// 推断的依据，否则一个错误会顺着继承链扩散。
func InheritFromHistory(q Querier, window time.Duration) InheritLookup {
	return func(ctx context.Context, s Signals) (string, error) {
		if q == nil || s.IdentityHash == "" {
			return "", nil
		}
		const sql = `
			SELECT spa.project_ref
			FROM public.session_project_attribution spa
			WHERE spa.tenant_id = $1
			  AND spa.status = 'confirmed'
			  AND spa.project_ref IS NOT NULL
			  AND spa.updated_at > NOW() - ($3 || ' seconds')::interval
			  AND EXISTS (
			      SELECT 1 FROM public.request_logs rl
			      WHERE rl.gw_session_id = spa.gw_session_id
			        AND rl.tenant_id = spa.tenant_id
			        AND rl.identity_hash = $2
			  )
			ORDER BY spa.updated_at DESC
			LIMIT 1`
		var ref string
		if err := q.QueryRow(ctx, sql, s.TenantID, s.IdentityHash,
			int(window.Seconds())).Scan(&ref); err != nil {
			return "", nil //nolint:nilerr // no history is a normal miss
		}
		return ref, nil
	}
}
