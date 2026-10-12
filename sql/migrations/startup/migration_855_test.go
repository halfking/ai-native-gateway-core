package startup

// migration_855_test.go — 855 的契约门。
//
// 855 干的事：把 session-analytics 三兄弟改成 30 天时间窗。实测收益来自
// **输出组数 104,299 → 1,832（-98.2%）**，而 CPU 只省 8%
// （runbook §10.106.39）。所以门要钉的不是「建了视图」——视图本来就有，
// 855 是 DROP + CREATE 重建——而是**重建出来的形状对不对**。
//
// 三条最容易在重建里丢、且丢了会静默或晚爆的东西：
//  ① 唯一索引：REFRESH CONCURRENTLY 没有唯一索引会**整轮失败**，而失败在
//     刷新器里表现为每轮一条 error 日志 + MV 数据停在旧值。
//  ② refreshed_at 列：admin 侧 4 个读点按名读它，删掉会让新鲜度门永久读成
//     「stale」并静默回落到基表查询。
//  ③ 时间窗本身：三个视图漏改一个，那个视图照旧每轮重写几万行。

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const m855Base = "855_session_analytics_time_window"
const m855 = m855Base + ".sql"
const m855Down = m855Base + ".down.sql"

// 855 必须覆盖的三个视图。与 bg/materialized_view_refresher.go 的
// SessionAnalyticsViews 是同一份清单；少改一个，那个视图的优化就不生效。
var m855Views = []string{
	"session_client_stats",
	"session_task_stats",
	"session_client_task_matrix",
}

