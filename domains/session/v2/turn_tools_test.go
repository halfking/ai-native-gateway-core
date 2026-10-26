package v2

// turn_tools_test.go —— 轮次级 tools 列的**写路径**门（迁移 889，审计 G-6）。
//
// # 这道门防什么
//
// `public.session_turns.tools` 曾是**死列**：456 建了它，但 turn_writer 的
// INSERT 列清单里没有它 ⇒ 每行都停在默认值 '[]'::jsonb ⇒ 视图
// request_logs_with_current_month 投影出的 tool_calls **恒为空数组**。
// db/request_logs_view_schema.go 的投影旁白甚至把这件事写成了注释
// （「⚠ tools 尚无写入路径」）。
//
// # 为什么断言必须落在**实际发给服务端的 SQL** 上
//
// 只断言「turnToolsJSONBExpr 常量里有 session_tools_hot」是不够的：
// 写方完全可能**根本没把它接进 INSERT**（列清单里没有 tools），
// 而那条门照样全绿。⇒ 这里用 pgxmock 的自定义 QueryMatcherFunc 把
// **真实下发的 SQL 原文**抓出来，在上面做结构化断言：
//
//	① 列清单（"INSERT INTO public.session_turns_hot (" 与 ") SELECT" 之间）
//	   **含 tools**；
//	② SELECT 段（") SELECT" 之后）**含**聚合表达式，且表达式里
//	   带 (session_id = $1 AND request_id = $4) 这个连接键；
//	③ 表达式**不含** arguments / result（正文不重复，避免无界放大）；
//	④ 位置参数仍是 101 个（只追加列、不重编号 ⇒ 既有判据不动）。
//
// ★ 断言 ② 里的 $1/$4 不是随手写的：turn_writer 的参数表是
//   $1=session_id, $2=turn_no, $3=tenant_id, $4=request_id。
//   写错成 $2（turn_no）会让子查询恒空 —— 而「恒空」在
//   「列清单里有 tools」这条断言下**看不出来**（列在、值是 []）。
//   ⇒ 这条断言专门堵这个洞。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// capturedSQL 记录 mock 实际收到的 SQL 原文（自定义 matcher 写入）。
type capturedSQL struct{ text string }

func (c *capturedSQL) match(_ string, actual string) error {
	c.text = actual
	return nil
}

// turnToolsInsertParts 把 INSERT 语句切成「列清单」与「SELECT 段」。
func turnToolsInsertParts(t *testing.T, sql string) (cols string, selectPart string) {
	t.Helper()
	up := strings.ToUpper(sql)
	i := strings.Index(up, "INSERT INTO PUBLIC.SESSION_TURNS_HOT (")
	if i < 0 {
		t.Fatalf("没有找到 INSERT INTO public.session_turns_hot：\n%s", sql)
	}
	j := strings.Index(sql[i:], ") SELECT")
	if j < 0 {
		t.Fatalf("没有找到 ') SELECT' 边界：\n%s", sql)
	}
	return sql[i+len("INSERT INTO public.session_turns_hot (") : i+j], sql[i+j+len(") SELECT"):]
}

// TestTurnWriterFillsToolsColumnFromSessionTools 是 G-6 的写路径正题。
func TestTurnWriterFillsToolsColumnFromSessionTools(t *testing.T) {
	cap := &capturedSQL{}
	mock, err := pgxmock.NewConn(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherFunc(cap.match)))
	require.NoError(t, err)
	defer func() { _ = mock.Close(context.Background()) }()

	rec := TurnRecord{
		SessionID:      "sess-tools",
		TenantID:       "tenant-tools",
		RequestID:      "req-tools",
		Ts:             time.Now().UTC(),
		SourceKind:     "live",
		ClientProtocol: "openai-chat",
		AgentType:      "assistant",
		TaskType:       "chat",
	}

	args := make([]interface{}, 0, 101)
	for i := 0; i < 101; i++ {
		args = append(args, pgxmock.AnyArg())
	}

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	mock.ExpectExec("INSERT").
		WithArgs(args...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	w := newTurnWriter(mock)
	turnNo, err := w.AppendTurn(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, 1, turnNo)
	require.NoError(t, mock.ExpectationsWereMet())

	sql := cap.text
	require.NotEmpty(t, sql, "没有抓到下发的 SQL")

	cols, selectPart := turnToolsInsertParts(t, sql)

	// ① 列清单里必须有 tools。
	require.Contains(t, cols, "tools",
		"INSERT 的列清单里没有 tools ⇒ 这一列仍停在默认值 '[]'::jsonb（死列没被治）")

	// ② SELECT 段必须真的带聚合表达式，且连接键是 (session_id=$1, request_id=$4)。
	require.Contains(t, selectPart, "jsonb_agg",
		"SELECT 段里没有聚合表达式 ⇒ tools 列的值不会来自 session_tools_hot")
	require.Contains(t, selectPart, "public.session_tools_hot",
		"SELECT 段没有从 public.session_tools_hot 取数")
	require.Contains(t, selectPart, "session_id = $1::text",
		"聚合表达式的 session_id 谓词不是 $1::text（$1 才是 session_id；写成 $2/turn_no 会恒空，"+
			"且缺了显式转型就依赖「目标列与子查询恰好定型一致」这个巧合）")
	require.Contains(t, selectPart, "request_id = $4::text",
		"聚合表达式的 request_id 谓词不是 $4::text（$4 才是 request_id）")

	// ③ 不得复制正文（arguments / result）。
	for _, forbidden := range []string{"arguments", "result"} {
		require.NotContains(t, selectPart, forbidden,
			fmt.Sprintf("聚合表达式里出现了 %q —— 工具正文是无界的，复制到轮次元数据行上会放大存储", forbidden))
	}

	// ④ 摘要字段必须齐全（否则读到的不是「工具调用」而是残缺信息）。
	for _, field := range []string{"tool_name", "tool_call_id", "status", "duration_ms", "error_type"} {
		require.Contains(t, selectPart, field,
			fmt.Sprintf("聚合表达式缺摘要字段 %q", field))
	}
}

// TestTurnToolsExpressionUsesOnlyHotTable 钉住「只读 hot 表」这个选择。
//
// 为什么不同时查月分区母表：promote_session_tools_hot_to_partition 只搬
// created_at < now() - retention 的行（retention 默认 8h，且管理端强制 > 0 ⇒
// 最小 1h）⇒ **当前请求的工具行必然还在 hot 表里**。轮次行是在请求结束时
// 写的，工具行在请求进行中写的，两者相隔以秒计 ⇒ 读 hot 表就够。
//
// 这条断言的作用是：万一有人日后把表达式改成查母表（或两张都查），
// 这里会红并把「为什么 hot 表就够」这段理由带出来。
func TestTurnToolsExpressionUsesOnlyHotTable(t *testing.T) {
	require.NotContains(t, turnToolsJSONBExpr, "public.session_tools t",
		"聚合表达式开始查月分区母表了 —— 说明作者不知道 promote 只搬「超过 retention 的旧行」")
	require.Contains(t, turnToolsJSONBExpr, "FROM public.session_tools_hot t",
		"聚合表达式没有从 session_tools_hot 取数")
	require.Contains(t, turnToolsJSONBExpr, "COALESCE(jsonb_agg(",
		"聚合表达式必须用 COALESCE 兜底 ⇒ 没有工具时落 '[]' 而不是 NULL（列是 NOT NULL）")
}
