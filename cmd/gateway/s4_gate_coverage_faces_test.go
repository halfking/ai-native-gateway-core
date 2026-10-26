package main

import (
	"context"
	"testing"
	"time"
)

// ── 退役记录（2026-10-20，迁移 890：v1 request_logs 族整体退役）─────────────
//
// 本文件原先只有一道门：`TestS4GateCoverageSQLReadsBothFaces`，钉的是
// **一条 SQL 常量的形状**——按小时量 v1 覆盖率的 SQL（`v1CoverageHoursSQL`）
// 必须同时读 v1 族的两个面（`_request_logs_hot` ∪ `_request_logs`）与会话族的
// 两个面（`session_turns_hot` ∪ `session_turns`）。
//
// # 那道门为什么退役
//
// 1. **被测对象不存在了**：`v1CoverageHoursSQL` 已随对账一起删除
//    （v1 族被 DROP ⇒ 那条语句 42P01，整条语句失败，不是那条腿失败）。
// 2. **被测的量本身没有对象了**：覆盖率问的是「窗口里有多少小时有 v1 数据」，
//    而这个问题的前提是「有一侧可以比」。退役后**没有第二侧**，
//    「覆盖了多少」这个问题不存在了 ⇒ `V1CoveragePP` 恒为 **-1**（未测量）。
//
// ⇒ 风险从「量错了」换成「**假装量过**」：-1 与 0 必须长得不一样，
//   而「未测量」必须一路传到响应里（S4GateVoid + 退役原因）。
//   下面的门守的就是这一条。
//
// 旧门里那张「两面缺一各会怎样」的对照表（coverageFaceLossConsequence）随被测
// 对象一起删除：它描述的是「少读一个面」的两种后果（假 void / 假 Ready），
// 而退役后**不存在「少读一个面」这回事**——一个面都不读，且如实说没读。
// 「假 Ready」这个风险仍然存在，只是换了来源：把「未测量」当成「够覆盖」
// （见下面的门：退役读数喂给 S4 门必须判不许可）。

// TestS4CoverageIsNotMeasuredAfterV1Retirement 钉住退役态的覆盖率读数。
//
// # 这道门守的是什么
//
// 「未测量」与「测到了 0」必须可区分：
//
//	-1 = 未测量（v1 族已退役，没有第二侧可比）
//	 0 = 测过了，窗口里一个带流量的小时都没有
//
// 这两个数字长得几乎一样，而它们对运维的含义完全相反。
// 本仓吃过这个亏的次数不少（S4 的「真空为绿」是同一根因）。
//
// # 正控制 / 负控制
//
// 下面用**退役态的真实读数**去喂 S4 门（`s4GateVerdictOf`）：它必须判「不许可」；
// 而同一道门喂一组**健康的历史读数**时必须判「许可」。两条同时绿才说明
// 「不许可」是退役读数的性质，而不是这道门坏了。
func TestS4CoverageIsNotMeasuredAfterV1Retirement(t *testing.T) {
	q := &retiredFamilyQuerier{}
	v := NewDualReadValidatorOn(q)
	sum, err := v.SummarizeFrom(context.Background(), "", time.Now(), 168)
	if err != nil {
		t.Fatalf("SummarizeFrom: %v", err)
	}
	if len(q.queries) != 0 {
		t.Errorf("退役态发了 %d 条查询（%v）—— v1 族已被 DROP，不该再有 v1 SQL",
			len(q.queries), q.queries)
	}
	if sum.V1CoveragePP != -1 {
		t.Errorf("V1CoveragePP = %v，期望 -1（未测量）—— 0 会被读成「测到了 0%% 覆盖」",
			sum.V1CoveragePP)
	}
	if !sum.S4GateVoid || sum.S4GateVoidReason != s4GateReasonV1FamilyRetired {
		t.Errorf("退役态必须 void 且原因 = %q，实得 void=%v reason=%q",
			s4GateReasonV1FamilyRetired, sum.S4GateVoid, sum.S4GateVoidReason)
	}
	if sum.S4Ready {
		t.Error("退役态 S4Ready 必须为 false —— 没有第二侧可比")
	}

	// ★ 正控制：把退役态的**真实读数**喂给 S4 门，它必须不许可。
	// 「可以停写」这个许可绝不能由一个**没测过**的窗口发出。
	verdict := s4GateVerdictOf(s4GateInput{
		v1Rows:       sum.V1Rows,
		genuineLoss:  sum.GenuineLossRows,
		v1WritesOn:   sum.V1WritesEnabled,
		v1CoveragePP: sum.V1CoveragePP,
		windowHours:  sum.WindowHours,
	})
	if verdict.Ready {
		t.Errorf("退役态读数喂给 s4GateVerdictOf 得到 Ready=%v —— 「可以停写」这个许可"+
			"绝不能由一个**没测过**的窗口发出", verdict.Ready)
	}
	if !verdict.Void || verdict.Reason == "" {
		t.Errorf("退役态读数喂给 s4GateVerdictOf 必须判 void 且带理由，实得 %+v", verdict)
	}

	// ★ 负控制：同一道门喂一组健康的历史读数必须判「许可」——
	// 否则上面那条「不许可」只是这道门坏了。
	healthy := s4GateVerdictOf(s4GateInput{
		v1Rows: 4789, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 100, windowHours: 168,
	})
	if !healthy.Ready {
		t.Errorf("健康读数喂给 s4GateVerdictOf 得到 %+v，期望 Ready —— "+
			"负控制失效，说明上面两条不构成对照", healthy)
	}
}
