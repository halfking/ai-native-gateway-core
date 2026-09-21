package v2

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// R50 F15（2026-09-21）：730 sessions 归因三列写入方钉桩。
// 此前三列零 Go 写入方——真库实测 289,471 行 agent_role<>main 与
// parent_session_id IS NOT NULL 均为 0，两条部分索引空转。

// TestUpsertSessionSnapshot_SqlCarriesAttributionColumns 锁 upsert 的列契约：
// INSERT 三列 + 首值优先冲突臂 + 空值归一（'main' 列默认 / NULLIF 父列）。
// QueryMatcherRegexp 下 ExpectExec 的正则即形状锁——实际 SQL 必须同时包含
// 全部关键片段才被匹配。字符串钉桩是格式锁，列存在性由真库三件套验证
// （见 R50 审计文档），本测试锁的是"SQL 形状不被后续改动无声漂移"。
func TestUpsertSessionSnapshot_SqlCarriesAttributionColumns(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	shape := regexp.QuoteMeta("INSERT INTO public.sessions") + `.*` +
		regexp.QuoteMeta("agent_role, parent_session_id, parent_task_id") + `.*` +
		regexp.QuoteMeta("COALESCE(NULLIF($20, ''), 'main')") + `.*` +
		regexp.QuoteMeta("NULLIF($21, ''), NULLIF($22, '')") + `.*` +
		regexp.QuoteMeta("WHEN public.sessions.agent_role = 'main' THEN EXCLUDED.agent_role") + `.*` +
		regexp.QuoteMeta("parent_session_id = COALESCE(EXCLUDED.parent_session_id, public.sessions.parent_session_id)") + `.*` +
		regexp.QuoteMeta("parent_task_id = COALESCE(EXCLUDED.parent_task_id, public.sessions.parent_task_id)")
	mock.ExpectExec(shape).
		WithArgs(
			"sess-attr", "tenant-1",
			time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
			0, 0, 0.0,
			0, "", "",
			"", "",
			"",
			"", "", "", "",
			"", "", "",
			// $20..$22：归因三列实参顺序（与 expectAggregateUpsert 一致）
			"worker", "gw_parent", "task-1",
			time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	update := SessionUpdate{
		SessionID:       "sess-attr",
		TenantID:        "tenant-1",
		AgentRole:       "worker",
		ParentSessionID: "gw_parent",
		ParentTaskID:    "task-1",
		UpdatedAt:       time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, upsertSessionSnapshot(context.Background(), mock, update, update.UpdatedAt.Truncate(24*time.Hour)))
}
