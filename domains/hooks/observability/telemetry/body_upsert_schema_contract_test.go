package telemetry

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// ★ 2026-10-20（迁移 890，v1 族退役）：本用例**退役**。
//
// 退役对象：upsertRequestLogBodies 的 schema 契约（列清单 / SELECT 形态 /
// ON CONFLICT 形态 / 不引用 tenant_id）。
//
// 退役原因：upsertRequestLogBodies —— v1 正文族（_request_logs_bodies_hot /
// _request_logs_bodies / _request_logs_bodies_archive）的写入函数 —— 随 890
// 连同整个 v1 正文族一起删除。v1 族落库后再写它必然 42P01。
//
// 为什么是「退役」而不是「改指」：这条契约守的是「正文只写正文列、不污染
// 主表」。这个不变式的前提是**主表与正文表分家**——正文表退役后本包不再有
// 任何 PG 正文写入，正文落点已迁到会话族（session_bodies /
// session_bodies_hot，由 internal/sessionv2mirror 承接）⇒「只写正文列」在
// 本包侧已无对象可指。
//
// 现在由什么承载：
//
//	① 「本包不写正文」⇒ body_summary_test.go 的
//	   TestUpdateRequestLog_BodiesSummaryModeWritesNoBodyRow /
//	   TestUpdateRequestLog_BodiesFullModeWritesNoBodyRow /
//	   TestInsertRequestLog_BodiesSummaryModeWritesNoBodyRow
//	   （运行时证据：写路径发出的语句里没有任何正文表/正文列）。
//	② 「v1 族整体不得复活」⇒ main_table_body_columns_excluded_test.go 的
//	   TestNoV1RequestLogFamilyWriteRemains（源码级门：本包任何生产源文件的
//	   SQL 字面量都不得指向 v1 族表）。
//	③ 「正文在事务提交后才释放」⇒ client_test.go 的
//	   TestPersistRequestLog_ReleasesBodiesAfterPersistedHooks（仍绿）。
func TestUpsertRequestLogBodiesContract_RetiredWithV1BodyFamily(t *testing.T) {
	// 退役登记：保留一个显式用例，使「这些判据已经不在了」这件事在 go test
	// 的输出里可见——免得下一个会话以为它们还在、或者以为漏跑了什么。
	t.Log("upsertRequestLogBodies 随迁移 890（v1 正文族退役）整条删除；" +
		"正文落点迁到会话族 session_bodies / session_bodies_hot，" +
		"由 internal/sessionv2mirror 与 domains/session/v2 承接。")

	// 退役事实本身的核对：client.go 里不得再有这个函数（防止「注释说退役了、
	// 代码还在」的半退役状态）。
	src, err := os.ReadFile("client.go")
	require.NoError(t, err)
	require.NotContains(t, string(src), "func (c *Client) upsertRequestLogBodies",
		"upsertRequestLogBodies 随 890 退役；若它回来了，v1 正文族就复活了")
	require.NotContains(t, string(src), "_request_logs_bodies_hot",
		"v1 正文族随 890 退役；client.go 里不得再出现该表的写语句")
}
