//go:build !integration

package v2

// session_family_p0_realdb_test.go —— 会话族 P0 轮（G-4 / G-5 / G-7）的**真库腿**。
//
// # 这道门在补哪个缺口
//
// 2026-10-20 会话治理 P0 轮给会话族补了三件事：
//
//	· G-4（迁移 885）：session_turns / session_turns_hot 加 host + tags
//	· G-5（迁移 887）：search_text 的 GIN 全文检索腿（to_tsvector('simple', search_text)）
//	· G-7（迁移 888）：session_bodies 的 request_id 连接索引 + sessions 的分区日期索引
//
// 审计方案 §9-1 记的是**真话**：这三件事的验收判据**全是 pgxmock / 静态形状级**
// ——「写方绑了非空值」「迁移里写了这两列」「查询谓词引用了这些列」。
// 这三条**都不等于**「真库上真的成立」。
//
// 具体的漏法有三条，每条都曾真实发生过：
//
//	(a) **参数个数**。写方的 INSERT 是 101 个位置参数（$1..$101）。少一个或多一个
//	    $N，PostgreSQL 报 "bind message supplies N parameters, but prepared
//	    statement requires M" —— **只在真的执行时**才发生。pgxmock 的桩只要参数
//	    个数对就能过。审计 §9.208 记过同族事故：列加进清单、实参也加了，**SELECT
//	    段里忘了加 $99**。
//	(b) **列集合契约**。迁移 707 给 promote_session_turns_hot_to_partition 加了
//	    运行时的父表/hot 列形状比对，两侧不同形就 RAISE EXCEPTION
//	    'session_turns hot/parent column contract has drifted' ⇒ **热表永远排不进
//	    月分区**（表现为「hot 表越滚越大」）。885 加了两列 —— 静态门比对的是两段
//	    ALTER TABLE 的**文本**，**不是**那个运行时比对。
//	(c) **索引真的建出来了、真的被这条查询用上**。迁移 888 用 format() 生成器
//	    逐分区建索引 —— 静态门解析的是那条 format() 串，**不是** catalog 里真实
//	    存在的索引。
//
// # 三条纪律（沿用 AGENTS.md 判据纪律）
//
// 1. **前提自证**：每个用例先自证前提（列在不在、索引在不在、函数在不在）。
//    前提不成立 ⇒ **t.Fatalf**，不是 t.Skip、不是「安静地通过」。
//    「没被检查」和「检查通过」必须长得不一样。
// 2. **计划断言的诚实边界**：本门用 `SET LOCAL enable_seqscan = off` 来断言
//    「该查询**能**用上该索引」。它**不**断言「planner 在生产数据量下**会**选它」
//    —— 几百行的测试表上 planner 本来就会选顺序扫描，那是表的规模决定的，不是
//    索引的毛病。生产规模的偏好是**另一个主张**，本门不冒充它。
// 3. **阴性对照**：凡是有「允许侧」的用例，都配一条「前提仍在场」的断言。
//    只测「显式值被原样落库」会漏掉「写方把值写死成常量」；所以额外测「不显式
//    给 host 时是否仍有可用的兜底身份」。
//
// # 怎么跑
//
// 需要一个**已迁移**的 PG 库（父表 + hot 表 + 逐分区索引都在）。仓内脚本：
//
//	bash scripts/audit/verify-session-family-p0-realdb.sh
//
// 它会从本机 llm-gateway-pg 容器**克隆**一份生产形态的库到一次性库
// （itgate_*，退出即删），跑本文件，然后删库 —— **不碰共享库**。
//
// 也可手工跑：
//
//	TEST_DATABASE_URL='postgres://llm_gateway@127.0.0.1:5432/itgate_x' \
//	  go test ./domains/session/v2/ -run 'RealDB' -v
//
// 没设 TEST_DATABASE_URL ⇒ 全部 skip（本文件是 `!integration` 构建标签的
// **普通**测试文件，不是 integration 专属的 —— 判据要能进常规 CI 形态）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// realDBPool opens the disposable database the P0 real-DB legs run against.
//
// The pool is opened with a bounded connect timeout: an unreachable DSN must
// fail fast with a diagnosable message, not hang the suite.
func realDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database session-family P0 proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("TEST_DATABASE_URL unreachable (%v) —— 这不是「跳过」的理由，"+
			"是环境坏了：请修好或显式不设该变量", err)
	}
	return pool
}

