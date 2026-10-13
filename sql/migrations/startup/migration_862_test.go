package startup

import (
	"strings"
	"testing"
)

// 862 的判据核心是**两条反向钉子**，与 858 同一教义：
//
// 这条迁移的全部价值 = 「无谓词」。两族分区上**已有**形状极像的索引
// （(ts DESC) WHERE discard_events IS NOT NULL、(ts, id) WHERE digest IS NULL），
// 它们对本缺陷的窗口形态全部不可用。如果哪天有人觉得「全索引浪费」，
// 给 862 加上 WHERE「优化」一下，CREATE INDEX IF NOT EXISTS 不会报错、
// 已建好的索引也不会重建——索引在下次 IF NOT EXISTS 命中后静默退化回
// 整条索引扫，而慢查只会在下一个月度分区变大后爬回来。
//
// 同理，request_logs 腿的表名是 860 之后的 `_` 前缀名；「顺手改回
// request_logs」会在 860 之后的环境直接炸（表不存在），但在预应用过的
// 环境会静默建出第二条孤儿索引。两者都必须当场红。

const idx862File = "862_partition_family_leading_ts_idx.sql"

// stmt862 剥掉 SQL 注释行与空行，只留可执行语句（858 的 stmt858 同款；
// 注释里正面讨论了 WHERE/CONCURRENTLY，整文件子串匹配会被自己的注释误伤）。
func stmt862(t *testing.T, name string) string {
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

func Test862MigrationCreatesUnpredicatedLeadingTsIndexOnParents(t *testing.T) {
	up := stmt862(t, idx862File)

	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS idx_session_turns_ts") ||
		!strings.Contains(up, "ON public.session_turns (ts)") {
		t.Fatalf("862 必须在分区父表 public.session_turns 上建 (ts) 索引；建在单个分区会漏未来月度分区")
	}
	if !strings.Contains(up, "ON public._request_logs (ts)") {
		t.Fatalf("862 的 request_logs 腿必须用 860 之后的 `public._request_logs` 前缀名；" +
			"改回旧名会在 860 之后的环境炸，或在预应用环境建出孤儿索引")
	}
	if !strings.Contains(up, "idx_request_logs_ts") {
		t.Fatalf("862 缺 idx_request_logs_ts 索引名 —— 预应用环境的幂等跳过按**索引名**匹配")
	}
}

func Test862IndexesMustNotBePartial(t *testing.T) {
	up := stmt862(t, idx862File)

	// 只检查 CREATE INDEX 语句本体（剥注释后按分号切）。
	for _, stmt := range strings.Split(up, ";") {
		s := strings.TrimSpace(stmt)
		if !strings.HasPrefix(s, "CREATE INDEX") {
			continue
		}
		if strings.Contains(strings.ToUpper(s), " WHERE ") {
			t.Errorf("862 的索引必须无谓词（本迁移的存在理由就是现存部分索引对窗口形态不可用）；违规语句：\n%s", s)
		}
	}
}

func Test862DoesNotUseConcurrently(t *testing.T) {
	up := stmt862(t, idx862File)
	if strings.Contains(strings.ToUpper(up), "CONCURRENTLY") {
		t.Error("862 不加 CONCURRENTLY —— 分区父表上被拒（842 实测），普通 CREATE INDEX 级联（846/858 先例）")
	}
}

func Test862DownDropsBothIndexes(t *testing.T) {
	down := stmt862(t, "862_partition_family_leading_ts_idx.down.sql")
	for _, name := range []string{"idx_session_turns_ts", "idx_request_logs_ts"} {
		if !strings.Contains(down, "DROP INDEX IF EXISTS public."+name) {
			t.Errorf("862.down 缺 %s 的 DROP", name)
		}
	}
}
