package sessionaudit

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// ★ 这道门存在的理由（2026-10-20 复核轮，
//
//	docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.6）：
//
//	GET /api/v1/approvals/{request_id} 在**格式非法的 id**上恒 500：
//	approval_queue.id 是 uuid 列，路由里来的是自由文本 ⇒ PG 在比较时抛
//	22P02（invalid input syntax for type uuid: "1"）⇒ handler 收到通用错误。
//	语义上「格式非法的 id」=「查无此审批」⇒ 应该是 404，不是 500。
//
//	判据直接驱动**分类函数**（与被测逻辑同层）：不是端到端 HTTP，而是
//	「这个 DB 错误该被归一成 not-found 吗」这一个判定。
func TestIsMalformedUUIDInputError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
		why  string
	}{
		{
			name: "22P02 from pg",
			err:  &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type uuid: "1"`},
			want: true,
			why:  "非法 uuid 文本喂给 uuid 列 ⇒ 归一为 not found",
		},
		{
			name: "22P02 wrapped",
			err:  fmt.Errorf("query approval: %w", &pgconn.PgError{Code: "22P02"}),
			want: true,
			why:  "被 fmt.Errorf 包过也要能认出来（errors.As 要能穿透）",
		},
		{
			name: "other pg error",
			err:  &pgconn.PgError{Code: "42P01", Message: `relation "x" does not exist`},
			want: false,
			why:  "缺表是环境问题，不能伪装成 not found",
		},
		{
			name: "non pg error",
			err:  errors.New("connection reset"),
			want: false,
			why:  "非 PG 错误一律不归一",
		},
		{
			name: "nil",
			err:  nil,
			want: false,
			why:  "nil 不是错误",
		},
	}
	for _, tc := range cases {
		if got := isMalformedUUIDInputError(tc.err); got != tc.want {
			t.Errorf("%s: isMalformedUUIDInputError = %v, want %v（%s）", tc.name, got, tc.want, tc.why)
		}
	}
}
