package analytics

// session_probe_scope_contract_test.go —— 契约门：Go 侧会话宇宙片段与迁移
// SQL（981/994 物化视图定义）必须逐字同构。迁移 SQL 是唯一真源；本测试从
// sql/migrations/startup/994_*.sql 里抽连接子句与 WHERE 合取项，与
// SessionUniverseJoin / SessionUniverseWhere 的产物做**归一化文本对拍**
// （剥空白差异，比较词序列）⇒ 任何一边改口径而另一边没跟 ⇒ 红。
//
// 为什么对拍而不是各自手写再「相信注释」：口径漂移事故的通用形态就是
// 「两份拷贝各改了一半」（AGENTS.md 2026-11-18 seq3067 §③ 同族案例）。

import (
	"os"
	"strings"
	"testing"
)

func read994(t *testing.T) string {
	t.Helper()
	const candidates = "../../sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql"
	b, err := os.ReadFile(candidates)
	if err != nil {
		t.Fatalf("read 994 migration: %v", err)
	}
	return string(b)
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestSessionUniverseJoinMatches994(t *testing.T) {
	mig := read994(t)
	// 994 的连接子句（FROM session_summaries ss ... 段）。
	wantJoin := normalize(`
JOIN sessions s
  ON s.session_id = ss.session_key
 AND s.tenant_id = ss.tenant_id
LEFT JOIN session_dim sd
  ON sd.session_key = ss.session_key
 AND sd.tenant_id = ss.tenant_id`)
	if !strings.Contains(normalize(mig), wantJoin) {
		t.Fatalf("994 migration no longer contains the canonical universe join; update both sides:\n%s", wantJoin)
	}
	got := normalize(SessionUniverseJoin("ss", "s", "sd"))
	if got != wantJoin {
		t.Fatalf("SessionUniverseJoin drifted from 994:\n got: %s\nwant: %s", got, wantJoin)
	}
}

func TestSessionUniverseWhereMatches994(t *testing.T) {
	mig := read994(t)
	wantWhere := normalize(`
WHERE NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') IS NOT NULL
  AND NOT COALESCE(s.is_probe, false)
  AND NOT session_analytics_is_probe(
      COALESCE(s.owner_user, sd.api_key_owner_user), s.client_type, s.title)`)
	if !strings.Contains(normalize(mig), wantWhere) {
		t.Fatalf("994 migration no longer contains the canonical universe WHERE; update both sides:\n%s", wantWhere)
	}
	got := normalize("WHERE " + SessionUniverseWhere("s", "sd"))
	if got != wantWhere {
		t.Fatalf("SessionUniverseWhere drifted from 994:\n got: %s\nwant: %s", got, wantWhere)
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
