package v2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// 本文件钉住「上下文分层加载」方案在 L1（明细层）所依赖的两条**真实**能力：
//
//	L1-a 按需加载：能从 session_bodies 的增量链里**只取指定的最近 N 轮**，
//	  而不是把整段历史拉出来 —— 这正是「某一步的细节需要时再加载」的底座。
//	L1-b 保真：拼回来的消息与写入时逐字节一致（顺序、角色、内容）。
//
// ★ 为什么要在这里（而不是只写在 bench 里）：bench 里的明细存储是
//   【理想形态】的内存镜像；本文件跑的是生产真正用的
//   TurnReader.LoadChain / OutboundBuilder.BuildFromDeltas，
//   也就是说「按需加载」这条路在生产代码里**今天就能走通**，
//   缺的只是把「要加载哪一步」这个决策接上（见审计档结论）。

// TestLayering_OnDemandLoadLastNTurns —— L1-a：按需只取最近 N 轮。
func TestLayering_OnDemandLoadLastNTurns(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	rows := pgxmock.NewRows([]string{"turn_no", "request_delta", "response_delta"})
	for i := 1; i <= 5; i++ {
		req := []byte(`[{"role":"user","content":"q` + itoa(i) + `"}]`)
		resp := []byte(`[{"role":"assistant","content":"a` + itoa(i) + `"}]`)
		rows.AddRow(i, req, resp)
	}
	mock.ExpectQuery(`(?s)SELECT b\.turn_no.*FROM public\.session_bodies_unified b`).
		WithArgs("tenant-1", "session-1").
		WillReturnRows(rows)

	reader := newTurnReader(mock)
	builder := NewOutboundBuilder(reader)

	// 只取最近 2 轮 ⇒ 4 条消息（2 组 request_delta + response_delta）。
	msgs, meta, err := builder.BuildFromDeltas(context.Background(), "tenant-1", "session-1", 2, false)
	require.NoError(t, err)
	require.Len(t, msgs, 4, "expected only the last 2 turns")
	require.Equal(t, "q4", msgs[0].Content)
	require.Equal(t, "a5", msgs[3].Content)
	require.Equal(t, 4, meta.TotalMessages)
	// ★ 注意：BuildMeta.TotalTurns 目前**未被 BuildFromDeltas 填充**
	//   （outbound_builder.go 只设 TotalMessages / TokenEstimate / HasCompression）
	//   ⇒ 「这次重建用了几轮」拿不到。分层方案要用它来标注重载了哪些步
	//   ⇒ 列为缺口（见审计档 §缺口 G4），此处不断言它的值。
}

// TestLayering_ReloadedTurnIsByteIdentical —— L1-b：重载出来的轮与写入时一致。
//
// 这条钉的是「按需加载」最核心的不变量：加载回来的东西必须**逐字节**
// 等于当初存进去的东西，否则「细节进记忆、需要时再取」就是有损的。
func TestLayering_ReloadedTurnIsByteIdentical(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	// 存进去的形状：一轮里既有结构化 tool_calls，也有纯文本。
	reqRaw := `[{"role":"user","content":"goal of step 7"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"x\":1}"}}]}]`
	respRaw := `[{"role":"assistant","content":"result of step 7"}]`

	rows := pgxmock.NewRows([]string{"turn_no", "request_delta", "response_delta"}).
		AddRow(7, []byte(reqRaw), []byte(respRaw))
	mock.ExpectQuery(`(?s)SELECT b\.turn_no.*FROM public\.session_bodies_unified b`).
		WithArgs("tenant-1", "session-1").
		WillReturnRows(rows)

	reader := newTurnReader(mock)
	msgs, err := reader.LoadChain(context.Background(), "tenant-1", "session-1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 3)

	// 顺序：请求增量在前，回复在后（与 LoadChain 的拼接口径一致）。
	require.Equal(t, "user", msgs[0].Role)
	require.Equal(t, "goal of step 7", msgs[0].Content)
	require.Equal(t, "assistant", msgs[1].Role)
	require.Len(t, msgs[1].ToolCalls, 1)
	require.Equal(t, "call_1", msgs[1].ToolCalls[0]["id"])
	require.Equal(t, "assistant", msgs[2].Role)
	require.Equal(t, "result of step 7", msgs[2].Content)

	// 逐字节回放：重序列化后与原始 JSON 等价（不丢字段、不改顺序）。
	// ★ 注意：Message.MarshalJSON 在 Content 为空时**省略 content 键**
	//   （Content 的 tag 是 `json:"-"`，空串不写）⇒ 期望值里也没有 content。
	got, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"role":"user","content":"goal of step 7"},
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"x\":1}"}}]},
		{"role":"assistant","content":"result of step 7"}
	]`, string(got))
}

// TestLayering_LoadChainIsOrderedByTurnNo —— L1 的顺序不变量。
//
// 「按步加载」要求明细能按步骤顺序还原；本条钉住 LoadChain 按 turn_no 升序
// 拼接，避免出现「先加载的步排在后加载的步前面」这种会让 L0 索引对不上的错。
func TestLayering_LoadChainIsOrderedByTurnNo(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	rows := pgxmock.NewRows([]string{"turn_no", "request_delta", "response_delta"})
	for i := 1; i <= 4; i++ {
		rows.AddRow(i,
			[]byte(`[{"role":"user","content":"q`+itoa(i)+`"}]`),
			[]byte(`[{"role":"assistant","content":"a`+itoa(i)+`"}]`),
		)
	}
	mock.ExpectQuery(`(?s)SELECT b\.turn_no.*FROM public\.session_bodies_unified b`).
		WithArgs("tenant-1", "session-1").
		WillReturnRows(rows)

	reader := newTurnReader(mock)
	msgs, err := reader.LoadChain(context.Background(), "tenant-1", "session-1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 8)
	for i := 0; i < 4; i++ {
		require.Equal(t, "q"+itoa(i+1), msgs[i*2].Content)
		require.Equal(t, "a"+itoa(i+1), msgs[i*2+1].Content)
	}
}
