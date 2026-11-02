// bodyref.go — capture body_ref 的读取端（P0-3 M2）：
// 供 admin.handleControlledBody 对 internal://capture/{uuid} 前缀的
// ref 取正文。归属校验在本查询（tenant_id 过滤——跨租户 ref 返回
// not found，对齐 ASM FetchBody 契约第 3 条"归属校验在 Gateway"）。
package userscriptcapture

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrCaptureBodyNotFound 表示 ref 不存在或不属于该租户（两种情况对外
// 同形——不泄露其他租户的 ref 是否存在）。
var ErrCaptureBodyNotFound = errors.New("userscript capture: body_ref not found for tenant")

var captureRefPattern = regexp.MustCompile(`^internal://capture/[0-9a-fA-F-]{36}$`)

// IsCaptureBodyRef 报告 ref 是否 capture 命名空间。
func IsCaptureBodyRef(ref string) bool {
	return strings.HasPrefix(ref, BodyRefPrefix) && captureRefPattern.MatchString(ref)
}

// CaptureMessageID 从 ref 提取 uuid 部分（调用方先经 IsCaptureBodyRef）。
func CaptureMessageID(ref string) string {
	return strings.TrimPrefix(ref, BodyRefPrefix)
}

// CaptureBodyRow 是读取端返回的正文行。
type CaptureBodyRow struct {
	Content     string
	SHA256      string
	ContentType string
	CreatedAt   time.Time
	Role        string // 来自 capture_messages.role（响应组装用）
}

// RowQuerier 是读取端的最小查询面（与 admin.controlledBodyDB 同款签名，
// *pgxpool.Pool / pgxmock 均满足）。
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LookupCaptureBody 按 ref + tenant 取正文行；不存在/跨租户统一
// ErrCaptureBodyNotFound。
func LookupCaptureBody(ctx context.Context, q RowQuerier, ref, tenantID string) (CaptureBodyRow, error) {
	var row CaptureBodyRow
	err := q.QueryRow(ctx, `
		SELECT b.content, b.sha256, b.content_type, b.created_at, m.role
		FROM public.capture_bodies b
		JOIN public.capture_messages m ON m.capture_message_id = b.capture_message_id
		WHERE b.body_ref = $1 AND b.tenant_id = $2`,
		ref, tenantID,
	).Scan(&row.Content, &row.SHA256, &row.ContentType, &row.CreatedAt, &row.Role)
	if err != nil {
		return CaptureBodyRow{}, ErrCaptureBodyNotFound
	}
	return row, nil
}
