package main

// ★ 2026-10-20（迁移 890，v1 族整体退役）：本文件**取代**了原
// `repair_e2e_realdb_test.go`。
//
// 原门（§9.186）是一道真库端到端门：它往 `_request_logs` 播种两行、
// 往 `session_bodies` / `session_bodies_hot` 各播种一行，然后跑
// `ExecuteRepair`，断言「两个面都被删干净、且重建按 V1 源的每个 turn
// 各产出一行」。
//
// 它验证的那个形状——「从 V1 源重建 V2」——**已经不可能存在**：
// v1 族被 DROP 之后，`LoadV1Turns` 返回 errV1FamilyRetired，
// `ExecuteRepair` 在**任何删除动作之前**就中止了。
//
// ⇒ 原门钉的那个缺陷（§9.183：只删父表、漏 hot 面）**不再可达**：
// 修复路径根本走不到删除那一步。留着一个永远 skip 或永远报错的门，
// 只会让下一个人以为「这个工具还在修东西」。
//
// 代替它的是下面这道**退役契约门**。它钉的是退役之后**必须**成立的东西：
//
//  1. `LoadV1Turns` 返回 errV1FamilyRetired（不是 0 行、不是 nil error）；
//  2. `ExecuteRepair` 把这个错误**如实往上抛**，并且**没有删掉任何东西**
//     （DeletedRows 为空、Success=false）；
//  3. `PlanRepair` / `VerifyRepair` 同样如实失败。
//
// 为什么第 2 条是重点：一条「什么都没做」的修复路径**必须**说成「什么都没做」。
// 如果它返回一个 Success=true、DeletedRows 全 0 的结果，那读起来就是
// 「修复完成，两边都空了」——而事实是「v1 族没了，工具没有第二侧可比」。
// 本仓为这件事付过两次学费（§9.222 截断窗口、§9.235 真空为绿）：
// **「没量」与「量了，没有」必须长得不一样。**

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRetiredFamilyErrorNamesTheMigration 钉住错误文案的**可操作性**。
//
// 一个只说「失败」的错误，对运维的价值是零；而这条错误的读者是**运维**
// （他在生产上手跑这个工具）。文案必须让他立刻知道两件事：
// 这不是环境问题（是设计上的退役），以及去哪查。
func TestRetiredFamilyErrorNamesTheMigration(t *testing.T) {
	msg := errV1FamilyRetired.Error()
	if !strings.Contains(msg, "890") {
		t.Errorf("错误文案没有点名迁移号 890：%q —— 运维需要知道这是设计上的退役，而不是环境坏了", msg)
	}
	if !strings.Contains(msg, "v1") {
		t.Errorf("错误文案没有点名是哪一族退役：%q", msg)
	}
}

// TestExecuteRepairRefusesInsteadOfReportingAFakeSuccess 是本文件的承重门。
//
// 它断言的是**行为**而不是形状：ExecuteRepair 在 v1 退役后必须
// 「响亮地失败」，且**一个字节都没删**。
//
// 阳性对照（为什么这道门不会恒绿）：如果 ExecuteRepair 改成
// 「吞掉错误继续删」，本门会在 DeletedRows 非空时红；如果它改成
// 「吞掉错误并返回 Success=true」，本门会在 Success 上红。
// 两个方向都测到了。
func TestExecuteRepairRefusesInsteadOfReportingAFakeSuccess(t *testing.T) {
	repairer := NewSessionRepairer(nil, NewSessionLoader(nil), nil, nil, nil)

	res, err := repairer.ExecuteRepair(context.Background(), "tenant-x", "session-x")
	if err == nil {
		t.Fatalf("ExecuteRepair err = nil（result=%+v）—— v1 族已退役，它必须失败而不是「修好了」", res)
	}
	if !errors.Is(err, errV1FamilyRetired) {
		t.Errorf("ExecuteRepair err = %v, want it to wrap errV1FamilyRetired", err)
	}
	if res == nil {
		t.Fatal("ExecuteRepair 返回了 nil result —— 调用方会拿到一个空指针而不是一条信息")
	}
	if res.Success {
		t.Error("ExecuteRepair 报告 Success=true —— 退役态下它什么都没做，「成功」是假的")
	}
	if len(res.DeletedRows) != 0 {
		t.Errorf("ExecuteRepair 报告删除了 %v —— 它在 v1 源缺席时本应中止，一个字节都不该删", res.DeletedRows)
	}
	if len(res.InsertedRows) != 0 {
		t.Errorf("ExecuteRepair 报告插入了 %v —— 同样不该发生", res.InsertedRows)
	}
}

// TestPlanAndVerifyAlsoRefuse 覆盖另外两个入口。
//
// 为什么要单独一条：PlanRepair / VerifyRepair 各自调 LoadV1Turns，
// 但它们各自的错误处理是独立写的（不是共用一个入口）——
// 一条只覆盖 ExecuteRepair 的门，会让这两个入口悄悄退化成
// 「返回一个看起来正常的空计划」。
func TestPlanAndVerifyAlsoRefuse(t *testing.T) {
	repairer := NewSessionRepairer(nil, NewSessionLoader(nil), nil, nil, nil)

	plan, err := repairer.PlanRepair(context.Background(), "tenant-x", "session-x")
	if err == nil {
		t.Errorf("PlanRepair err = nil（plan=%+v）—— 它必须失败", plan)
	}
	if plan != nil {
		t.Errorf("PlanRepair 返回了非 nil plan（%+v）—— 失败时不应给出一个可执行的计划", plan)
	}

	report, err := repairer.VerifyRepair(context.Background(), "tenant-x", "session-x")
	if err == nil {
		t.Errorf("VerifyRepair err = nil（report=%+v）—— 它必须失败", report)
	}
	if report != nil {
		t.Errorf("VerifyRepair 返回了非 nil report（%+v）—— 失败时不应给出一份报告", report)
	}
}
