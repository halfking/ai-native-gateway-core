// db_848_v1_family_guard_test.go — 2026-10-20 退役轮：
// ensureBillingUnitAndAudioSeconds（迁移 848）的**显式存在性闸**门。
//
// ★ 这道门为什么存在（缺陷本体）：
//   848 的 ensure 体里有两条 v1 DDL（ALTER TABLE _request_logs_hot / _request_logs
//   加 audio_seconds）。890 把 v1 族连表带分区一起 DROP ⇒ 族不存在时这两条
//   不是「no-op」而是 **42P01** ⇒ db.Open 失败 ⇒ 网关起不来。
//
// ★ 原来的防护是「巧合式安全」：短路条件 hotOK 用 columnsAllPresent 取值，
//   而 columnsAllPresent 的真实语义是「表**不存在** ⇒ 也返回 true」
//   （to_regclass 判据写在 WHERE 里 ⇒ 表不存在 ⇒ missing=0 ⇒ 读作「没缺列」）
//   ⇒ 退役后 hotOK 恒 true ⇒ 短路恒触发 ⇒ 恰好不炸。
//   ⇒ 但这依赖的是一个**注释与实现相反**的 helper 语义：谁按注释把它改成
//   「表不存在 ⇒ false」，boot 就 42P01 挂掉。
//
// ⇒ 修法：848 自己显式探族在不在（与 ensureRequestLogSchema / maas /
//   work_type 三处同一条纪律），v1 DDL 只在族在位时拼进语句。
//   本门钉住这个**显式闸**（含「闸在 DDL 之前」这个位置约束）。
package db

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureBillingUnitAndAudioSeconds_HasExplicitV1FamilyGate(t *testing.T) {
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

	// ① 必须有**显式**存在性探测（to_regclass），而不是靠 columnsAllPresent
	//    的空真语义。
	if !strings.Contains(fn, "to_regclass('public._request_logs')") &&
		!strings.Contains(fn, "to_regclass('public._request_logs_hot')") {
		t.Fatalf("848 必须显式探测 v1 族是否在位（to_regclass），不能依赖 columnsAllPresent 的「表不存在⇒true」空真语义；got:\n%s", fn)
	}
	// ② v1 DDL 必须被存在性闸**包住**：闸的判断变量必须出现在 v1DDL 的拼装处。
	if !strings.Contains(fn, "v1FamilyPresent") {
		t.Fatalf("848 的存在性闸结果必须参与 v1 DDL 的取舍（v1FamilyPresent）；got:\n%s", fn)
	}
	if !strings.Contains(fn, "if v1FamilyPresent {") {
		t.Fatalf("848 的 v1 DDL 必须在族在位时才拼进语句（if v1FamilyPresent { … }）；got:\n%s", fn)
	}
	// ③ 位置约束：**探针**必须早于 v1 DDL 的拼装（否则闸是装饰）。
	probeAt := strings.Index(fn, "to_regclass('public._request_logs")
	ddlAt := strings.Index(fn, "if v1FamilyPresent {")
	if probeAt < 0 || ddlAt < 0 || probeAt > ddlAt {
		t.Fatalf("848 的存在性探针必须早于 v1 DDL 拼装（probe@%d, ddl@%d）；got:\n%s", probeAt, ddlAt, fn)
	}
	// ④ 两条 v1 ALTER 仍在（改写不能把业务 DDL 删掉）。
	for _, want := range []string{
		"ALTER TABLE _request_logs_hot",
		"ALTER TABLE _request_logs\n",
	} {
		if !strings.Contains(fn, want) {
			t.Fatalf("848 的 v1 DDL 形状被改掉了（缺 %q）；got:\n%s", want, fn)
		}
	}
}
