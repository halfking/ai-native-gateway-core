//go:build !integration

package main

import (
	"strings"
	"testing"
)

// ── 退役记录（2026-10-20，迁移 890：v1 request_logs 族整体退役）─────────────
//
// 本文件原先只有一道门：`TestS4DriftScopeReadsBothStorageFaces`，钉的是
// **一条 SQL 常量的形状**——对账范围 SQL（`mirrorDriftScopeSQL`）必须同时读
// v1 族的两个存储面（`_request_logs_hot` ∪ `_request_logs`）与会话族的两个面
// （`session_turns_hot` ∪ `session_turns`）。
//
// # 那道门为什么退役
//
//  1. **被测对象不存在了**：v1 族被 890 整体 DROP，那条对账范围 SQL 已随生产
//     代码一起删除（`dual_read_validator.go` 里现在只剩一段说明为什么删）。
//     门还在、对象没了 ⇒ 它只能靠「把断言删掉」变绿，而那不是门。
//  2. **它守的不变量整体换题了**：「读全两个存储面」守的是「只读父表会把
//     『最近 N 小时没有 v1』误报成空窗」——那是**v1 还在写、但父表落后 9 小时**
//     的形态。v1 族被 DROP 之后这个形态不存在了：没有「父表落后 hot」这回事，
//     **「只读一面」这个错误也无处可犯**。
//
// # 风险现在由谁承担
//
// 退役把「读少了」这个错误换成了两个更要紧的错误：
//
//   - **还在量**：把退役态报成「已测量」——`v1_coverage_pp` 报 0 而不是 -1、
//     `zero_drift` 报 true 而不是 false ⇒ 读起来像「两侧一致、窗口干净」。
//     ⇒ 由 s4_gate_coverage_faces_test.go 与 dual_read_validator_test.go 承担。
//   - **还在查**：拿已 DROP 的表名去查——任何提到 v1 族的语句都是 42P01
//     （**整条语句**失败，不是那条腿失败）。
//     ⇒ 由本文件的下面那道门 + dual_read_validator_test.go 的「零查询」门承担。
//
// # 本文件现在的门
//
// `TestDriftReconciliationMentionsNoRetiredV1Relation`：**反转**原断言——
// 从「SQL 必须读到 v1 的两个面」变成「对账这条路上**不许再出现 v1 族的关系名**」。
// 这不是把门放宽：原门守的是「读全」，新门守的是「别再读」，
// 而后者是当前唯一还可能犯的错。
func TestDriftReconciliationMentionsNoRetiredV1Relation(t *testing.T) {
	// 判据打在**去掉注释后**的生产源码上：判据一旦能在注释里被满足，就不是判据
	// （同款注记见 dual_read_gate_test.go 的 stripGoComments）。
	clean := stripGoComments(readGoSource(t, "dual_read_validator.go"))
	if mentionsRetiredV1Family(clean) {
		t.Error("dual_read_validator.go 的**代码**里仍然出现 v1 族的关系名 —— " +
			"那一族已被迁移 890 整体 DROP，任何提到它的语句都是 42P01（整条语句失败，" +
			"不是那条腿失败）。对账已退役 ⇒ 这条路上不该再有 v1 SQL。")
	}

	// ★ 判据的正控制：检查器必须**看得见** v1 族，否则上面那条恒绿。
	if !mentionsRetiredV1Family("SELECT 1 FROM _request_logs_hot") {
		t.Fatal("判据没有牙：一个确实提到 _request_logs_hot 的片段被判成「没提到」")
	}
	// 负控制：不能误伤会话族，也不能误伤规范视图 public.request_logs
	//（那是**另一个**关系，迁移 890 之后是会话族的两臂视图）。
	for _, other := range []string{
		"SELECT 1 FROM session_turns_hot",
		"SELECT 1 FROM public.request_logs",
	} {
		if mentionsRetiredV1Family(other) {
			t.Errorf("判据误伤：%q 不含 v1 族，却被判成提到了", other)
		}
	}
}

// mentionsRetiredV1Family 报「这段 SQL/代码里提到了 v1 族的关系名」。
//
// 为什么用 strings.Contains 而**不是** containsIdentifier（按标识符边界匹配）：
// containsIdentifier 要求 needle 之后不是标识符字节，而 '_' **就是**标识符字节
// ⇒ containsIdentifier(code, "_request_logs") 对 "_request_logs_hot" 返回 **false**，
// 整族都漏掉。这里要的就是「族」语义（前缀命中即算命中）。
//
// 反过来，族名前缀带下划线 ⇒ 规范视图 public.request_logs（**另一个**关系）
// 与设置键 storage.request_logs_write_enabled 都不会被误伤。
func mentionsRetiredV1Family(code string) bool {
	return strings.Contains(code, "_request_logs")
}

// TestS4WindowFloorIsWeakerThanTheDocumentedExitCondition 记录一处
// **门比 spec 宽**的差异，并且**故意不让它红**。
//
// spec 自己的退出条件（settings/spec_storage.go，S4 停写 gate 那一条）：
//
//	「关闭前提：dual_read_validator 对账 7 天零漂移（plan §4 S2 退出条件）」
//
// 门实际强制的窗口下限是 `s4MinWindowHours = 24`。
// 文件头的注释说明这个常数是**照着** 7 天条件的漏检率（0.08%）推出来的，
// 但下限本身是 24 小时 ⇒ **一个 24 小时窗口就足以让门说 ready，
// 而 spec 要求的是 7 天。**
//
// 这不是缺陷判定，是**属主的决定**：spec 那句可能是文档没跟上实现，
// 也可能实现有意放宽。两种改法方向相反 ——
// 把下限提到 168h 会让门更严（可能长期不 ready），
// 把 spec 改成 24h 会放宽一个危险开关的书面条件。
// 所以本测试只**把它打印出来**，不判定对错。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：对账退役后，`s4GateVerdictOf`
// 在生产里**已无调用点**（SummarizeFrom 不再调用它），这个纯函数现在只被测试
// 调用。所以这道门记录的是「这个函数**如果**被重新接上，窗口下限有多宽」——
// 别把它读成「现网正在用 24 小时窗口放行停写」：现网根本没在测。
func TestS4WindowFloorIsWeakerThanTheDocumentedExitCondition(t *testing.T) {
	t.Logf("S4 窗口下限 = %d 小时；spec 写的退出条件是「7 天零漂移」= %d 小时。%s",
		s4MinWindowHours, 7*24,
		"下限比书面条件宽 7 倍 ⇒ 24h 干净即可放行，而 spec 要求 7 天。属主定。")

	// 门能拒绝的四种理由，逐条列出来（供对照「现在卡在哪一条」）。
	for _, r := range []string{
		s4GateReasonV1WritesDisabled,
		s4GateReasonNoV1Traffic,
		s4GateReasonInsufficientV1Coverage,
		s4GateReasonWindowTooShort,
	} {
		if strings.TrimSpace(r) == "" {
			t.Errorf("门的一个否决理由是空串 —— 输出里会少一条 operator 提示")
		}
	}
}
