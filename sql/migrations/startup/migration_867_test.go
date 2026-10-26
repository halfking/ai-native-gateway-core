package startup

import (
	"strings"
	"testing"
)

// 867 的判据核心：键序与在线会话超管查询的 ORDER BY 逐列同向，且 last_request_at
// 在前导位。tenant_time 正是「形状极像但不可用」的那条——若有人把租户列加回前导
// 或把方向改成 ASC，CREATE INDEX IF NOT EXISTS 不会报错，超管冷查询会静默回到
// 整扫 + top-N 排序。另一条钉子是「已建则不拿锁」：生产大表靠 CONCURRENTLY 预建，
// 本文件若裸跑 CREATE INDEX IF NOT EXISTS 仍会先拿 ShareLock 挡写。

const idx867File = "867_session_summaries_last_request_at_idx.sql"
const idx867Name = "idx_session_summaries_last_request_recent"

func stmt867(t *testing.T, name string) string {
	t.Helper()
	var kept []string
	for _, ln := range strings.Split(repoFile(t, "sql/migrations/startup/"+name), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "--") || strings.TrimSpace(ln) == "" {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func Test867IndexKeyMatchesOnlineSuperadminOrderBy(t *testing.T) {
	up := stmt867(t, idx867File)
	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS "+idx867Name) {
		t.Fatalf("867 缺目标索引名 —— 生产预建与幂等跳过都按**索引名**匹配")
	}
	if !strings.Contains(up, "ON public.session_summaries (last_request_at DESC, session_key DESC)") {
		t.Fatalf("867 的键必须是 (last_request_at DESC, session_key DESC)：与 admin/session_online.go 的 " +
			"ORDER BY 逐列同向才能按序取前 n 行即停；租户列前导即复刻 tenant_time 的整扫")
	}
}

func Test867KeepsNotNullPartialPredicate(t *testing.T) {
	up := stmt867(t, idx867File)
	if !strings.Contains(up, "WHERE last_request_at IS NOT NULL;") {
		t.Error("867 的部分谓词必须与消费方的 `last_request_at IS NOT NULL` 精确一致")
	}
}

func Test867SkipsCreateWhenIndexAlreadyExists(t *testing.T) {
	up := stmt867(t, idx867File)
	guard := "IF to_regclass('public." + idx867Name + "') IS NULL THEN"
	gi := strings.Index(up, guard)
	ci := strings.Index(up, "CREATE INDEX")
	if gi < 0 || ci < 0 || gi > ci {
		t.Fatalf("867 的 CREATE INDEX 必须包在 %q 判定内：PG 在发现同名索引前已拿表级 ShareLock，"+
			"生产预建环境裸跑 IF NOT EXISTS 仍会挡写", guard)
	}
	for i, ln := range strings.Split(up, "\n") {
		if strings.HasPrefix(ln, "CREATE INDEX") {
			t.Errorf("867 第 %d 行是顶层 CREATE INDEX（未进判定块）：%s", i+1, ln)
		}
	}
}

func Test867DoesNotUseConcurrently(t *testing.T) {
	if strings.Contains(strings.ToUpper(stmt867(t, idx867File)), "CONCURRENTLY") {
		t.Error("867 不能含 CONCURRENTLY —— 安装器 psql --single-transaction 下必然失败；生产走运维预建")
	}
}

func Test867SetsStatementAndLockTimeout(t *testing.T) {
	up := stmt867(t, idx867File)
	for _, want := range []string{"SET LOCAL statement_timeout", "SET LOCAL lock_timeout"} {
		if !strings.Contains(up, want) {
			t.Errorf("867 缺少 %s（共享 PG 纪律钉，862/864 同形先例）", want)
		}
	}
}

func Test867DownDropsByIndexName(t *testing.T) {
	down := stmt867(t, "867_session_summaries_last_request_at_idx.down.sql")
	if !strings.Contains(down, "DROP INDEX IF EXISTS public."+idx867Name) {
		t.Error("867.down 缺按名 DROP")
	}
}

func Test867CommentsAreGuardedLikeTheirCreateLegs(t *testing.T) {
	up := stmt867(t, idx867File)
	for i, ln := range strings.Split(up, "\n") {
		if strings.HasPrefix(ln, "COMMENT ON INDEX") {
			t.Errorf("867 第 %d 行是无守卫的顶层 COMMENT ON INDEX：%s（r1030：缺席环境 exit=3）", i+1, ln)
		}
	}
	if strings.Count(up, "to_regclass('public."+idx867Name+"')") < 2 {
		t.Error("867 的建索引与 COMMENT 两腿都必须先经 to_regclass 判定")
	}
}

func Test867EmbeddedCopyMatchesCanonical(t *testing.T) {
	canonical := repoFile(t, "sql/migrations/startup/"+idx867File)
	embed := repoFile(t, "installer/cmd/llm-gw-installer/embeddata/startup/"+idx867File)
	if embed != canonical {
		t.Errorf("867 embed 漂移：installer/cmd/llm-gw-installer/embeddata/startup/%s 与 canonical SQL 不一致", idx867File)
	}
}