// ---------------------------------------------------------------------------
// G-4 —— 迁移 885 的 host / tags：真库写路径
// ---------------------------------------------------------------------------

// TestTurnWriterHostTagsRealDB proves the 885 columns are **written** by the
// real writer and **read back** from a real table.
//
// What it proves:
//   - the INSERT's 101 positional parameters line up with the real column set
//     (a mismatch aborts here with "bind message supplies N parameters"),
//   - an explicit host/tags land verbatim,
//   - a turn written **without** an explicit host still gets a non-empty host
//     (process-identity fallback) and a non-empty tag array — so the column
//     is derived, not a constant passthrough.
//
// What it does NOT prove: anything about the read side of the admin UI.
func TestTurnWriterHostTagsRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	// 前提自证：两侧都要有这两列。缺列时下面每一步都会以「读回空」通过，
	// 那是一条恒真的门 —— 所以这里直接 Fatal。
	for _, table := range []string{"session_turns", "session_turns_hot"} {
		var hostType, tagsType string
		err := pool.QueryRow(ctx, `
			SELECT
			  COALESCE((SELECT data_type FROM information_schema.columns
			             WHERE table_name = $1 AND column_name = 'host'), ''),
			  COALESCE((SELECT data_type FROM information_schema.columns
			             WHERE table_name = $1 AND column_name = 'tags'), '')`,
			table).Scan(&hostType, &tagsType)
		if err != nil {
			t.Fatalf("probe columns on %s: %v", table, err)
		}
		if hostType != "text" {
			t.Fatalf("%s.host 不存在或类型不是 text（实得 %q）—— 本门前提不成立。"+
				"它会在缺列时恒绿。**不要**把恒绿当通过。", table, hostType)
		}
		if tagsType != "ARRAY" {
			t.Fatalf("%s.tags 不存在或类型不是 ARRAY（实得 %q）—— 本门前提不成立。", table, tagsType)
		}
	}

	stamp := time.Now().UTC().Format("20060102150405.000000000")
	stamp = strings.ReplaceAll(stamp, ".", "")
	explicitID := "probe-hosttags-explicit-" + stamp
	fallbackID := "probe-hosttags-fallback-" + stamp

	defer func() {
		for _, id := range []string{explicitID, fallbackID} {
			for _, tbl := range []string{"public.session_turns", "public.session_turns_hot"} {
				if _, err := pool.Exec(ctx, `DELETE FROM `+tbl+` WHERE request_id = $1`, id); err != nil {
					t.Errorf("cleanup %s from %s: %v", id, tbl, err)
				}
			}
		}
		var leftover int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM (
			  SELECT request_id FROM session_turns_hot WHERE request_id LIKE 'probe-hosttags-%'
			  UNION ALL SELECT request_id FROM session_turns  WHERE request_id LIKE 'probe-hosttags-%'
			) x`).Scan(&leftover); err != nil {
			t.Errorf("post-cleanup probe: %v", err)
			return
		}
		if leftover != 0 {
			t.Errorf("本门在库里留了 %d 行探针数据（request_id LIKE 'probe-hosttags-%%'）", leftover)
		}
	}()

	w := newTurnWriter(pool)

	// 阳性：显式值必须原样落库。
	const wantHost = "gw-probe-host-001"
	wantTags := []string{"alpha", "beta"}
	if _, err := w.AppendTurn(ctx, TurnRecord{
		SessionID:      "probe-hosttags-session",
		TenantID:       "probe-hosttags-tenant",
		RequestID:      explicitID,
		Ts:             time.Now().UTC().Truncate(time.Microsecond),
		SourceKind:     "live",
		ClientProtocol: "openai-chat",
		AgentType:      "assistant",
		TaskType:       "chat",
		Host:           wantHost,
		Tags:           wantTags,
		DigestJSON:     []byte(`{}`),
	}); err != nil {
		t.Fatalf("AppendTurn (explicit case): %v —— 若报 bind message supplies N parameters，"+
			"则是列清单 / $N / 实参三处没对齐", err)
	}

	var gotHost *string
	var gotTags []string
	if err := pool.QueryRow(ctx,
		`SELECT host, tags FROM session_turns_hot WHERE request_id = $1`, explicitID).
		Scan(&gotHost, &gotTags); err != nil {
		t.Fatalf("read back (explicit case): %v", err)
	}
	if gotHost == nil || *gotHost != wantHost {
		t.Fatalf("session_turns_hot.host = %v, want %q —— 列没被写（或是写死的常量）", gotHost, wantHost)
	}
	if len(gotTags) != len(wantTags) {
		t.Fatalf("session_turns_hot.tags = %v, want %v —— 数组长度不符", gotTags, wantTags)
	}
	for i := range wantTags {
		if gotTags[i] != wantTags[i] {
			t.Fatalf("session_turns_hot.tags = %v, want %v —— 逐项不符", gotTags, wantTags)
		}
	}

	// 阴性对照：不显式给 host/tags 时，落库值**仍必须可用**。
	//   · host 走进程身份兜底（永不空），
	//   · tags 走轮次自身分类列派生（永不为空数组，且带命名空间标记）。
	// 这条钉的是「派生」而不是「透传」—— 只测阳性用例时，
	// 「写方把 host 写死成常量」这条变异照样全绿。
	if _, err := w.AppendTurn(ctx, TurnRecord{
		SessionID:      "probe-hosttags-session",
		TenantID:       "probe-hosttags-tenant",
		RequestID:      fallbackID,
		Ts:             time.Now().UTC().Truncate(time.Microsecond),
		SourceKind:     "live",
		ClientProtocol: "anthropic-messages",
		DigestJSON:     []byte(`{}`),
	}); err != nil {
		t.Fatalf("AppendTurn (fallback case): %v", err)
	}
	var fbHost *string
	var fbTags []string
	if err := pool.QueryRow(ctx,
		`SELECT host, tags FROM session_turns_hot WHERE request_id = $1`, fallbackID).
		Scan(&fbHost, &fbTags); err != nil {
		t.Fatalf("read back (fallback case): %v", err)
	}
	if fbHost == nil || strings.TrimSpace(*fbHost) == "" {
		t.Fatalf("未显式给 host 时落库为 %v —— 兜底身份失效，"+
			"「哪台机器写的这轮」又无处可查了", fbHost)
	}
	if len(fbTags) == 0 {
		t.Fatalf("未显式给 tags 时落库为空数组 —— 「没有标签」与「标签没写」" +
			"必须长得不一样")
	}
	if fbTags[0] != TurnTagNamespace {
		t.Fatalf("派生 tags[0] 必须是命名空间标记 %q，实得 %v", TurnTagNamespace, fbTags)
	}
	if !containsTag(fbTags, "proto:anthropic-messages") {
		t.Fatalf("派生 tags 必须含 client_protocol 标签，实得 %v", fbTags)
	}
	t.Logf("写侧成立：显式 host=%q tags=%v；兜底 host=%q tags=%v",
		wantHost, gotTags, *fbHost, fbTags)
}

// ---------------------------------------------------------------------------
// G-4 的下游 —— 迁移 707 的 promote 列集合契约
// ---------------------------------------------------------------------------

// TestPromoteColumnContractRealDB proves that adding host/tags (885) did NOT
// break the promote path.
//
// Why this leg exists: migration 707 puts a **runtime** column-shape check in
// promote_session_turns_hot_to_partition — parent and hot must have identical
// (name:type:notnull) sets, otherwise
//
//	RAISE EXCEPTION 'session_turns hot/parent column contract has drifted'
//
// ⇒ hot rows can never be promoted into the monthly partitions ⇒ the hot
// table grows forever. The static gate for 885 compares the **text** of two
// ALTER TABLE statements; it cannot see the runtime check.
//
// How it is made safe: the call runs inside a transaction that is rolled
// back, and the retention window is 100 years so no row can qualify for
// promotion even if the rollback were removed.
func TestPromoteColumnContractRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	// 前提自证：promote 函数必须在库里（它是 707 建的）。
	var fnExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		   WHERE n.nspname = 'public'
		     AND p.proname = 'promote_session_turns_hot_to_partition')`).
		Scan(&fnExists); err != nil {
		t.Fatalf("probe promote function: %v", err)
	}
	if !fnExists {
		t.Skip("库里没有 promote_session_turns_hot_to_partition（未迁移的库）—— " +
			"本门的前提不成立，跳过而不是假装通过")
	}

	// 独立复算一次契约（用与 707/869 函数体逐字同款的形状串），
	// 让失败时的信息能直接指出「哪一侧多了/少了哪一列」。
	var parentShape, hotShape string
	shapeSQL := `
		SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
		                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
		  FROM pg_attribute
		 WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped`
	if err := pool.QueryRow(ctx, shapeSQL, "public.session_turns").Scan(&parentShape); err != nil {
		t.Fatalf("read parent shape: %v", err)
	}
	if err := pool.QueryRow(ctx, shapeSQL, "public.session_turns_hot").Scan(&hotShape); err != nil {
		t.Fatalf("read hot shape: %v", err)
	}
	if parentShape != hotShape {
		t.Fatalf("父表与 hot 的列形状不同 ⇒ promote 会当场报错（column contract has drifted），"+
			"热表永远排不进月分区。\n--- parent ---\n%s\n--- hot ---\n%s", parentShape, hotShape)
	}

	// 真的调一次函数：形状检查在函数体最前面，retention 设成 100 年 ⇒ 不会搬任何行。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var moved int64
	if err := tx.QueryRow(ctx,
		`SELECT promote_session_turns_hot_to_partition('100 years'::interval, 10)`).Scan(&moved); err != nil {
		if strings.Contains(err.Error(), "column contract has drifted") {
			t.Fatalf("promote 报列集合契约漂移：%v —— 885 的加列破坏了 promote 的前提", err)
		}
		t.Fatalf("promote 调用失败：%v", err)
	}
	if moved != 0 {
		t.Errorf("promote 搬了 %d 行（retention=100 years 时应当是 0）", moved)
	}
	t.Logf("promote 列集合契约成立（parent/hot 形状各 %d 字节），promote 调用返回 %d", len(parentShape), moved)
}

