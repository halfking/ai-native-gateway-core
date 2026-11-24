package bg

// partition_analyze_settings_test.go —— 会话族月分区 autovacuum analyze 设置巡检的门。
//
// 背景（211 生产实证，2026-11-25）：session_turns / session_turn_details 的月分区
// 从未被纳入任何 autovacuum analyze 设置 ⇒ 统计信息陈旧 45 小时 ⇒ 日志列表的
// 冷分区被估成 3 行 ⇒ details 连接退化成 1 亿次 Join Filter 比较、单条语句 26~38s。
//
// 钉五件事：
//
//	① DDL 文本正确，relname 经标识符转义（它是从 catalog 读回来再拼进 DDL 的）；
//	② scale factor 白名单：候选 SQL 之外的值不执行；
//	③ 候选筛选带 reloptions 守卫且与 ALTER 同口径（当月 0.005 / 其余 0.02）；
//	④ 接线：每趟 ensure 之后调度巡检（启动一趟 + 每个 tick 一趟）；
//	⑤ 真库幂等：跑一趟后全部分区带齐设置，再跑一趟 Applied=0 / Skipped=0。

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionFamilyAnalyzeAlterSQL(t *testing.T) {
	got := sessionFamilyAnalyzeAlterSQL("session_turns_2026_11", "0.005")
	want := "ALTER TABLE public.\"session_turns_2026_11\" SET (autovacuum_enabled=true, " +
		"autovacuum_vacuum_scale_factor=0.05, autovacuum_vacuum_threshold=10, " +
		"autovacuum_analyze_threshold=50, autovacuum_analyze_scale_factor=0.005)"
	if got != want {
		t.Fatalf("ALTER 文本不符：\n got %s\nwant %s", got, want)
	}
	// relname 带引号 / 分号时必须被当作一个标识符整体转义，不得拼出第二条语句。
	hostile := sessionFamilyAnalyzeAlterSQL(`x"; DROP TABLE public.session_turns; --`, "0.02")
	if !strings.Contains(hostile, `public."x""; DROP TABLE public.session_turns; --" SET (`) {
		t.Fatalf("恶意 relname 未被整体转义：%s", hostile)
	}
	if strings.Count(hostile, "ALTER TABLE") != 1 {
		t.Fatalf("不得拼出第二条 DDL：%s", hostile)
	}
}

func TestSessionFamilyAnalyzeParentsAreTheLogsSources(t *testing.T) {
	// 只巡检这两张表的月分区：它们是日志列表按时间窗读的冷数据，其余表没有实证。
	if len(sessionFamilyAnalyzeParents) != 2 ||
		sessionFamilyAnalyzeParents[0] != "session_turns" ||
		sessionFamilyAnalyzeParents[1] != "session_turn_details" {
		t.Fatalf("sessionFamilyAnalyzeParents = %v", sessionFamilyAnalyzeParents)
	}
}

func TestIsLockTimeoutError(t *testing.T) {
	if !isLockTimeoutError(&pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}) {
		t.Error("55P03 必须被识别为 lock_timeout")
	}
	for _, err := range []error{
		nil,
		errors.New("canceling statement due to statement timeout"),
		&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
		&pgconn.PgError{Code: "42P01", Message: "relation does not exist"},
	} {
		if isLockTimeoutError(err) {
			t.Errorf("%v 不应被识别为 lock_timeout", err)
		}
	}
}

