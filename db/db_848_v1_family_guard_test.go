// db_848_v1_family_guard_test.go —— 2026-10-20 退役轮起一道门，
// **2026-10-24「v1 → session_turns 替换轮」把它的极性翻转了**。
//
// # 翻转前（缺陷本体）
//
// 848 的 ensure 体里有两条 v1 DDL（ALTER TABLE _request_logs_hot /
// _request_logs 加 audio_seconds）。890 把 v1 族连表带分区一起 DROP ⇒ 族不存在时
// 这两条不是「no-op」而是 **42P01** ⇒ db.Open 失败 ⇒ 网关起不来。
//
// 原来的防护是「巧合式安全」：短路条件 hotOK 用 columnsAllPresent 取值，而
// columnsAllPresent 的真实语义是「表**不存在** ⇒ 也返回 true」（to_regclass 判据
// 写在 WHERE 里 ⇒ 表不存在 ⇒ missing=0 ⇒ 读作「没缺列」）⇒ 退役后 hotOK 恒 true
// ⇒ 短路恒触发 ⇒ **恰好**不炸。
// ⇒ 但那依赖的是一个**注释与实现相反**的 helper 语义：谁按注释把它改成
//
//	「表不存在 ⇒ false」，boot 就 42P01 挂掉。
//
// ⇒ 当时的修法：848 自己显式探族在不在（显式存在性闸）。
//
// # 翻转后（2026-10-24）
//
// v1 族已退役 ⇒ **那两条 DDL 整个删掉了** ⇒ 没有锁可挡，也没有闸需要挡。
// ⇒ 门从「显式闸必须在、且必须包住 v1 DDL」翻成「**v1 DDL 不许存在**」。
//
//	这比原判据更强：原判据允许「闸写对了 ⇒ DDL 照跑」（只是不 42P01）；
//	新判据是**零 DDL**。
//
// # audio_seconds 去哪了（别把它当成「漏搬」）
//
// 会话族**没有** audio_seconds 列（真库实测：session_turns /
// session_turn_details 都没有；usage_ledger 侧只有 audio_tokens）⇒ 这是**登记过的
// 结构缺口**，不是漏搬：domains/streaming/audio_usage_log.go 的 LogAudioUsage
// 是有文档的 no-op，由 domains/streaming/audio_usage_gate_test.go 钉住。
// ⇒ 媒体时长计费当前走 request 级计量，不是本列。
//
// # 保留的正/反向判据
//
//	① credential_model_bindings.billing_unit **必须还在**（848 的另一半，
//	   不是 v1 族；候选 SELECT 硬读它，缺列即 42703）。
//	② 848 的台账行 **必须还在**。
//	③ ensureBillingUnitAndAudioSeconds **必须仍然**挂在启动序列上。
package db

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureBillingUnitAndAudioSeconds_HasNoV1FamilyDDL(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "func (d *DB) ensureBillingUnitAndAudioSeconds(")
	if start < 0 {
		t.Fatal("ensureBillingUnitAndAudioSeconds not found in db.go")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot find the end of ensureBillingUnitAndAudioSeconds")
	}
	fn := text[start : start+end]

	// ① v1 DDL **必须不存在**（族不存在时 ALTER 它 = 42P01 = boot 中止）。
	for _, gone := range []string{
		"ALTER TABLE _request_logs",
		"ALTER TABLE _request_logs_hot",
		"v1DDL",
		"v1FamilyPresent",
		"to_regclass('public._request_logs')",
		"to_regclass('public._request_logs_hot')",
	} {
		if strings.Contains(fn, gone) {
			t.Errorf("848 的 ensure 里仍有 %q —— v1 族已随 890 退役，这条 DDL 只能 "+
				"是 42P01 ⇒ 整条 boot 迁移中止 ⇒ 网关「就绪但没有数据库」", gone)
		}
	}

	// ①b 再用**可执行部分**口径断一次（字符串字面量）：注释里保留着
	// 「这里原来有一条 v1 腿」的历史证据，那不是 DDL。
	if refs := v1WriteRefsInFuncLiterals(t, "db.go", "ensureBillingUnitAndAudioSeconds"); len(refs) > 0 {
		t.Errorf("ensureBillingUnitAndAudioSeconds 的**可执行部分**里仍有对 v1 关系的写/DDL：\n  %s\n"+
			"⇒ v1 族已随 890 退役 ⇒ 运行时会 42P01 ⇒ boot 中止。", strings.Join(refs, "\n  "))
	}

	// ② 848 的**非 v1** 部分必须还在：cmb.billing_unit + CHECK 约束 + 台账行。
	for _, want := range []string{
		"credential_model_bindings",
		"ADD COLUMN IF NOT EXISTS billing_unit",
		"cmb_billing_unit_check",
		"VALUES ('848'",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("848 的 ensure 必须保留 %q（cmb 侧不是 v1 族：候选 SELECT 硬读它，缺列即 42703）", want)
		}
	}

	// ③ 必须仍然挂在启动序列上。
	if !strings.Contains(text, "ensureBillingUnitAndAudioSeconds(migCtx)") {
		t.Error("ensureBillingUnitAndAudioSeconds must still be called from the startup sequence")
	}
}
