package analytics

// session_probe_scope_contract_test.go —— 契约门：Go 侧会话宇宙片段与迁移
// SQL（**984** 物化视图定义）必须逐字同构。迁移 SQL 是唯一真源；本测试从
// sql/migrations/startup/984_*.sql 里抽连接子句与 WHERE 合取项，与
// SessionUniverseJoin / SessionUniverseWhere 的产物做**归一化文本对拍**
// （剥空白差异，比较词序列）⇒ 任何一边改口径而另一边没跟 ⇒ 红。
//
// 为什么对拍而不是各自手写再「相信注释」：口径漂移事故的通用形态就是
// 「两份拷贝各改了一半」（AGENTS.md 2026-11-18 seq3067 §③ 同族案例）。
//
// ★ 为什么真源从 994 换成 984（2026-11-24 P1）：
//
//	994 重建了四个物化视图，**现行定义**是 984（会话宇宙谓词加「真实计费
//	豁免」：有真实计费的会话不因 owner 名字命中探测正则而被剔）。⇒ 对拍面
//	必须指向 984，否则这道门会拿 984 的**现行**定义去撞 Go 侧的新谓词。
//	★ 994 的文件**仍必须存在**（它已 applied，删它＝改已应用迁移）⇒ 另有一道
//	  「994 基线文件仍在」的存在性检查。
//
// ★ 本门**没有**被削弱成「宽松匹配」：判定仍是**归一化后逐字相等**，任何一
//   边改口径而另一边没跟 ⇒ 立刻红。

import (
	"os"
	"strings"
	"testing"
)

// sessionUniverseMigrationFile 是**会话宇宙谓词的现行真源**（984）。
const sessionUniverseMigrationFile = "../../sql/migrations/startup/984_session_universe_cost_aware_probe_scope.sql"

// sessionUniverseBaselineFile 是 994（历史基线，已 applied）。
const sessionUniverseBaselineFile = "../../sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql"

func readSessionUniverseMigration(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(sessionUniverseMigrationFile)
	if err != nil {
		t.Fatalf("read 984 migration: %v", err)
	}
	return string(b)
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestSessionUniverseJoinMatches984(t *testing.T) {
	mig := readSessionUniverseMigration(t)
	// 984 的连接子句（FROM session_summaries ss ... 段）。
	// 2026-11-23 D2 去重修复：改为 DISTINCT ON 子查询。
	wantJoin := normalize(`
JOIN (
  SELECT DISTINCT ON (session_id, tenant_id)
    session_id, tenant_id, is_probe, owner_user, client_type, title
  FROM sessions
  ORDER BY session_id, tenant_id, partition_date DESC
) s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id`)
	if !strings.Contains(normalize(mig), wantJoin) {
		t.Fatalf("984 migration no longer contains the canonical universe join; update both sides:\n%s", wantJoin)
	}
	got := normalize(SessionUniverseJoin("ss", "s", "sd"))
	if got != wantJoin {
		t.Fatalf("SessionUniverseJoin drifted from 984:\n got: %s\nwant: %s", got, wantJoin)
	}
}

func TestSessionUniverseWhereMatches984(t *testing.T) {
	mig := readSessionUniverseMigration(t)
	// ★ 2026-11-24 P1：谓词加「真实计费豁免」——
	//   有真实计费（ss.total_cost_usd > 0）⇒ 计入；
	//   零成本会话沿用旧排除语义（¬is_probe ∧ ¬session_analytics_is_probe）。
	wantWhere := normalize(`
WHERE NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') IS NOT NULL
  AND (
      COALESCE(ss.total_cost_usd, 0) > 0
      OR (
          NOT COALESCE(s.is_probe, false)
          AND NOT session_analytics_is_probe(
              COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)
      )
  )`)
	if !strings.Contains(normalize(mig), wantWhere) {
		t.Fatalf("984 migration no longer contains the canonical universe WHERE; update both sides:\n%s", wantWhere)
	}
	got := normalize("WHERE " + SessionUniverseWhere("s", "sd", "ss"))
	if got != wantWhere {
		t.Fatalf("SessionUniverseWhere drifted from 984:\n got: %s\nwant: %s", got, wantWhere)
	}
}

// TestSessionUniverse994BaselineStillExists —— 994（已 applied 的历史基线）
// 的文件不得消失：删它＝改已应用迁移的字节（L23 同款分叉）。
func TestSessionUniverse994BaselineStillExists(t *testing.T) {
	if _, err := os.Stat(sessionUniverseBaselineFile); err != nil {
		t.Fatalf("994 baseline migration 不在预期路径: %v", err)
	}
}

// TestSessionUniverseWhereKeepsCostEscape —— **判据不能退化成恒真**。
//
// 这道门防的是「把豁免写成恒真 ⇒ 探测会话全放行」这种退化：
// 断言生成的谓词**同时**含「真实计费豁免项」与「零成本会话的旧排除项」，
// 且两者都真的出现在文本里。
func TestSessionUniverseWhereKeepsCostEscape(t *testing.T) {
	where := SessionUniverseWhere("s", "sd", "ss")
	for _, frag := range []string{
		"COALESCE(ss.total_cost_usd, 0) > 0",
		"NOT COALESCE(s.is_probe, false)",
		"NOT session_analytics_is_probe(",
	} {
		if !strings.Contains(where, frag) {
			t.Errorf("会话宇宙谓词缺片段 %q（豁免项或旧排除项被抹掉 ⇒ 口径漂移）:\n%s", frag, where)
		}
	}
}

func TestShanghaiCalendarRangeUtcBucketParity(t *testing.T) {
	// 与 SQL 日桶逐值对拍：窗口界应使
	// (ts AT TIME ZONE 'Asia/Shanghai')::date ∈ [start, end] 的行全部落在
	// [startUtc, endUtc) 内。+08 固定偏移 ⇒ startUtc = 上海日 00:00 - 8h。
	startUtc, endUtc, err := ShanghaiCalendarRangeUtc("2026-11-15", "2026-11-17")
	if err != nil {
		t.Fatal(err)
	}
	if got := startUtc.Format("2006-01-02T15:04:05Z"); got != "2026-11-14T16:00:00Z" {
		t.Errorf("start bucket = %s, want 2026-11-14T16:00:00Z", got)
	}
	if got := endUtc.Format("2006-01-02T15:04:05Z"); got != "2026-11-17T16:00:00Z" {
		t.Errorf("end bucket = %s, want 2026-11-17T16:00:00Z", got)
	}
	// 逆窗口必须报错（与 settings.SystemDayRangeUtc 同语义）。
	if _, _, err := ShanghaiCalendarRangeUtc("2026-11-17", "2026-11-15"); err == nil {
		t.Fatal("reversed range must error")
	}
}
