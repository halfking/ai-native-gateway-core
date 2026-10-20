//go:build !integration

package telemetry

import (
	"strings"
	"testing"
)

// TestS3Wave2StopWriteReadSources（2026-10-13 S3 波 2）钉住停写切换后本包
// 两个 v1 直读面的源形状。判据同层：断言驱动包级 SQL 变量——它们就是
// FindRecentGatewaySession / lookupTurnNumber 实际发给 PostgreSQL 的文本。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本文件**在本轮范围之外**（父会话只派了
// client_test.go / final_success_claim_test.go），但迁移 890 删掉了生产侧的
// lookupTurnNoV1FallbackSQL ⇒ 本文件**编译不过**，整个包的测试二进制都构建不出来。
// ⇒ 此处做**最小编译修复**（3 处断言按新形状收敛），完整重写留给该文件的所有者。
//
// 形状变化：
//
//	① FindRecentGatewaySession：v1 那条 UNION 腿整段删除（v1 族落库后读它必然
//	   42P01）⇒ 只剩会话族热表一条腿，窗口谓词也随之只剩一处。
//	② lookupTurnNumber：v1 回退腿整段删除（lookupTurnNoV1FallbackSQL 随生产侧
//	   一起删除）⇒ 会话族两腿恒 0 时 turn_no 取 1。
//	   「回退腿不得复活」由 client_test.go 的 TestLookupTurnNumber 守着
//	   （录制器数语句条数）。
func TestS3Wave2StopWriteReadSources(t *testing.T) {
	t.Run("FindRecentGatewaySession 单热腿（v1 腿随 890 退役）", func(t *testing.T) {
		q := findRecentGatewaySessionSQL
		// ★ 2026-10-20：v1 腿（FROM public._request_logs_hot）随 890 退役。
		if !strings.Contains(q, "FROM public.session_turns_hot") {
			t.Error("会话粘滞查找缺少 FROM public.session_turns_hot：会话会查不到")
		}
		if strings.Contains(q, "_request_logs") {
			t.Error("会话粘滞查找不得再引用随 890 退役的 v1 族（读了必然 42P01）")
		}
		// 会话腿 api_key_id 是 TEXT（707 S1a），必须显式 ::text 对齐 int 参数。
		if !strings.Contains(q, "t.api_key_id = $2::text") {
			t.Error("会话腿 api_key_id 缺 ::text 对齐（TEXT 列与 int 参数直接比较会 42883）")
		}
		// 粘滞语义：窗口/排序/限幅必须成立，取最新一条。
		// ★ 2026-10-20：v1 腿删除后窗口谓词只剩一处（原为 2 处）。
		if got := strings.Count(q, "NOW() - ($4 * INTERVAL '1 second')"); got != 1 {
			t.Errorf("窗口谓词应出现一次（实存 %d 处）", got)
		}
		if !strings.HasSuffix(strings.TrimSpace(q), "LIMIT 1") {
			t.Error("查找必须 LIMIT 1（最新一条）")
		}
	})

	t.Run("lookupTurnNumber 会话族权威（v1 回退腿随 890 退役）", func(t *testing.T) {
		s := lookupTurnNoSessionSQL
		for _, leg := range []string{"FROM public.session_turns_hot WHERE session_id = $1",
			"FROM public.session_turns WHERE session_id = $1"} {
			if !strings.Contains(s, leg) {
				t.Errorf("turn_no 会话读缺少 %q——停写后新会话的序号会恒 1", leg)
			}
		}
		if !strings.Contains(s, "MAX(turn_no)") {
			t.Error("turn_no 应读 MAX(turn_no)（序号权威在会话族，COUNT 会漏补写行）")
		}
		// ★ 2026-10-20（迁移 890，v1 族退役）：v1 回退腿（lookupTurnNoV1FallbackSQL）
		// 随生产侧整段删除——v1 族落库后读它必然 42P01 ⇒ 会话族两腿恒 0 时
		// turn_no 取 1（会话首轮）。本轮据此删除了对 lookupTurnNoV1FallbackSQL
		// 的引用（该常量已不存在，留着编译不过）。
		// 「回退腿不得复活」由 client_test.go 的 TestLookupTurnNumber 守着：
		// 它用 SQL 录制器数语句条数——若 v1 回退查询被加回来，录制到的语句会变成
		// 2 条，用例变红（只看返回值分辨不出，因为 lookupTurnNumber 读失败会吞错
		// 返回 1）。
	})
}
