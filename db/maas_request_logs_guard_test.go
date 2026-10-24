package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureMaasSchema_HasNoV1FamilyDDL —— 2026-10-24「v1 → session_turns 替换轮」。
//
// # 这道门的**极性在 2026-10-24 翻转了**
//
// 翻转前（§10.97 起）：门钉的是「v1 的 credits_charged DDL 必须被 catalog 守卫
// 挡住」—— 因为 `ALTER TABLE … ADD COLUMN IF NOT EXISTS` **不省锁**：PG 必须先对
// 父表和每一个分区取 ACCESS EXCLUSIVE 才能判断列在不在，一个已经存在几个月的
// 列照样要付一次全量独占扫描（生产实测 420 次调用、均值 2,289 ms、最长单次
// 104,322 ms，对象是库里最忙的表）。
//
// 翻转后：**v1 族已随 890 退役 ⇒ 那条 DDL 整个不存在了** ⇒ 没有锁可挡。
// ⇒ 门从「DDL 必须被守卫」翻成「**DDL 不许存在**」——这比原判据**更强**：
//
//	原判据允许「守卫写对了 ⇒ DDL 照跑」（只是不白锁）；新判据是零 DDL。
//
// # 为什么「删掉」比「改写成会话族版本」好
//
// 见 db/session_family_contract_test.go 的头注：把这条 ALTER 搬到
// session_turns 上 = 把一次已知锁事故搬到一个**更热、分区更多**的对象上。
// ⇒ 正确形态是「迁移链建列 + CI 门证明投影 ⊆ 迁移产物」。
//
// # 保留的两条正/反向判据
//
//	① 定价表批次（maas_settings / model_credit_rates）**必须还在** ——
//	   那是计费的价格来源，不是 v1 族。
//	② EnsureMaasSchema **必须仍然**挂在启动序列上。
func TestEnsureMaasSchema_HasNoV1FamilyDDL(t *testing.T) {
	src, err := os.ReadFile("maas_schema.go")
	if err != nil {
		t.Fatalf("read maas_schema.go: %v", err)
	}
	text := string(src)

	t.Run("v1_ddl_must_not_exist_anymore", func(t *testing.T) {
		// 曾经的守卫与 DDL 常量都必须**消失**：
		//   maasRequestLogsDDL     —— 给 _request_logs 补 credits_charged
		//   maasRequestLogsCurrent  —— 它的 catalog 守卫
		for _, gone := range []string{
			"maasRequestLogsDDL",
			"maasRequestLogsCurrent",
		} {
			if strings.Contains(text, gone) {
				t.Errorf("maas_schema.go 里仍有 %q —— v1 族已随 890 退役，这条 DDL "+
					"只会在 v1 还在的库上白取一次 ACCESS EXCLUSIVE（锁事故的最后一处来源）",
					gone)
			}
		}

		// ⚠ 断的是**可执行部分**（字符串字面量），不是「这个词不许出现」：
		//   maas_schema.go 里保留着「这里原来有一条 v1 腿」的注释 —— 那是承重证据。
		//   （纯文本判据会把注释当成 DDL ⇒ 门恒红 ⇒ 门被关掉 ⇒ 真缺陷没人管。）
		if refs := v1WriteRefsInFuncLiterals(t, "maas_schema.go", "EnsureMaasSchema"); len(refs) > 0 {
			t.Errorf("EnsureMaasSchema 的**可执行部分**里仍有对 v1 关系的写/DDL：\n  %s\n"+
				"⇒ v1 族已随 890 退役 ⇒ 运行时会 42P01 ⇒ boot 中止。", strings.Join(refs, "\n  "))
		}
	})

	t.Run("pricing_batch_is_preserved", func(t *testing.T) {
		// 反向判据：这道门不许变成「顺手把定价表也删了」的通行证。
		fn := sliceBetween(text, "func (d *DB) EnsureMaasSchema(", "\n}\n")
		if fn == "" {
			t.Fatal("EnsureMaasSchema not found in maas_schema.go")
		}
		for _, want := range []string{
			"CREATE TABLE IF NOT EXISTS maas_settings",
			"base_credits_per_1m_cache_out",
			"global_discount",
			"CREATE TABLE IF NOT EXISTS model_credit_rates",
			"manual_cache_out",
		} {
			if !strings.Contains(fn, want) {
				t.Errorf("EnsureMaasSchema 必须保留 %q（定价表批次不是 v1 族）", want)
			}
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		dbGo, err := os.ReadFile("db.go")
		if err != nil {
			t.Fatalf("read db.go: %v", err)
		}
		if !strings.Contains(string(dbGo), "EnsureMaasSchema(") {
			t.Error("EnsureMaasSchema must still be called from db.go")
		}
	})
}

// sliceBetween 取 [start, end) 之间的文本；end 为空时取到函数末尾。
func sliceBetween(text, start, end string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if end == "" {
		return rest
	}
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j+len(end)]
}
