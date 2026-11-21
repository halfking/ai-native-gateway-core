package errorplaybook

// 日志上下文采集：PI 智能体分析未知错误时的「log 及上下文」材料。
//
//	来源 = candidate_failure_logs_hot（按 error_kind 的最近 1 小时窗口）：
//	分布（供应商/模型/状态码/次数）+ 最多 3 条错误消息样本。
//	表不在场/查询失败 ⇒ 返回空串（分析仍可进行，只是材料少），不崩。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SQLLogContext 是 LogContextProvider 的 SQL 实现。
type SQLLogContext struct {
	db *sql.DB
}

// NewLogContext 创建日志上下文采集器。
func NewLogContext(db *sql.DB) *SQLLogContext {
	return &SQLLogContext{db: db}
}

// Gather implements LogContextProvider.
func (l *SQLLogContext) Gather(ctx context.Context, errorType string) (string, error) {
	if l == nil || l.db == nil {
		return "", nil
	}
	var b strings.Builder

	rows, err := l.db.QueryContext(ctx, `
		SELECT provider_id::text, COALESCE(raw_model_name, ''),
		       COALESCE(extracted_upstream_status_code, upstream_status_code, 0),
		       COUNT(*)
		FROM candidate_failure_logs_hot
		WHERE error_kind = $1 AND ts > NOW() - interval '1 hour'
		GROUP BY 1, 2, 3
		ORDER BY 4 DESC
		LIMIT 10`, errorType)
	if err != nil {
		if isNotProvisioned(err) {
			return "", nil
		}
		return "", fmt.Errorf("gather error distribution failed: %w", err)
	}
	total := int64(0)
	distRows := 0
	for rows.Next() {
		var provider, model string
		var status int64
		var cnt int64
		if err := rows.Scan(&provider, &model, &status, &cnt); err != nil {
			rows.Close()
			return "", fmt.Errorf("scan error distribution failed: %w", err)
		}
		total += cnt
		distRows++
		if distRows <= 8 {
			fmt.Fprintf(&b, "- %s / %s / status=%d: %d 次\n", provider, model, status, cnt)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", fmt.Errorf("iterate error distribution failed: %w", err)
	}
	rows.Close()

	if total == 0 {
		return "", nil
	}
	fmt.Fprintf(&b, "窗口内合计 %d 次（错误类型 %s）\n", total, errorType)

	// 消息样本（每条裁到 400 字）。
	sampleRows, err := l.db.QueryContext(ctx, `
		SELECT error_message FROM candidate_failure_logs_hot
		WHERE error_kind = $1 AND ts > NOW() - interval '1 hour'
		ORDER BY ts DESC LIMIT 3`, errorType)
	if err != nil {
		return b.String(), nil // 分布已有 ⇒ 样本失败不致命
	}
	defer sampleRows.Close()
	idx := 0
	for sampleRows.Next() {
		var msg sql.NullString
		if err := sampleRows.Scan(&msg); err != nil {
			break
		}
		if !msg.Valid || strings.TrimSpace(msg.String) == "" {
			continue
		}
		idx++
		fmt.Fprintf(&b, "- 样本%d: %s\n", idx, truncateRunes(msg.String, 400))
	}
	return b.String(), nil
}
