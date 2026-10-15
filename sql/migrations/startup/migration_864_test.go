package startup

import (
	"strings"
	"testing"
)

// 864 的判据核心：裸列前导 + 覆盖列。与 862 的教义方向相反——862 的存在
// 理由是「无谓词」，864 的存在理由是「谓词必须与消费方精确一致」。
//
// 本缺陷的成因正是「形状极像但不可用的索引」：lower(raw_name) 表达式索引
// 对裸列等值 `raw_name = lower(外列)` 永远不可用，uq(canonical_id, raw_name)
// 无前导只能全索引扫。若有人「顺手统一」成 lower(raw_name) 或把列序调成
// (canonical_id, raw_name)，CREATE INDEX IF NOT EXISTS 不会报错、已建好的
// 索引也不会重建——CMI rollup 的 68/h 击杀与凭据自检/探针资格两族的
// 按行放大会在下个流量高峰静默爬回来。两者都必须当场红。

const idx864File = "864_model_aliases_bare_raw_name_active_idx.sql"

// stmt864 剥掉 SQL 注释行与空行，只留可执行语句（862 的 stmt862 同款；
// 注释里正面讨论了 lower(raw_name)/列序，整文件子串匹配会被自己的注释误伤）。
func stmt864(t *testing.T, name string) string {
	t.Helper()
	var kept []string
	for _, ln := range strings.Split(repoFile(t, "sql/migrations/startup/"+name), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "--") {
			continue
		}
		if strings.TrimSpace(ln) == "" {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func Test864IndexIsBareRawNameLeadingWithCoveredCanonicalID(t *testing.T) {
	up := stmt864(t, idx864File)

	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS idx_model_aliases_raw_name_active") {
		t.Fatalf("864 缺目标索引名 —— 预应用环境的幂等跳过按**索引名**匹配")
	}
	if !strings.Contains(up, "ON public.model_aliases (raw_name, canonical_id)") {
		t.Fatalf("864 必须以裸列 (raw_name, canonical_id) 为键：raw_name 前导服务裸列等值，" +
			"canonical_id 在键内使 alias→canonical 子查询 Index Only。改成 lower(raw_name) 即复刻本缺陷" +
			"（表达式索引对裸列等值永远不可用），改成 (canonical_id, raw_name) 即回到无前导全索引扫")
	}
	for _, stmt := range strings.Split(up, ";") {
		s := strings.TrimSpace(stmt)
		if strings.HasPrefix(s, "CREATE INDEX") && strings.Contains(s, "lower(raw_name)") {
			t.Errorf("864 的索引键禁止 lower(raw_name) 表达式（正是本缺陷成因）；违规语句：\n%s", s)
		}
	}
}

func Test864KeepsActivePartialPredicate(t *testing.T) {
	up := stmt864(t, idx864File)
	found := false
	for _, stmt := range strings.Split(up, ";") {
		s := strings.TrimSpace(stmt)
		if !strings.HasPrefix(s, "CREATE INDEX") {
			continue
		}
		found = true
		if !strings.Contains(strings.ToUpper(s), "WHERE STATUS = 'ACTIVE'") {
			t.Errorf("864 的部分谓词必须保留（全部消费方都带 status='active'，谓词一致才能 Index Only）；违规语句：\n%s", s)
		}
	}
	if !found {
		t.Fatal("864 未找到 CREATE INDEX 语句")
	}
}

func Test864DoesNotUseConcurrentlyInsideTransaction(t *testing.T) {
	up := stmt864(t, idx864File)
	if strings.Contains(strings.ToUpper(up), "CONCURRENTLY") {
		t.Error("864 不加 CONCURRENTLY —— 本迁移在 BEGIN 块内执行，CONCURRENTLY 会直接报错" +
			"（cannot run inside a transaction block），2,744 行构建毫秒级也无需它")
	}
}

func Test864SetsStatementTimeout(t *testing.T) {
	up := stmt864(t, idx864File)
	if !strings.Contains(up, "SET LOCAL statement_timeout") {
		t.Error("864 缺少 SET LOCAL statement_timeout：共享 PG 服务端 30s 是部署链 57014 的常设源头" +
			"（849/855/856/862 同形先例），纪律钉不留例外")
	}
}

func Test864DownDropsByIndexName(t *testing.T) {
	down := stmt864(t, "864_model_aliases_bare_raw_name_active_idx.down.sql")
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_model_aliases_raw_name_active") {
		t.Error("864.down 缺按名 DROP")
	}
}

func Test864CommentsAreGuardedLikeTheirCreateLegs(t *testing.T) {
	up := stmt864(t, idx864File)
	for i, ln := range strings.Split(up, "\n") {
		if strings.HasPrefix(ln, "COMMENT ON INDEX") {
			t.Errorf("864 第 %d 行是无守卫的顶层 COMMENT ON INDEX：%s\n"+
				"守卫掉的对象不能留无条件 COMMENT（r1030 真库实测 exit=3，迁移永不记账）。"+
				"请放进 to_regclass 判定的 DO 块。", i+1, ln)
		}
	}
	if !strings.Contains(up, "to_regclass('public.idx_model_aliases_raw_name_active')") {
		t.Error("864 的 COMMENT 必须先经 to_regclass 判定（r1030 同款守卫）")
	}
}

// Test864EmbeddedCopyMatchesCanonical —— 双点同步钉（862 同款）。
func Test864EmbeddedCopyMatchesCanonical(t *testing.T) {
	canonical := repoFile(t, "sql/migrations/startup/"+idx864File)
	embed := repoFile(t, "installer/cmd/llm-gw-installer/embeddata/startup/"+idx864File)
	if embed != canonical {
		t.Errorf("864 embed 漂移：installer/cmd/llm-gw-installer/embeddata/startup/%s 与 canonical SQL 不一致", idx864File)
	}
}