func TestSessionFamilyAnalyzeCandidatesSQL_GuardMatchesAlter(t *testing.T) {
	sql := sessionFamilyAnalyzeCandidatesSQL
	// 守卫：reloptions 已含全部五项（含按月定的 scale factor）的分区不进候选
	// ⇒ 不会被 ALTER（也就不会被取 ACCESS EXCLUSIVE 锁）。
	for _, opt := range []string{
		"'autovacuum_enabled=true'",
		"'autovacuum_vacuum_scale_factor=0.05'",
		"'autovacuum_vacuum_threshold=10'",
		"'autovacuum_analyze_threshold=50'",
		"'autovacuum_analyze_scale_factor=' ||",
		"COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[",
		"p.relname = ANY($1::text[])",
	} {
		if !strings.Contains(sql, opt) {
			t.Errorf("候选 SQL 缺少 %q", opt)
		}
	}
	// 当月 / 其余月份两个取值都要出现在候选 SQL 里，且月判定在「选择列」与
	// 「守卫」两处各一次（只改一头 = 月初被改回去 / 永远配不上）。
	if strings.Count(sql, sessionFamilyExpectedSFExpr) != 2 {
		t.Errorf("月判定的 scale factor 表达式必须同时出现在选择列与守卫里，得到 %d 处", strings.Count(sql, sessionFamilyExpectedSFExpr))
	}
	if !strings.Contains(sessionFamilyExpectedSFExpr, "THEN '"+sessionFamilyAnalyzeScaleCurrentMonth+"'") ||
		!strings.Contains(sessionFamilyExpectedSFExpr, "ELSE '"+sessionFamilyAnalyzeScaleOtherMonths+"'") {
		t.Errorf("scale factor 口径应为当月 %s / 其余 %s", sessionFamilyAnalyzeScaleCurrentMonth, sessionFamilyAnalyzeScaleOtherMonths)
	}
	// ALTER 侧写的是同一组选项（不含 scale factor 的四项 + 单独一项 scale）。
	for _, opt := range []string{"autovacuum_enabled=true", "autovacuum_vacuum_scale_factor=0.05", "autovacuum_vacuum_threshold=10", "autovacuum_analyze_threshold=50"} {
		if !strings.Contains(sessionFamilyAnalyzeOptsBase, opt) || !strings.Contains(sql, "'"+opt+"'") {
			t.Errorf("选项 %q 必须在守卫与 ALTER 两侧同口径出现", opt)
		}
	}
	// 复查 SQL（持锁后再判一次）与候选守卫同口径。
	if !strings.Contains(sessionFamilyAnalyzeConfiguredSQL, sessionFamilyExpectedSFExpr) ||
		!strings.Contains(sessionFamilyAnalyzeConfiguredSQL, "@> ARRAY[") {
		t.Error("持锁后的复查必须用与候选守卫相同的 reloptions 包含判定")
	}
}

func TestPartitionManager_SchedulesAnalyzeSettingsAfterEveryEnsure(t *testing.T) {
	raw, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	src := string(raw)
	// 启动趟 + tick 趟：每处 ensureNextMonthPartitions 之后都要调度巡检。
	ensure := strings.Count(src, "pm.ensureNextMonthPartitions(ctx)")
	sched := strings.Count(src, "pm.scheduleSessionFamilyAnalyzeSettings(ctx)")
	if ensure < 2 || sched != ensure {
		t.Fatalf("ensureNextMonthPartitions 出现 %d 处、巡检调度 %d 处——每一趟 ensure 之后都必须调度", ensure, sched)
	}
	// 每一处 ensure 之后紧跟调度（缩进随所在分支不同 ⇒ 不按字面空白比对）。
	after := regexp.MustCompile(`pm\.ensureNextMonthPartitions\(ctx\)\s+pm\.scheduleSessionFamilyAnalyzeSettings\(ctx\)`).
		FindAllString(src, -1)
	if len(after) != sched {
		t.Fatalf("%d 处调度里只有 %d 处紧跟在 ensureNextMonthPartitions 之后（新月份分区要在被预建出来的同一趟拿到设置）", sched, len(after))
	}
	// 巡检在独立 goroutine 里跑：它要等 lock_timeout，内联会推迟 close(pm.ready)。
	sched2, err := os.ReadFile("partition_analyze_settings.go")
	if err != nil {
		t.Fatalf("read partition_analyze_settings.go: %v", err)
	}
	if !strings.Contains(string(sched2), `Go("partition_manager.sessionFamilyAnalyzeSettings", func() {`) {
		t.Error("巡检必须在独立 goroutine 里调度")
	}
	// run() 里只许出现调度（schedule…），不许内联调用巡检本体。
	if strings.Count(string(raw), "pm.ensureSessionFamilyAnalyzeSettings(") != 0 {
		t.Error("run() 不得内联调用 ensureSessionFamilyAnalyzeSettings（会推迟 ready）")
	}
}

