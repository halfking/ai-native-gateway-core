package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureWorkTypeSchema_HasNoV1FamilyDDL —— 2026-10-24「v1 → session_turns 替换轮」。
//
// # 这道门的**极性在 2026-10-24 翻转了**
//
// 翻转前（§10.84 起）：门钉的是「v1 的 work_type DDL 必须被 catalog 守卫挡住」
// —— 因为 `ALTER TABLE … ADD COLUMN IF NOT EXISTS` **不省锁**：PG 必须先对父表
// 和每一个分区取 ACCESS EXCLUSIVE 才能判断列在不在（生产实测最长单次
// 109,456 ms，对象是库里最忙的表、5 个月分区 ⇒ 6 个关系被锁，只为一条 no-op）。
//
// 翻转后：**v1 族已随 890 退役 ⇒ 那条 DDL 整个不存在了** ⇒ 没有锁可挡。
// ⇒ 门从「DDL 必须被守卫」翻成「**DDL 不许存在**」——比原判据更强：
//
//	原判据允许「守卫写对了 ⇒ DDL 照跑」（只是不白锁）；新判据是零 DDL。
//
// # 为什么 work_type 本身没丢
//
// work_type 在会话族由**迁移链**提供（session_turns.work_type），canonical
// 视图把它投影成 v1 同名列 ⇒ 读方零改动。
// ⇒ 「v1 那一列谁来保证存在」换了主人：迁移链 + CI 门
//
//	（db/session_family_contract_test.go），而不是 boot 里的 ALTER。
//
// # 保留的两条正/反向判据
//
//	① workTypeSchemaSQL（work_type_config / work_type_model_route 两张配置小表）
//	   **必须还在** —— 它不是 v1 族，且是 work_type 路由配置的真源。
//	② ensureWorkTypeSchema **必须仍然**挂在启动序列上。
func TestEnsureWorkTypeSchema_HasNoV1FamilyDDL(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)

	t.Run("v1_ddl_and_its_guard_must_not_exist_anymore", func(t *testing.T) {
		for _, gone := range []string{
			"workTypeRequestLogsDDL",
			"workTypeRequestLogsCurrent",
		} {
			if strings.Contains(text, gone) {
				t.Errorf("db.go 里仍有 %q —— v1 族已随 890 退役，这条 DDL 只会在 v1 "+
					"还在的库上白取一次 ACCESS EXCLUSIVE（锁事故的最后一处来源）", gone)
			}
		}
	})

	t.Run("ensure_body_no_longer_touches_the_v1_family", func(t *testing.T) {
		// ⚠ 断的是**可执行部分**（字符串字面量），不是「这个词不许出现」：
		//   ensureWorkTypeSchema 里保留着「这里原来有一条 v1 腿」的注释 ——
		//   那是承重证据（下一个读代码的人要知道这一跳去哪了）。
		//   （纯文本判据会把注释当成 DDL ⇒ 门恒红 ⇒ 门被关掉 ⇒ 真缺陷没人管。）
		if refs := v1WriteRefsInFuncLiterals(t, "db.go", "ensureWorkTypeSchema"); len(refs) > 0 {
			t.Errorf("ensureWorkTypeSchema 的**可执行部分**里仍有对 v1 关系的写/DDL：\n  %s\n"+
				"⇒ v1 族已随 890 退役 ⇒ 运行时会 42P01 ⇒ boot 中止。", strings.Join(refs, "\n  "))
		}
	})

	t.Run("work_type_config_batch_is_preserved", func(t *testing.T) {
		// 反向判据：这道门不许变成「顺手把配置表也删了」的通行证。
		if !strings.Contains(text, "const workTypeSchemaSQL = `") {
			t.Fatal("db.go 应当仍然声明 workTypeSchemaSQL")
		}
		schemaSQL := sliceBetween(text, "const workTypeSchemaSQL = `", "\n`")
		for _, want := range []string{
			"CREATE TABLE IF NOT EXISTS work_type_config",
			"CREATE TABLE IF NOT EXISTS work_type_model_route",
			"CREATE INDEX IF NOT EXISTS idx_work_type_config_category",
		} {
			if !strings.Contains(schemaSQL, want) {
				t.Errorf("workTypeSchemaSQL 必须保留 %q（配置小表不是 v1 族）", want)
			}
		}
	})

	t.Run("wired_into_the_startup_sequence", func(t *testing.T) {
		if !strings.Contains(text, "ensureWorkTypeSchema(migCtx)") {
			t.Error("ensureWorkTypeSchema must still be called from the startup sequence")
		}
	})
}
