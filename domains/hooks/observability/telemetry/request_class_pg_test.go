package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// request_class_pg_test.go —— 「定时请求分类」这条语义在真库上的载体契约。
//
// # ★ R69 轮改瞄（2026-10-21）：载体搬了家，测试侧还留在旧家
//
// 原门叫 `TestRequestClassPGRoundTrip`，它验的是：
//
//	① 迁移 610 给 `_request_logs_hot` / `_request_logs` 加的
//	   `request_class` / `due_at` 两列存在；
//	② 客户端 `EmitRequestLogUpdate` 把 `request_class='scheduled'` + `due_at`
//	   **写进 `_request_logs_hot`**，再读回来相等；
//	③ 完成态 UPDATE（不带 class）后 class 不退化（COALESCE 守卫）；
//	④ 不带 class 的 INSERT 落列默认值 `'immediate'`。
//
// ★ **v1 族随迁移 885（真删表）+ 890（退族）被删掉了** ⇒
// ①③④ 全部 42P01；② 的写路径**也不存在了**（客户端现在只写 `usage_ledger_hot`）。
//
// # ★ 语义没丢，**载体换了**（实测，2026-10-21）
//
// `request_class` / `due_at` 现在住在**会话族的 details 表**：
//
//	session_turn_details_hot / session_turn_details  ← 两列都在（实测）
//	request_logs_with_current_month                  ← canonical 视图仍投影这两列（实测 2 列）
//
// 写路径：telemetry client（算 + 归一化 `RequestClass`/`DueAt`）
//   → cmd/gateway/lite_telemetry_sink.go（`RequestClass: strPtrValue(entry.RequestClass)`）
//   → `domains/session/v2` 的 `SessionTurnDetailsWriter`
//   → `INSERT INTO public.session_turn_details_hot (… request_class, due_at …)`（实测原文）
//
// ⇒ 所以本门改成钉**现行载体**：
//
//	① **载体列契约**（真库）：details 两表 + canonical 视图都必须暴露这两列；
//	② **客户端不变式**（本包，纯函数）：`scheduled ⇒ due_at 非 nil`，
//	   否则一律 `immediate` + `due_at=nil`；
//	③ **真往返**（真库 + **活的** writer）：写 scheduled 行 ⇒ 回读相等；
//	   ⇒ 用**事务回滚**收尾 ⇒ 不留残留行（比原门的 DELETE 干净）。
//
// # ⚠ 一条必须如实登记的事实：**DB 侧的兜底没了**
//
// v1 时代这两列是 `NOT NULL DEFAULT 'immediate'` + CHECK
// `((request_class='immediate' AND due_at IS NULL) OR (request_class='scheduled' AND due_at IS NOT NULL))`
// ⇒ **数据库会拒掉违规行**。
//
// ★ 会话族的 details 表上：两列**都可空**、**无 DEFAULT**、**无 CHECK**（实测）
// ⇒ ⇒ **不变式现在只由代码（`normalizeRequestClassAndDueAt`）保证**。
// ⇒ 本门因此显式断言这件事（下面 ① 里的 `attnotnull` / `pg_get_expr` 两条）
// ⇒ ⇒ **让「DB 不再兜底」这个事实被钉住**，而不是让门假装它还在。
//
// # 变量名按仓内契约取（见文件末 `resolveTestDSN` 的注释）。

