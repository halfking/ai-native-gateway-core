package ursm_test

// pg17-proactive-empty-table-cleanup.sh 的行为门（2026-10-06，审计 §10.74）
//
// # 这道门要拦的事故
//
// 该脚本每日 02:00 在 252 上跑，会把「空表」直接 DROP 掉。它的候选 SQL
// 曾经从 pg_stat_user_tables 取数时**既不过滤 relispartition、也不排除 _default**，
// 而命中后的动作是**裸 `DROP TABLE`（不走 DETACH、也不问父表）**。
//
// 后果（生产实测，154 直连）：
//   - 现存 **13 个空的 DEFAULT 分区**，其中 **11 个不在脚本的 10 表白名单**里，
//     不会被第 2 段的 DETACH+DROP 分支接住，会被**第 1 段捞出来裸 DROP**。
//   - `usage_facts_default` 已达 **79.6 MB**，距脚本 100 MB 阈值**只差 20.4 MB**，
//     而它的膨胀来源正是 §10.73.2 记录的「清空后索引不回收」。
//   - DEFAULT 分区一旦被丢，父表 `usage_facts` 就再也接不住越界行，
//     后续 INSERT 直接报错 —— 这是写路径事故，不是省下 80 MB 的收益。
//
// # 为什么不能用「断言源码里有 NOT relispartition」
//
// 那是子串断言，量的是「源码里写过这句话」，不是「运行时真的不 DROP」。
// 本项目已吃过同族亏（§10.70 M68：守卫查询被跑但结果被丢弃，门全绿）。
// 所以本门**注入一个假 docker**，让脚本真的跑到候选循环，
// 再断言**它没有对 `_default` 分区发出 DROP**。
//
// # 门覆盖的两条不同的失效路径
//
// ① SQL 层：`EMPTY_TABLES_SQL` 里的 `NOT c.relispartition` / `NOT LIKE '%_default'`
// ② 行为层：候选行带出的 `is_partition` 列 + 循环里的结构闸（名字 `_default` 兜底）
//
// 两条都必须成立。撤掉任一条，另一条仍能兜住 —— 这正是要测的。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const proactiveScript = "252-monitor/pg17-proactive-empty-table-cleanup.sh"

func proactiveScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", proactiveScript)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("脚本不存在: %v", err)
	}
	return p
}

// fakeDocker 写一个假 `docker`：按调用到的 SQL 内容分支应答，
// 并把所有收到的参数追加到 $TRACE_FILE，供断言检查。
func fakeDocker(t *testing.T, dir string, candidates string) string {
	t.Helper()
	trace := filepath.Join(dir, "trace.txt")
	script := `#!/bin/sh
# 记录完整调用参数（第二行起就是 exec <container> psql ... -c "<SQL>"）
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

all="$*"

case "$all" in
  *pg_stat_activity*VACUUM*)
      echo "0"          # 预检查 3：没有 VACUUM FULL 在跑，放行
      ;;
  *"pg_database_size"*)
      echo "1.00"       # 前后库体积，脚本要拿它算 saved
      ;;
  *relispartition*)
      echo "$CANDIDATES"  # 第 1 段候选查询
      ;;
  *"COUNT(*) FROM"*)
      echo "0"          # 逐个 COUNT(*) 复核
      ;;
  *DROP*)
      echo ""           # DROP 本身
      ;;
  *"SELECT MIN(occurred_at)"*|*"event_id"*)
      echo ""           # 第 2 段 DEFAULT 分支查询（本门不关心）
      ;;
  *)
      echo ""
      ;;
esac
exit 0
`
	p := filepath.Join(dir, "fake-docker")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 docker: %v", err)
	}
	_ = trace
	return p
}

