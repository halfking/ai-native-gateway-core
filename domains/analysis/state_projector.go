// Package analysis — state_projector.go
//
// SessionStateProjector 把热路径 SessionState（compression.SessionState 的 v6
// 审计字段）投影到 session_tags，与 SessionTagger（OLAP 投影）共写同一张表。
// 这是"每会话打标统一层"的另一半：让安全/合规/审批结论跨模块可读。
//
// 设计（见 docs/2026-07-09-session-tagging-redaction-architecture.md §2.2）：
//   - 复用 SessionTagger 的 UPSERT SQL（ON CONFLICT DO NOTHING），不新建表。
//   - 复用 analysis.DB 接口（pgxpool），与 tagger 同一依赖注入。
//   - 投影是幂等、best-effort 的：失败仅 warn，不阻断热路径。
//   - 与 tagger 的 tag_key 词汇表正交（task/client/llm/... vs security/compliance/...）。
package analysis

import (
	"context"
	"fmt"
	"log/slog"
)

// SessionStateProjection 是从 SessionState v6 字段投影出来的最小视图。
// 由调用方（cache_update_hook）从 compression.SessionState 构造，避免本包
// 反向依赖 compression 包（compression 不应被 analysis 引入）。
type SessionStateProjection struct {
	GwSessionID      string
	TenantID         string
	AuditScore       int    // 0-10
	SecurityScore    int    // 0-10
	SensitiveDetected bool
	PIIStripped      bool
	ApprovalStatus   string // pending|approved|rejected|timeout|""
	OptimizationTag  string // strip_tools|compress_thinking|summarize|""
}

// SessionStateProjector 把 SessionStateProjection 写入 session_tags。
type SessionStateProjector struct {
	db     DB
	logger *slog.Logger
}

// NewSessionStateProjector 构造投影器。db 为 nil 时 Project 变 no-op（安全降级）。
func NewSessionStateProjector(db DB, logger *slog.Logger) *SessionStateProjector {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionStateProjector{db: db, logger: logger}
}

// Project 把一个 SessionStateProjection 的 v6 字段投影为多条 session_tags 行。
// 幂等（ON CONFLICT DO NOTHING）；任何单条失败仅 warn，不返回 error（热路径不阻断）。
func (p *SessionStateProjector) Project(ctx context.Context, s SessionStateProjection) error {
	if p == nil || p.db == nil || s.GwSessionID == "" {
		return nil
	}
	tags := deriveStateTags(s)
	if len(tags) == 0 {
		return nil
	}
	const upsertSQL = `
		INSERT INTO session_tags (gw_session_id, tenant_id, tag_key, tag_value, tag_source, confidence)
		VALUES ($1, $2, $3, $4, 'auto', 1.0)
		ON CONFLICT (gw_session_id, tag_key, tag_value) DO NOTHING`
	var failed int
	for _, tag := range tags {
		if _, err := p.db.Exec(ctx, upsertSQL, s.GwSessionID, s.TenantID, tag.Key, tag.Value); err != nil {
			p.logger.Warn("state_projector: failed to save tag",
				"gw_session_id", s.GwSessionID, "tag_key", tag.Key, "error", err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("state_projector: failed %d/%d tags for %s", failed, len(tags), s.GwSessionID)
	}
	return nil
}

// deriveStateTags 把 SessionStateProjection 映射为 tagEntry 列表（Source 固定 auto）。
// 只投影非零/有意义的状态，避免写入噪音标签。
func deriveStateTags(s SessionStateProjection) []tagEntry {
	var tags []tagEntry
	// security: 风险等级（由 SecurityScore 0-10 映射）
	if s.SecurityScore > 0 || s.AuditScore > 0 {
		tags = append(tags, tagEntry{Key: "security", Value: securityLevel(s.SecurityScore), Source: "auto", Confidence: 1.0})
	}
	// compliance: 敏感检出
	if s.SensitiveDetected {
		tags = append(tags, tagEntry{Key: "compliance", Value: "sensitive_detected", Source: "auto", Confidence: 1.0})
	}
	// pii: 脱敏状态
	switch {
	case s.PIIStripped:
		tags = append(tags, tagEntry{Key: "pii", Value: "stripped", Source: "auto", Confidence: 1.0})
	case s.SensitiveDetected:
		tags = append(tags, tagEntry{Key: "pii", Value: "detected", Source: "auto", Confidence: 1.0})
	}
	// approval: 审批状态
	if s.ApprovalStatus != "" {
		tags = append(tags, tagEntry{Key: "approval", Value: s.ApprovalStatus, Source: "auto", Confidence: 1.0})
	}
	// optimization: 应用的优化
	if s.OptimizationTag != "" {
		tags = append(tags, tagEntry{Key: "optimization", Value: s.OptimizationTag, Source: "auto", Confidence: 1.0})
	}
	return tags
}

// securityLevel 把 0-10 的 SecurityScore 映射为 risk 标签值。
func securityLevel(score int) string {
	switch {
	case score >= 8:
		return "risk:high"
	case score >= 5:
		return "risk:medium"
	case score > 0:
		return "risk:low"
	default:
		return "risk:none"
	}
}