// analyzeSettingsRealDBPool 取真库连接；三个环境变量名任一即可（admin/db 包用
// TEST_PG_URL，bg 包既有测试用 TEST_DATABASE_URL / TEST_DB_URL）。
func analyzeSettingsRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		dsn = os.Getenv("TEST_PG_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL / TEST_PG_URL 未设置 —— 跳过不构成证据：巡检的幂等性只在真库上成立")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// sessionFamilyPartitionsNeedingSettings 数出「reloptions 还不完整」的会话族分区。
func sessionFamilyPartitionsNeedingSettings(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM ("+sessionFamilyAnalyzeCandidatesSQL+") c",
		sessionFamilyAnalyzeParents).Scan(&n); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	return n
}

func TestEnsureSessionFamilyAnalyzeSettings_IdempotentOnRealDB(t *testing.T) {
	pool := analyzeSettingsRealDBPool(t)
	pm := &PartitionManager{db: pool}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var partitions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND i.inhparent IN ('public.session_turns'::regclass, 'public.session_turn_details'::regclass)
		  AND c.relkind = 'r'`).Scan(&partitions); err != nil {
		t.Skipf("会话族分区表不存在（迁移未应用？err=%v）", err)
	}
	if partitions == 0 {
		t.Skip("没有会话族月分区——空集不构成证据")
	}

	outcome := pm.ensureSessionFamilyAnalyzeSettings(ctx)
	if n := sessionFamilyPartitionsNeedingSettings(t, pool); n != 0 {
		t.Fatalf("跑完一趟后仍有 %d 个分区缺设置（本趟 applied=%d skipped=%d）——若表忙被跳过，重跑一趟应能补齐", n, outcome.Applied, outcome.Skipped)
	}
	if outcome.Applied+outcome.Skipped == 0 && partitions > 0 {
		// 第一趟之前就已经全配好（例如别的测试/实例先跑过）⇒ 这一趟无事可做是合法的；
		// 幂等性由下面的第二趟证明。
		t.Logf("首趟之前已全部配置（applied=%d）", outcome.Applied)
	}

	// 第二趟：候选为空 ⇒ 不 ALTER、不等锁。
	again := pm.ensureSessionFamilyAnalyzeSettings(ctx)
	if again.Applied != 0 || again.Skipped != 0 {
		t.Fatalf("第二趟必须无事可做，得到 applied=%d skipped=%d", again.Applied, again.Skipped)
	}

	// 逐分区核对：设置齐全，scale factor 按是否当月分区。
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	rows, err := pool.Query(ctx2, `SELECT c.relname, c.reloptions, `+sessionFamilyExpectedSFExpr+` AS expect_sf
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND i.inhparent IN ('public.session_turns'::regclass, 'public.session_turn_details'::regclass)
		  AND c.relkind = 'r' ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var relname, expectSF string
		var opts []string
		if err := rows.Scan(&relname, &opts, &expectSF); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, want := range []string{
			"autovacuum_enabled=true", "autovacuum_vacuum_scale_factor=0.05", "autovacuum_vacuum_threshold=10",
			"autovacuum_analyze_threshold=50", "autovacuum_analyze_scale_factor=" + expectSF,
		} {
			found := false
			for _, o := range opts {
				if o == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s 缺少 reloption %q（现有 %v）", relname, want, opts)
			}
		}
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if checked != partitions {
		t.Fatalf("核对数 %d != 分区数 %d", checked, partitions)
	}
}