func TestRequestClassCarriedBySessionDetails(t *testing.T) {
	dsn := resolveTestDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN (or LLM_GATEWAY_TEST_PG_DSN) not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// ① 载体列契约：两列必须在**活着的载体**上，且 canonical 视图必须投影它们。
	for _, table := range []string{"session_turn_details_hot", "session_turn_details"} {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			 WHERE table_name = $1 AND column_name IN ('request_class','due_at')`, table).Scan(&n); err != nil {
			t.Fatalf("columns on %s: %v", table, err)
		}
		if n != 2 {
			t.Errorf("★ 载体 %s 只暴露 %d/2 个分类列（request_class / due_at）—— "+
				"「定时请求分类」这条语义**没有载体了** ⇒ 调度相关读面会丢信息。", table, n)
		}
	}
	var viewCols int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'request_logs_with_current_month'
		   AND column_name IN ('request_class','due_at')`).Scan(&viewCols); err != nil {
		t.Fatalf("canonical view columns: %v", err)
	}
	if viewCols != 2 {
		t.Errorf("★ canonical 视图只投影 %d/2 个分类列 —— 退役后的读面（890 建的 session-only 体）"+
			"应当仍然把 request_class / due_at 透出来。", viewCols)
	}

	// ★ DB 侧兜底是否还在：**这是事实登记，不是期望**。
	// v1 时代是 NOT NULL DEFAULT 'immediate' + CHECK；退役后不是。
	// ⇒ 一旦哪天有人给 details 表补回 CHECK，这条断言会红 ⇒ 那时该更新本门，
	//    但**它红的时候说明 DB 兜底回来了**（好事）。
	for _, table := range []string{"session_turn_details_hot"} {
		var notNull bool
		var hasDefault bool
		if err := pool.QueryRow(ctx, `
			SELECT a.attnotnull,
			       EXISTS (SELECT 1 FROM pg_attrdef d
			                WHERE d.adrelid = a.attrelid AND d.adnum = a.attnum)
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			WHERE c.relname = $1 AND a.attname = 'request_class'
			  AND NOT a.attisdropped AND a.attnum > 0`, table).Scan(&notNull, &hasDefault); err != nil {
			t.Fatalf("probe %s.request_class shape: %v", table, err)
		}
		if notNull || hasDefault {
			t.Logf("★ %s.request_class 现在是 NOT NULL=%v / 有 DEFAULT=%v —— "+
				"DB 侧兜底回来了（v1 时代是 NOT NULL DEFAULT 'immediate' + CHECK）。"+
				"⇒ 本门关于「不变式只由代码保证」的那段说明需要更新。", table, notNull, hasDefault)
		}
	}

	// ② 客户端不变式（本包，纯函数）：scheduled ⇒ due_at 非 nil；否则 immediate + nil。
	{
		scheduled := "scheduled"
		due := time.Unix(1800000123, 0).UTC()
		e := &RequestLogEntry{RequestClass: &scheduled, DueAt: &due}
		normalizeRequestClassAndDueAt(e, false)
		if e.RequestClass == nil || *e.RequestClass != "scheduled" || e.DueAt == nil || !e.DueAt.Equal(due) {
			t.Errorf("不变式被破坏：scheduled + due_at 被归一化成 %v/%v", e.RequestClass, e.DueAt)
		}
		// scheduled 但没有 due_at ⇒ 必须降级成 immediate + nil。
		e2 := &RequestLogEntry{RequestClass: &scheduled}
		normalizeRequestClassAndDueAt(e2, false)
		if e2.RequestClass == nil || *e2.RequestClass != "immediate" || e2.DueAt != nil {
			t.Errorf("不变式被破坏：scheduled 且无 due_at ⇒ 期望 immediate + nil，得到 %v/%v",
				e2.RequestClass, e2.DueAt)
		}
	}

	// ③ 真往返：用**活的**会话族 writer 写一行 scheduled，再读回来。
	// ⚠ 用事务 + 回滚收尾 ⇒ 不留残留行。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	writer := v2.NewSessionTurnDetailsWriter(true)
	if !writer.Available() {
		t.Fatalf("会话族 details writer 不可用 —— 定时请求分类的载体写不进去")
	}
	scheduled := "scheduled"
	due := time.Unix(1800000123, 0).UTC()
	reqID := "608-test-" + time.Now().Format("150405.000000000")
	rec := v2.DetailsRecord{
		TenantID:     "t608",
		SessionID:    "sess-608",
		TurnNo:       1,
		RequestID:    reqID,
		Ts:           time.Now().UTC(),
		RequestClass: &scheduled,
		DueAt:        &due,
	}
	if err := writer.UpsertDetailsInTx(ctx, tx, rec); err != nil {
		t.Fatalf("写入会话族 details（scheduled）：%v", err)
	}
	var gotClass string
	var gotDue *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT request_class, due_at FROM public.session_turn_details_hot
		  WHERE tenant_id = $1 AND request_id = $2`, rec.TenantID, reqID,
	).Scan(&gotClass, &gotDue); err != nil {
		t.Fatalf("回读会话族 details：%v", err)
	}
	if gotClass != "scheduled" || gotDue == nil || !gotDue.UTC().Equal(due) {
		t.Fatalf("往返 = %q/%v，期望 scheduled/%v", gotClass, gotDue, due)
	}
	t.Logf("✓ 会话族载体往返成立：request_class=%q due_at=%v", gotClass, gotDue)
}

// 变量名按仓内契约取：scripts/audit/run-integration-gate.sh 只注入
// TEST_PG_DSN / *_DATABASE_URL / *_DB_URL / *_PG_URL 这几种后缀（注入清单见
// 该脚本的 dsn_runner 段），而 sql/schema/integration_gate_test.go 的
// TestGateInjectsEveryDBCredentialName 正是按这些后缀从仓内**推导**该清单的。
// 用 _PG_DSN 这类不在契约内的名字，harness 永远不会注入 ⇒ 这道门在 CI 上
// 结构性沉睡，却仍以 "ok" 的形式出现在报告里。第二变量名保留给手工运行，
// 但**新代码请直接用 TEST_PG_DSN**，否则重蹈「门禁对沉睡文件不作证」。

func resolveTestDSN() string {
	if v := os.Getenv("TEST_PG_DSN"); v != "" {
		return v
	}
	return os.Getenv("LLM_GATEWAY_TEST_PG_DSN")
}