func read855(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// strip855SQLComments drops whole-line -- comments so contracts assert on
// statements rather than prose. Trailing inline comments are kept, matching
// the conservative rule used by 842/843.
func strip855SQLComments(src string) string {
	var kept []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// m855CreateBlock returns the text of one `CREATE MATERIALIZED VIEW <view> AS
// ...;` statement. Scoping every assertion to the right view matters: a gate
// that searches the whole file passes when the WHERE exists on *some* view,
// which is exactly the "one of three got missed" case.
func m855CreateBlock(t *testing.T, body, view string) string {
	t.Helper()
	re := regexp.MustCompile(`(?is)CREATE\s+MATERIALIZED\s+VIEW\s+` +
		regexp.QuoteMeta(view) + `\s+AS\b.*?;`)
	m := re.FindString(body)
	require.NotEmptyf(t, m, "找不到 %s 的 CREATE MATERIALIZED VIEW 语句", view)
	return m
}

// m855SourceTail returns the part of a CREATE block from "FROM session_summaries"
// onwards — i.e. the WHERE/GROUP BY tail, with the SELECT list (where
// active_sessions_24h lives inside `FILTER (WHERE ...)`) excluded.
//
// Without this scoping every "is there a WHERE on last_request_at" assertion
// matches the 24-hour FILTER instead of the window predicate: same column,
// same operator, completely different meaning.
func m855SourceTail(t *testing.T, block string) string {
	t.Helper()
	at := regexp.MustCompile(`(?i)\bFROM\s+session_summaries\b`).FindStringIndex(block)
	require.NotNilf(t, at, "找不到 FROM session_summaries：\n%s", block)
	return block[at[0]:]
}

// TestMigration855_EveryViewGetsATimeWindow pins the defect itself: a view
// without the WHERE keeps rewriting its full historical group set every cycle.
func TestMigration855_EveryViewGetsATimeWindow(t *testing.T) {
	body := strip855SQLComments(read855(t, m855))
	for _, v := range m855Views {
		tail := m855SourceTail(t, m855CreateBlock(t, body, v))
		re := regexp.MustCompile(`(?is)WHERE\s+(?:\w+\.)?last_request_at\s*>=\s*` +
			`NOW\(\)\s*-\s*INTERVAL\s*'\s*30\s*days\s*'\s*GROUP\s+BY`)
		require.Regexpf(t, re, tail,
			"%s 重建后必须带 30 天时间窗 —— 漏掉它，这个视图就照旧每轮重写全部历史组", v)
	}
}

// TestMigration855_WindowUsesLastRequestAtNotCreatedAt: the window is "the
// session was active in the last 30 days", not "it was created in the last 30
// days". A session created in March and still serving requests in April
// belongs in the window; keying on created_at silently drops it, and no shape
// assertion about "there is a WHERE clause" would notice.
func TestMigration855_WindowUsesLastRequestAtNotCreatedAt(t *testing.T) {
	body := strip855SQLComments(read855(t, m855))
	// 必假式（否定判据最容易恒真），所以每条断言都配一条**同形状的阳性对照**：
	// 同样的正则去匹 last_request_at，必须命中——否则这条「不该匹配」
	// 可能只是因为正则根本匹配不到任何东西。
	windowRe := regexp.MustCompile(`(?is)WHERE\s+(?:\w+\.)?last_request_at\s*>=.*?GROUP\s+BY`)
	createdRe := regexp.MustCompile(`(?is)WHERE\s+(?:\w+\.)?created_at\s*>=`)

	for _, v := range m855Views {
		tail := m855SourceTail(t, m855CreateBlock(t, body, v))
		require.Regexpf(t, windowRe, tail,
			"阳性对照失败：%s 的 last_request_at 窗口谓词没被匹配到，下面的否定断言没有意义", v)
		require.NotRegexpf(t, createdRe, tail,
			"%s 不该按 created_at 切窗：三月创建、四月仍在用的会话会被误排除", v)
	}
}

// TestMigration855_KeepsRefreshedAtColumn: admin reads this column by name at
// four sites (admin/session_analytics_clients.go, admin/session_analytics_tasks.go).
// Dropping it while tidying the views would leave the freshness gate reading
// "stale" forever and silently degrade to base-table queries.
func TestMigration855_KeepsRefreshedAtColumn(t *testing.T) {
	body := strip855SQLComments(read855(t, m855))
	for _, v := range m855Views {
		block := m855CreateBlock(t, body, v)
		require.Regexpf(t, `(?i)NOW\(\)\s+as\s+refreshed_at`,
			block, "%s 必须保留 refreshed_at 列：admin 侧 4 个读点按名读它", v)
	}
}

// TestMigration855_UniqueIndexesSurviveTheRebuild is the highest-consequence
// assertion here. REFRESH MATERIALIZED VIEW CONCURRENTLY *requires* a unique
// index over all rows; drop one during the rebuild and every refresh of that
// view fails, leaving the MV frozen at whatever snapshot it had.
func TestMigration855_UniqueIndexesSurviveTheRebuild(t *testing.T) {
	body := strip855SQLComments(read855(t, m855))
	for _, idx := range []string{
		"idx_session_client_stats_tenant_client",
		"idx_session_task_stats_tenant_task",
		"idx_session_client_task_matrix_uq",
	} {
		re := regexp.MustCompile(`(?i)CREATE\s+UNIQUE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` +
			regexp.QuoteMeta(idx) + `\b`)
		require.Regexpf(t, re, body,
			"%s 必须重建：CONCURRENTLY 刷新缺唯一索引会整轮失败，MV 会冻在旧快照", idx)
	}
}

// TestMigration855_DropsEachViewBeforeRecreating: without the DROP the CREATE
// fails on an existing relation, so the migration is a no-op error rather than
// a window change.
func TestMigration855_DropsEachViewBeforeRecreating(t *testing.T) {
	body := strip855SQLComments(read855(t, m855))
	for _, v := range m855Views {
		dropAt := strings.Index(body, "DROP MATERIALIZED VIEW IF EXISTS "+v)
		createAt := strings.Index(body, "CREATE MATERIALIZED VIEW "+v)
		require.NotEqualf(t, -1, dropAt, "缺少对 %s 的 DROP", v)
		require.NotEqualf(t, -1, createAt, "缺少对 %s 的 CREATE", v)
		require.Lessf(t, dropAt, createAt,
			"%s 必须先 DROP 再 CREATE，否则 CREATE 会撞上已存在的同名视图", v)
	}
}

// TestMigration855_DownRestoresTheFullHistoryWindow: the down migration has to
// actually undo the window. A copy-paste of the up file would "roll back"
// nothing while looking like a rollback — and the size blow-up (182 MB and
// ~169 MB WAL per cycle) would come back with no signal.
func TestMigration855_DownRestoresTheFullHistoryWindow(t *testing.T) {
	down := strip855SQLComments(read855(t, m855Down))
	require.NotContainsf(t, down, "INTERVAL '30 days'",
		"down 迁移里仍带 30 天窗口 —— 它没有真的回退")

	// Every view's SELECT in the down file must not filter on the window
	// column at all. Scope per view so "the WHERE was removed from two of
	// three" cannot pass.
	for _, v := range m855Views {
		tail := m855SourceTail(t, m855CreateBlock(t, down, v))
		require.NotRegexpf(t, `(?is)WHERE\s+(?:\w+\.)?last_request_at\s*>=`,
			tail, "down 的 %s 仍在切窗：回滚应当恢复全历史口径", v)
	}
}

// TestMigration855_DownRebuildsTheSameColumns: a rollback that drops or renames
// a column is a schema change wearing a rollback's name — the admin queries
// read these by name.
func TestMigration855_DownRebuildsTheSameColumns(t *testing.T) {
	up := strip855SQLComments(read855(t, m855))
	down := strip855SQLComments(read855(t, m855Down))
	for _, v := range m855Views {
		upBlock, downBlock := m855CreateBlock(t, up, v), m855CreateBlock(t, down, v)
		aliasRe := regexp.MustCompile(`(?i)as\s+(\w+)\s*(?:,|$)`)
		upCols := map[string]bool{}
		for _, m := range aliasRe.FindAllStringSubmatch(upBlock, -1) {
			upCols[strings.ToLower(m[1])] = true
		}
		for _, m := range aliasRe.FindAllStringSubmatch(downBlock, -1) {
			col := strings.ToLower(m[1])
			require.Truef(t, upCols[col],
				"down 的 %s 多了 up 没有的输出列 %q —— 回滚不该改变 schema", v, col)
		}
		// refreshed_at must be there in both.
		require.Regexpf(t, `(?i)NOW\(\)\s+as\s+refreshed_at`, downBlock,
			"down 的 %s 必须同样保留 refreshed_at 列", v)
	}
}
