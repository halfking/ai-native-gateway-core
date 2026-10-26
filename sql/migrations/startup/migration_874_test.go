package startup

import (
	"strings"
	"testing"
)

// 874 的判据核心与 862 同一教义，但方向相反：862 的存在理由是「无谓词」，
// 874 的存在理由是「部分索引」。session_turns 上建全量 client_request_id
// 索引不会错，但会把构建/写放大抬高 ~9 倍（实测 2026_10 仅 10.9% 行带值），
// 而等值查询 client_request_id = $1 蕴含 NOT NULL，部分索引完全可用。
// 若哪天有人删掉 WHERE「简化」，已建好的索引不会重建，只是静默变贵。
//
// 反向风险更大的是**建到分区而不是父表**：新月分区自动继承父表索引
// （862/846 先例），逐分区建会漏未来分区，client_request_id 兜底腿会在
// 下一个月度分区出现时重新退化为顺序扫描 —— 且只在生产数据量下可感。
const idx874File = "874_session_turns_client_request_id_idx.sql"

func stmt874(t *testing.T, name string) string {
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

func Test874CreatesPartialClientRequestIdIndexOnParent(t *testing.T) {
	up := stmt874(t, idx874File)

	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS idx_session_turns_client_request_id") ||
		!strings.Contains(up, "ON public.session_turns (client_request_id)") {
		t.Fatalf("874 必须在分区父表 public.session_turns 上建 (client_request_id) 索引；" +
			"建在单个分区会漏未来月度分区（862/846 先例）")
	}
	for _, stmt := range strings.Split(up, ";") {
		s := strings.TrimSpace(stmt)
		if !strings.HasPrefix(s, "CREATE INDEX") {
			continue
		}
		if !strings.Contains(strings.ToUpper(s), "WHERE CLIENT_REQUEST_ID IS NOT NULL") {
			t.Errorf("874 的索引必须是部分索引（实测仅 ~10.9%% 行带值；删 WHERE 会让已建索引" +
				"静默变贵且不会自动重建）。违规语句：\n%s", s)
		}
	}
}

func Test874DoesNotUseConcurrently(t *testing.T) {
	up := stmt874(t, idx874File)
	if strings.Contains(strings.ToUpper(up), "CONCURRENTLY") {
		t.Error("874 不加 CONCURRENTLY —— 分区父表上被拒（842 实测），普通 CREATE INDEX 级联（846/862 先例）")
	}
}

func Test874SetsStatementTimeout(t *testing.T) {
	up := stmt874(t, idx874File)
	if !strings.Contains(up, "SET LOCAL statement_timeout") {
		t.Errorf("874 缺少 SET LOCAL statement_timeout：共享 PG 服务端 30s 会掐断级联索引构建，" +
			"dbinit/部署链只按退出码记账 ⇒ 永久迁移卡点（849/855/856/862 同形先例）")
	}
}

func Test874CommentIsGuarded(t *testing.T) {
	up := stmt874(t, idx874File)
	for i, ln := range strings.Split(up, "\n") {
		if strings.HasPrefix(ln, "COMMENT ON INDEX") {
			t.Errorf("874 第 %d 行是无守卫的顶层 COMMENT ON INDEX：%s\n"+
				"守卫掉的对象不能留无条件 COMMENT（r1030 实测 exit 非 0，迁移永不记账）", i+1, ln)
		}
	}
	if !strings.Contains(up, "to_regclass('public.idx_session_turns_client_request_id')") {
		t.Error("874 的 COMMENT 必须先经 to_regclass 判定")
	}
}

func Test874DownDropsIndex(t *testing.T) {
	down := stmt874(t, "874_session_turns_client_request_id_idx.down.sql")
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_session_turns_client_request_id") {
		t.Error("874.down 缺 idx_session_turns_client_request_id 的 DROP")
	}
}

// Test874EmbeddedCopyMatchesCanonical —— 双点同步钉（installer embed 与
// canonical 字节必须一致），849/850/851 漏同步那一族的通用护栏。
func Test874EmbeddedCopyMatchesCanonical(t *testing.T) {
	canonical := repoFile(t, "sql/migrations/startup/"+idx874File)
	embed := repoFile(t, "installer/cmd/llm-gw-installer/embeddata/startup/"+idx874File)
	if embed != canonical {
		t.Errorf("874 embed 漂移：installer/cmd/llm-gw-installer/embeddata/startup/%s 与 canonical SQL 不一致", idx874File)
	}
}