// ---------------------------------------------------------------------------
// G-5 —— 迁移 887 的全文检索腿
// ---------------------------------------------------------------------------

// TestSessionTurnsFTSIndexUsableRealDB proves the 887 GIN index really exists
// in the database and really carries the admin list query.
//
// The admin predicate (admin/logs_search_predicate.go) is:
//
//	(rl.search_text ILIKE $1 OR to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', $2))
//
// The FTS leg only uses the index when the **expression matches the index
// expression verbatim** — a different config ('english' vs 'simple') or a
// different column silently degrades to a sequential scan (no error, just
// slow). That is exactly the class of defect a static shape gate cannot see.
//
// Plan assertion boundary: `enable_seqscan = off` forces the planner to
// consider index paths. This proves the index is **usable** for this query.
// It does NOT prove the planner would **prefer** it on production-scale data
// (that is a different claim, and it is not this gate's job).
func TestSessionTurnsFTSIndexUsableRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	// 前提自证：索引必须真的在 catalog 里。
	//
	// 逐分区索引与父表壳（ON ONLY）都算：迁移 888/877/887 用 format() 逐分区
	// 建索引，父表上是同名壳。任一层缺索引 ⇒ 已晋级的老行查不到。
	ftsIndexes := map[string]bool{}
	trgmIndexes := map[string]bool{}
	rows, err := pool.Query(ctx, `
		SELECT indexname, indexdef FROM pg_indexes
		 WHERE schemaname = 'public'
		   AND tablename IN ('session_turns', 'session_turns_hot')
		   AND (indexdef ILIKE '%to_tsvector%' OR indexdef ILIKE '%gin_trgm_ops%')`)
	if err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			rows.Close()
			t.Fatalf("scan index row: %v", err)
		}
		if strings.Contains(def, "to_tsvector") {
			ftsIndexes[name] = true
		}
		if strings.Contains(def, "gin_trgm_ops") {
			trgmIndexes[name] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_indexes: %v", err)
	}
	if len(ftsIndexes) == 0 {
		t.Fatalf("库里没有任何 to_tsvector 索引 —— 本门前提不成立（迁移 887 未入库？）。" +
			"**不要**把恒绿当通过。")
	}
	if len(trgmIndexes) == 0 {
		t.Fatalf("库里没有任何 gin_trgm_ops 索引 —— 子串回退腿缺失，" +
			"「破坏子串检索」的风险成立")
	}
	t.Logf("FTS 索引 %d 个、trigram 索引 %d 个", len(ftsIndexes), len(trgmIndexes))

	// 真实数据：写一行带独特词的轮次，然后确认它**查得到**。
	stamp := time.Now().UTC().Format("20060102150405.000000000")
	stamp = strings.ReplaceAll(stamp, ".", "")
	reqID := "probe-fts-" + stamp
	const needle = "zqmarkerneedle"
	defer func() {
		for _, tbl := range []string{"public.session_turns", "public.session_turns_hot"} {
			if _, err := pool.Exec(ctx, `DELETE FROM `+tbl+` WHERE request_id = $1`, reqID); err != nil {
				t.Errorf("cleanup %s: %v", tbl, err)
			}
		}
	}()

	w := newTurnWriter(pool)
	if _, err := w.AppendTurn(ctx, TurnRecord{
		SessionID:  "probe-fts-session",
		TenantID:   "probe-fts-tenant",
		RequestID:  reqID,
		Ts:         time.Now().UTC().Truncate(time.Microsecond),
		SearchText: needle + " 一个独特的中文标记",
		DigestJSON: []byte(`{}`),
	}); err != nil {
		t.Fatalf("AppendTurn (fts case): %v", err)
	}

	// 功能正确性（默认 planner）：这行必须能被 FTS 谓词查到。
	//
	// 查询形态照抄 admin 的真实来源（db/request_logs_view_schema.go
	// SessionFamilyTurnsSourceSQL）：两臂 UNION ALL —— hot 表 + 父表。
	// ⚠ 不能用 `session_turns_with_current_month` 视图代替：那个视图**不投影
	// search_text**（实测 information_schema.columns 里没有这一列），拿它
	// 测会得到 42703 —— 那是判据的形状错了，不是产品坏了。
	var hits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT t.search_text FROM public.session_turns_hot t
		  UNION ALL
		  SELECT t.search_text FROM public.session_turns t
		) t
		WHERE to_tsvector('simple', t.search_text) @@ plainto_tsquery('simple', $1)`,
		needle).Scan(&hits); err != nil {
		t.Fatalf("fts count: %v", err)
	}
	if hits != 1 {
		t.Fatalf("FTS 谓词查到 %d 行，want 1 —— 索引存在但检索不到（表达式不同形？数据没落？）", hits)
	}

	// 计划断言：这条谓词**能**走 GIN 索引。
	plan := explainPlan(t, pool, `
		SELECT count(*) FROM (
		  SELECT t.search_text FROM public.session_turns_hot t
		  UNION ALL
		  SELECT t.search_text FROM public.session_turns t
		) t
		WHERE to_tsvector('simple', t.search_text) @@ plainto_tsquery('simple', $1)`, needle)
	if !strings.Contains(plan, "search_fts") {
		t.Fatalf("FTS 查询的计划里没有 search_fts 索引（enable_seqscan=off 下仍不走索引）—— "+
			"索引表达式与查询表达式不同形，或索引不存在。\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on session_turns") {
		t.Fatalf("FTS 查询在 enable_seqscan=off 下仍然顺序扫描 session_turns —— "+
			"索引对这条谓词不可用。\n%s", plan)
	}
	t.Logf("FTS 计划（enable_seqscan=off）：\n%s", plan)
}

// ---------------------------------------------------------------------------
// G-7 —— 迁移 888 的连接索引
// ---------------------------------------------------------------------------

// TestSessionBodiesRequestIDJoinIndexRealDB proves the 888 index really exists
// and really carries the admin turns-list / turns-detail join.
//
// The join (admin/session_turns_v2.go) is:
//
//	LEFT JOIN public.session_bodies_unified b
//	  ON b.tenant_id=t.tenant_id AND b.session_id=t.session_id
//	 AND b.turn_no=t.turn_no AND b.request_id=t.request_id
//
// Four equality predicates; the index added by 888 is
// (tenant_id, session_id, turn_no DESC, request_id) — the join predicate
// matches the index **prefix** exactly, which is what makes the join
// index-only instead of a recheck per row.
func TestSessionBodiesRequestIDJoinIndexRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	// 前提自证：连接索引必须**在 catalog 里**，且列序必须覆盖 join 谓词的四列。
	//
	// ⚠ 不能只按「含 request_id」挑一条：库里同时存在 idx_session_bodies_request
	// （**只有** request_id 一列）。它能服务「按 request_id 查」，但**不**能
	// 服务本 join 的四列等值谓词 ⇒ 挑错索引会把「索引不承重」误判成「索引在」。
	// 判据只认「列序覆盖 (tenant_id, session_id, turn_no, request_id)」的那条。
	rows, err := pool.Query(ctx, `
		SELECT tablename, indexname, indexdef FROM pg_indexes
		 WHERE schemaname = 'public'
		   AND tablename IN ('session_bodies', 'session_bodies_hot')
		   AND indexdef ILIKE '%USING btree%'
		 ORDER BY tablename, indexname`)
	if err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	covering := map[string][]string{}
	for rows.Next() {
		var table, name, def string
		if err := rows.Scan(&table, &name, &def); err != nil {
			rows.Close()
			t.Fatalf("scan index row: %v", err)
		}
		if indexCoversJoinColumns(def) {
			covering[table] = append(covering[table], name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_indexes: %v", err)
	}
	if len(covering["session_bodies"]) == 0 {
		t.Fatalf("session_bodies 上没有覆盖 join 谓词四列 (tenant_id, session_id, turn_no, request_id) " +
			"的 btree 索引 —— 迁移 888 未入库，或列序不对。**不要**把恒绿当通过。")
	}
	if len(covering["session_bodies_hot"]) == 0 {
		t.Fatalf("session_bodies_hot 上没有覆盖 join 谓词四列的 btree 索引 —— " +
			"迁移 888 的 hot 臂未入库。")
	}
	t.Logf("bodies 连接索引：parent=%v hot=%v", covering["session_bodies"], covering["session_bodies_hot"])

	// 计划断言：join 的 bodies 侧必须走索引。
	plan := explainPlan(t, pool, `
		SELECT b.request_delta, b.response_delta
		  FROM (SELECT tenant_id, session_id, turn_no, request_id
		          FROM public.session_turns_hot
		        UNION ALL
		        SELECT tenant_id, session_id, turn_no, request_id
		          FROM public.session_turns) t
		  LEFT JOIN public.session_bodies_unified b
		    ON b.tenant_id=t.tenant_id AND b.session_id=t.session_id
		   AND b.turn_no=t.turn_no AND b.request_id=t.request_id
		 WHERE t.tenant_id=$1 AND t.session_id=$2`,
		"probe-fts-tenant", "probe-fts-session")
	if strings.Contains(plan, "Seq Scan on session_bodies") {
		t.Fatalf("bodies join 在 enable_seqscan=off 下仍然顺序扫描 session_bodies —— "+
			"连接索引对这条 join 不可用。\n%s", plan)
	}
	if !strings.Contains(plan, "session_bodies") {
		t.Fatalf("计划里根本没有 session_bodies —— 形状与预期不符，判据前提有疑问。\n%s", plan)
	}
	if !strings.Contains(plan, "tenant_session_turn_request") {
		t.Fatalf("bodies join 的计划里没有 888 的连接索引（*_tenant_session_turn_request）—— "+
			"这条 join 仍然不承重。\n%s", plan)
	}
	t.Logf("bodies join 计划（enable_seqscan=off）：\n%s", plan)
}

// indexCoversJoinColumns reports whether a btree index definition carries the
// four equality columns of the admin turns join, **in that order**.
//
// Order matters: a btree index only serves a multi-column equality predicate
// when the predicate's column order matches the index's leading columns.
func indexCoversJoinColumns(indexDef string) bool {
	open := strings.Index(indexDef, "(")
	close := strings.LastIndex(indexDef, ")")
	if open < 0 || close <= open {
		return false
	}
	cols := strings.Split(indexDef[open+1:close], ",")
	got := make([]string, 0, len(cols))
	for _, c := range cols {
		c = strings.TrimSpace(c)
		c = strings.TrimSuffix(c, " DESC")
		c = strings.TrimSuffix(c, " ASC")
		got = append(got, strings.TrimSpace(c))
	}
	want := []string{"tenant_id", "session_id", "turn_no", "request_id"}
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// explainPlan returns the EXPLAIN (COSTS OFF) text of stmt with
// `enable_seqscan = off` in effect.
//
// Why enable_seqscan=off: on a test-sized table the planner picks a
// sequential scan **because the table is tiny**, not because the index is
// bad. Forcing the index path turns the question from "would the planner
// prefer it here" into "is the index usable for this statement at all" —
// the latter is the claim this file is entitled to make.
func explainPlan(t *testing.T, pool *pgxpool.Pool, stmt string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin (explain): %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("SET enable_seqscan: %v", err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+stmt, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain row: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate explain rows: %v", err)
	}
	if len(lines) == 0 {
		t.Fatalf("EXPLAIN 返回空计划 —— 判据前提有疑问")
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// G-7② —— 迁移 888 的 sessions 索引：抗连接的**计划级**判据
// ---------------------------------------------------------------------------

// TestSessionsPartitionDateAntiJoinUsesIndexRealDB proves the **second** 888
// index — idx_sessions_tenant_session_partition_date — actually carries the
// "latest partition row" anti-join.
//
// 2026-10-21（判据自审轮）：G-7 的验收判据原文是「**每条**新索引都有**对应查询**的
// 判据（EXPLAIN 不再 Seq Scan / 不再扫全部分区）」—— 这是个**全称量词**。
// bodies 臂有计划级真库腿（TestSessionBodiesRequestIDJoinIndexRealDB），
// ★ 而 sessions 臂**只有**静态门（索引列序 + 四个读方存在）
// （sql/migrations/startup/session_family_join_index_gaps_test.go:214）
// ⇒ **全称量词没被证明** ⇒ 该行当时不能叫「已闭合」。
// ⇒ 本门补上计划级的那一半。
//
// 生产实际用的谓词（internal/titlestore/store.go:271 /
// admin/turns_sessions.go:190 的 turnsSessionsLatestPartitionSQL）：
//
//	partition_date = (SELECT MAX(x.partition_date) FROM public.sessions x
//	                  WHERE x.tenant_id = s.tenant_id AND x.session_id = s.session_id)
//
// 本门证明三件事：
//  1. 索引在 catalog 里，且列序 = (tenant_id, session_id, partition_date DESC)；
//  2. 它是**分区索引**（建在 ONLY 父表上）⇒ **至少有一个分区子索引** ——
//     没有子索引的话父级索引在计划里根本不出现，这是「catalog 查得到、EXPLAIN
//     看不到」的最隐蔽一种；
//  3. 抗连接子查询在 enable_seqscan=off 下**能**走该索引，且**不**顺序扫 sessions。
//
// 诚实边界（同本文件其它计划门）：enable_seqscan=off 只证明「**能**用上该索引」，
// **不**证明生产数据量下 planner **会**选它。
func TestSessionsPartitionDateAntiJoinUsesIndexRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	// ── 前提自证 ①：索引在 catalog 里，且列序覆盖三列 ──────────────────────
	rows, err := pool.Query(ctx, `
		SELECT indexdef FROM pg_indexes
		 WHERE schemaname='public' AND tablename='sessions'
		   AND indexname='idx_sessions_tenant_session_partition_date'`)
	if err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	var def string
	found := false
	for rows.Next() {
		if err := rows.Scan(&def); err != nil {
			rows.Close()
			t.Fatalf("scan indexdef: %v", err)
		}
		found = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_indexes: %v", err)
	}
	if !found {
		t.Fatalf("public.sessions 上没有 idx_sessions_tenant_session_partition_date —— " +
			"迁移 888 的 sessions 臂未入库。**不要**把恒绿当通过。")
	}
	if !sessionsIndexCoversLatestPartition(def) {
		t.Fatalf("idx_sessions_tenant_session_partition_date 的列序不对"+
			"（判据要求 tenant_id, session_id, partition_date DESC）：\n%s", def)
	}

	// ── 前提自证 ②：分区索引必须有子索引 ──────────────────────────────────
	// 它建在 ONLY 父表上 ⇒ 那是 partitioned index，真正承重的是各分区上的子索引。
	// 只查父表的 pg_indexes 会造出「索引存在」的假象 ⇒ 必须查子索引。
	var childCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		 WHERE schemaname='public'
		   AND tablename LIKE 'sessions\_%'
		   AND indexname LIKE '%tenant_session_partition_date%'`).Scan(&childCount); err != nil {
		t.Fatalf("count partition child indexes: %v", err)
	}
	if childCount == 0 {
		t.Fatalf("idx_sessions_tenant_session_partition_date 在任何分区上都没有子索引 —— " +
			"父级索引是 partitioned index，没有子索引它在计划里根本不出现。")
	}
	t.Logf("sessions 抗连接索引：父级=%s 子索引数=%d", def, childCount)

	// ── 计划断言 ─────────────────────────────────────────────────────────
	// 探针用不存在的 session_id：**不需要**真有行 —— EXPLAIN（不带 ANALYZE）
	// 只出计划形状，不执行 ⇒ 计划里该不该用索引，与有没有行无关。
	plan := explainPlan(t, pool, `
		SELECT s.session_id
		  FROM public.sessions s
		 WHERE s.tenant_id=$1 AND s.session_id=$2
		   AND s.partition_date = (SELECT MAX(x.partition_date) FROM public.sessions x
		                            WHERE x.tenant_id = s.tenant_id AND x.session_id = s.session_id)`,
		"default", "gw_probe_sessions_anti_join")
	if strings.Contains(plan, "Seq Scan on sessions") {
		t.Fatalf("sessions 抗连接在 enable_seqscan=off 下仍然顺序扫描 sessions —— "+
			"idx_sessions_tenant_session_partition_date 对这条谓词不可用。\n%s", plan)
	}
	if !strings.Contains(plan, "tenant_session_partition_date") {
		t.Fatalf("sessions 抗连接的计划里没有 idx_sessions_tenant_session_partition_date —— "+
			"这条读路径仍然不承重（判据要求的「不再扫全部分区」没有兑现）。\n%s", plan)
	}
	t.Logf("sessions 抗连接计划（enable_seqscan=off）：\n%s", plan)
}

// sessionsIndexCoversLatestPartition reports whether the sessions index
// definition carries exactly (tenant_id, session_id, partition_date DESC).
//
// The DESC on the third column is not decoration: the production predicate is
// "partition_date = (SELECT MAX(partition_date) ...)", so the index has to
// serve a **descending** max lookup. An ascending
// (tenant_id, session_id, partition_date) index answers the same equality but
// does not match the ORDER BY DESC that max() wants.
func sessionsIndexCoversLatestPartition(indexDef string) bool {
	openIdx := strings.Index(indexDef, "(")
	closeIdx := strings.LastIndex(indexDef, ")")
	if openIdx < 0 || closeIdx <= openIdx {
		return false
	}
	parts := strings.Split(indexDef[openIdx+1:closeIdx], ",")
	got := make([]string, 0, len(parts))
	for _, p := range parts {
		got = append(got, strings.ToLower(strings.TrimSpace(p)))
	}
	want := []string{"tenant_id", "session_id", "partition_date desc"}
	return strings.Join(got, ",") == strings.Join(want, ",")
}