func runProactive(t *testing.T, candidates string, extraEnv ...string) string {
	t.Helper()
	dir := t.TempDir()
	fake := fakeDocker(t, dir, candidates)
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "cleanup.log")
	cooldown := filepath.Join(dir, "cooldown.absent")

	env := append([]string{
		"DOCKER_BIN=" + fake,
		"TRACE_FILE=" + trace,
		"CANDIDATES=" + candidates,
		"LOG=" + logPath,
		"COOLDOWN_FILE=" + cooldown,
		"NOTIFY=" + filepath.Join(dir, "notify-absent"),
		"MIN_TABLE_SIZE_MB=1",
	}, extraEnv...)

	cmd := exec.Command("bash", proactiveScriptPath(t))
	cmd.Env = append(os.Environ(), env...)
	out, _ := cmd.CombinedOutput()
	_ = out

	traceBody, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没跑到查询）: %v", err)
	}
	return string(traceBody) + "\n===LOG===\n" + readFileOr(t, logPath)
}

func readFileOr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// TestProactiveCleanupNeverDropsPartition 是本门的正身。
// 候选里同时给一张普通空表和一个 `_default` 分区：
// 普通表必须被 DROP（证明脚本没被我改瘫），`_default` 必须一个字节都不能被 DROP。
func TestProactiveCleanupNeverDropsPartition(t *testing.T) {
	// 候选行格式：tname|size_mb|n_live|n_dead|is_partition
	candidates := strings.Join([]string{
		`public.some_genuinely_empty_table|150|0|0|0`,
		`public.usage_facts_default|79|0|0|1`,
		`public.request_logs_default|120|0|0|1`,
	}, "\n")

	got := runProactive(t, candidates)

	if !strings.Contains(got, "DROP TABLE IF EXISTS public.some_genuinely_empty_table") {
		t.Fatalf("普通空表应当被 DROP，但轨迹里没有 —— 脚本可能被我改瘫了，判据失去意义\n%s", got)
	}

	for _, forbidden := range []string{
		"DROP TABLE IF EXISTS public.usage_facts_default",
		"DROP TABLE IF EXISTS public.request_logs_default",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("★ 对 DEFAULT 分区发出了 DROP：%s\n这是会打断父表写入路径的事故\n%s", forbidden, got)
		}
	}
}

// TestProactiveCleanupNeverDropsPartitionEvenIfFlagSaysPlain 覆盖**第二道闸**。
// 故意让候选行的 is_partition 列谎报 0（模拟 SQL 层被改坏 / 目录信息过期），
// 名字兜底仍必须拦住 `_default`。
func TestProactiveCleanupNeverDropsPartitionEvenIfFlagSaysPlain(t *testing.T) {
	candidates := `public.usage_facts_default|79|0|0|0` // is_partition 谎报为 0
	got := runProactive(t, candidates)

	if strings.Contains(got, "DROP TABLE IF EXISTS public.usage_facts_default") {
		t.Fatalf("★ is_partition 谎报 0 时，名字兜底必须仍然拦住 _default 分区，但它被 DROP 了\n%s", got)
	}
	if !strings.Contains(got, "SKIP public.usage_facts_default") {
		t.Fatalf("应当在日志里留下 SKIP 记录以便事后反查为什么没删\n%s", got)
	}
}

// TestProactiveCleanupCandidateSQLExcludesPartitions 钉住第一道闸（SQL 层）。
// 这里允许做文本断言 —— 因为它判的是「筛选条件在不在」这个**可枚举**的属性，
// 而上一条 TestProactiveCleanupNeverDropsPartition 负责判行为不被瘫掉。
func TestProactiveCleanupCandidateSQLExcludesPartitions(t *testing.T) {
	b, err := os.ReadFile(proactiveScriptPath(t))
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)

	seg := s
	if i := strings.Index(seg, "EMPTY_TABLES_SQL="); i >= 0 {
		rest := seg[i:]
		if j := strings.Index(rest, "\n\""); j >= 0 {
			seg = rest[:j]
		}
	}

	for _, needle := range []string{"NOT c.relispartition", "NOT LIKE '%_default'"} {
		if !strings.Contains(seg, needle) {
			t.Fatalf("候选 SQL 缺少 %q —— 分区可能被裸 DROP", needle)
		}
	}
}
