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

// Test862CommentsAreGuardedLikeTheirCreateLegs —— r1030 真库实测：
// request_logs 腿的创建被 to_regclass 守卫，可脚本末尾两条
// `COMMENT ON INDEX` 是无条件的。在该族缺席的环境（860 之前的旧名形态、
// 或已 DROP 的族——**正是这条守卫存在的理由所覆盖的场景**），
// 整文件在 COMMIT 之后以
//
//	ERROR: relation "idx_request_logs_ts" does not exist
//
// 非零退出。dbinit 的 applySQL 用 `psql -v ON_ERROR_STOP=1` 执行并只按
// 退出码记账，于是该迁移永远不被记为 applied，每次 boot 重放、每次同样
// 失败（索引其实早已建好，IF NOT EXISTS 命中）——一个永久的迁移卡点。
//
// 静态钉法：本文件里所有顶层语句都顶格（DO 块体内的语句一律缩进）。
// 因此「顶格的 COMMENT ON INDEX」就是无守卫的 COMMENT。
func Test862CommentsAreGuardedLikeTheirCreateLegs(t *testing.T) {
	up := stmt862(t, idx862File)
	for i, ln := range strings.Split(up, "\n") {
		if strings.HasPrefix(ln, "COMMENT ON INDEX") {
			t.Errorf("862 第 %d 行是无守卫的顶层 COMMENT ON INDEX：%s\n"+
				"守卫掉的对象不能留无条件 COMMENT（r1030 真库实测 exit=3，"+
				"迁移永不记账）。请与创建腿同守卫：放进 to_regclass 判定的 DO 块。", i+1, ln)
		}
	}
	// 正向钉子：两条注释都必须在守卫块内真的写着。
	if !strings.Contains(up, "to_regclass('public.idx_request_logs_ts')") ||
		!strings.Contains(up, "to_regclass('public.idx_session_turns_ts')") {
		t.Errorf("862 的两条 COMMENT 必须各自先经 to_regclass 判定；守卫被删掉后 " +
			"缺席族环境会重新变回 exit=3 的迁移卡点")
	}
}

// Test862SetsStatementTimeout —— 12h 审计轮补的 P1 反向钉。
//
// 862 是 850–863 区间唯一「建索引却不设 statement_timeout」的迁移。
// 共享 PG 服务端 statement_timeout=30s（849/855/856 都为此各补过一次
// SET LOCAL，实录 57014），而本迁移的 session_turns 腿建在**活写**分区
// 父表上并级联到每个分区，252 审计轮实测 idx_session_turns_ts 单条 94.2s
// （2026_09 79.8 万 + 2026_10 12.8 万行）——远超 30s。
//
// 后果与守卫击穿同族：两条 CREATE INDEX 同处一个事务，57014 后整体回滚、
// 不留部分状态，但 psql_file 只按退出码记账 ⇒ 永不 applied，每次部署
// 重放、每次同样失败 = 永久迁移卡点，且只会在数据量已达百万级的存量库上
// 炸（全新安装 session_turns 为空，反而看不出问题）。
func Test862SetsStatementTimeout(t *testing.T) {
	up := stmt862(t, idx862File)
	if !strings.Contains(up, "SET LOCAL statement_timeout") {
		t.Errorf("862 缺少 SET LOCAL statement_timeout：共享 PG 服务端 30s 会掐断 94.2s 的 " +
			"idx_session_turns_ts 构建，dbinit/部署链只按退出码记账 ⇒ 永久迁移卡点（849/855/856 同形先例）")
	}
}

// Test862EmbeddedCopyMatchesCanonical —— 双点同步钉（installer embed 与
// canonical 字节必须一致）。只改一处会让 CI 构建出的安装器带着与正典不同
// 的 SQL 上门，是 849/850/851 漏同步那一族的通用护栏。
func Test862EmbeddedCopyMatchesCanonical(t *testing.T) {
	canonical := repoFile(t, "sql/migrations/startup/"+idx862File)
	embed := repoFile(t, "installer/cmd/llm-gw-installer/embeddata/startup/"+idx862File)
	if embed != canonical {
		t.Errorf("862 embed 漂移：installer/cmd/llm-gw-installer/embeddata/startup/%s 与 canonical SQL 不一致", idx862File)
	}
}
