// data_lifecycle_v1_retirement_test.go — 2026-10-20 退役轮：
// data-lifecycle 自动 hot promote 清单的**v1 族清零**门 + cron 的**能力探针**门。
//
// ★ 缺陷本体（活库实测，每轮 cron 2×WARN + 1×ERROR）：
//
//	890（v1 族真删）把 v1 族（表 + 分区 + 索引）与它的 promote 函数
//	（promote_request_logs_hot_to_partition /
//	  promote_request_logs_bodies_hot_to_partition）**一起 DROP**。
//	而 admin 的 hotPromoteTableMap 里这两项**仍在** ⇒ 自动 hot cron 每轮
//	去调已不存在的函数 ⇒ 42883（function ... does not exist）。
//	而被调用的表**根本不存在**、没有任何东西可搬 ⇒ 纯噪声，且每轮 ERROR
//	会把真实故障淹没。
//
// ⇒ 修法两件：
//
//	① 清单里摘掉两条 v1 死引用（退役语义由 retiredV1HotTables 承载，手动
//	   promote 仍按 promoteHotRetiredReason 拒绝并给理由）；
//	② cron 侧加**能力探针**：promote 函数不存在 ⇒ 记一条 INFO 的 SKIP
//	   （不是 ERROR）⇒ 今后任何被 DROP 的函数都不会再刷错误日志。
//
// 本门守这两条不变量。
package admin

import (
	"os"
	"strings"
	"testing"
)

func TestHotPromoteTableMap_V1EntriesAreExactlyTheRetiredSet(t *testing.T) {
	// ① 清单里**凡是**指向 v1 promote 函数的项，其表必须属于退役集合
	//    （防"换名回潮"：把 v1 族换个表名塞回清单）。
	for table, fn := range hotPromoteTableMap {
		if !strings.HasPrefix(fn, "promote_request_logs_") {
			continue
		}
		if _, retired := retiredV1HotTables[table]; !retired {
			t.Errorf("表 %q 映射到 v1 族的 promote 函数 %q，但它不在 retiredV1HotTables 里——"+
				"要么登记为退役族，要么它根本不该在清单里", table, fn)
		}
	}
	// ② 退役族**必须**仍在清单里：手动 promote 端点靠 map 里的表名去
	//    匹配 promoteHotRetiredReason ⇒ 摘掉条目会把「409 + 退役理由」
	//    变成「不支持该表」（既有退役契约测试会红，实测）。
	for table := range retiredV1HotTables {
		if _, ok := hotPromoteTableMap[table]; !ok {
			t.Errorf("退役族 %q 从 hotPromoteTableMap 消失了——手动 promote 端点会失去"+
				"「409 + retired request_logs family」的拒绝形状", table)
		}
	}
	// ③ 在位的族一个都不能少（防「顺手把整张清单清空」那种变异）。
	for _, want := range []string{
		"session_turns_hot", "session_bodies_hot", "session_turn_details_hot",
		"usage_ledger_hot", "request_wal_hot", "routing_decision_log_hot",
		"credential_model_index_hot", "credit_ledger_hot", "supplier_errors_hot",
	} {
		if _, ok := hotPromoteTableMap[want]; !ok {
			t.Errorf("在位族 %q 从 hotPromoteTableMap 消失了——清单只应移除退役族", want)
		}
	}
}

func TestHotCron_HasPromoteFunctionCapabilityProbe(t *testing.T) {
	src, err := os.ReadFile("data_lifecycle_cron.go")
	if err != nil {
		t.Fatalf("read data_lifecycle_cron.go: %v", err)
	}
	text := string(src)
	// ① 能力探针：查 pg_proc 里函数在不在。
	if !strings.Contains(text, "FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace") {
		t.Fatalf("hot cron 缺 promote 函数的能力探针（查 pg_proc）——"+
			"被 DROP 的函数会让每轮 cron 报 42883 噪声；got:\n%s", cronRegion(text))
	}
	// ② 函数不在 ⇒ 记 INFO 的 SKIP，**不是** ERROR。
	if !strings.Contains(text, "promote function absent") {
		t.Fatalf("hot cron 必须把「promote 函数不存在」记成 SKIP（INFO），不是 ERROR；got:\n%s", cronRegion(text))
	}
	// ③ 位置约束：探针必须在真正调用 promote 函数**之前**（否则是装饰）。
	probeAt := strings.Index(text, "FROM pg_proc p JOIN pg_namespace n")
	callAt := strings.Index(text, "SELECT %s($1::interval, $2::int)")
	if probeAt < 0 || callAt < 0 || probeAt > callAt {
		t.Fatalf("能力探针必须早于 promote 调用（probe@%d, call@%d）", probeAt, callAt)
	}
}

// cronRegion 截出 runOnce 的循环体片段，供失败信息定位。
func cronRegion(text string) string {
	start := strings.Index(text, "func (s *HotCronScheduler) runOnce(")
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		return text[start:]
	}
	return text[start : start+end]
}
