package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClampRecoverableProbeSchedulesSQLShape(t *testing.T) {
	sql := clampRecoverableProbeSchedulesSQL()
	for _, want := range []string{
		"UPDATE node_probe_state",
		"make_interval(secs => $1)",
		"http_429",
		"http_5%",
		"timeout%",
		"http_404",
		"http_410",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("clamp SQL missing %q", want)
		}
	}
	// Must not touch healthy-parked rows (no error evidence).
	if !strings.Contains(sql, nodeProbeErrorEvidenceSQL("node_probe_state")) {
		t.Fatal("clamp SQL must require row-level error evidence")
	}
	// ★★ 2026-11-05（P-11）：★ 这条 UPDATE 必须【有上界】。
	//   ★ 形态：★ CTE 取一批（ctid）⇒ ★ UPDATE 只改那一批 ⇒ ★ 行锁足迹与时长可预测。
	//   ★ ★★ 为什么判 ctid 而不是业务列：★ ctid 是**单语句内的物理行标识**
	//   ★   ⇒ ★ 即便该表将来被改成无唯一键形态，ctid 仍给出严格上界
	//   ★   （业务列在无唯一键时会放大行数 ⇒ 上界失效）。
	//   ★ ★★ 2026-11-08（第 51 增量审计 F-2）事实订正：★ 原注释断言
	//   ★   「node_probe_state **没有唯一键**」——**该断言与事实相反**：
	//   ★   node_probe_state_pkey = (credential_id, raw_model_name) 一直存在
	//   ★   （建表迁移 341/402 即带 PRIMARY KEY；211 生产库实测确认）。
	//   ★   ⇒ 实现的 ctid 方案本身安全（且对无唯一键形态更稳），本轮**只订正事实**、
	//   ★   不改实现 —— 判据建立在假事实上会误导后续维护者。
	if !strings.Contains(sql, "WITH victims AS") {
		t.Fatal("clamp SQL must bound itself with a CTE batch")
	}
	if !strings.Contains(sql, "LIMIT $2") {
		t.Fatal("clamp SQL must bound itself with LIMIT $2 (★ 一次夹无限行 ⇒ 锁足迹不可预测)")
	}
	if !strings.Contains(sql, "s.ctid = v.ctid") {
		t.Fatal("clamp SQL must join the bounded batch by ctid (★ 单语句物理标识 ⇒ 无唯一键形态下仍严格上界)")
	}
	// ★ 2026-11-07（持续失败升级）：consecutive_failures ≥ 阈值的行被排除 ——
	//   它们的远期排程是升级裁决（sustained_failure_recheck），clamp 不得把
	//   升级拉回恢复 SLA。
	if !strings.Contains(sql, "COALESCE(consecutive_failures, 0) < $3") {
		t.Fatal("clamp SQL must exempt sustained-failure rows (cf < $3)")
	}
}

func TestPumpDueStatesCallsClamp(t *testing.T) {
	src, err := readSource("node_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "func (w *NodeProbeWorker) pumpDueStatesToQueue"
	idx := strings.Index(src, anchor)
	if idx < 0 {
		t.Fatal("pumpDueStatesToQueue not found")
	}
	// ★ 窗口放宽：★ 2026-11-05 起 clamp 前面多了一段「为什么必须用自己的 ctx」的注释
	//   ⇒ ★ 800 字节窗口已装不下调用点（★ 判据会因注释变长而误红 ⇒ ★ 判据不许钉死位置）。
	body := src[idx : idx+3000]
	if !strings.Contains(body, "clampRecoverableProbeSchedules") {
		t.Fatal("pumpDueStatesToQueue must clamp recoverable futures before selecting due rows")
	}
}

// ★★ 2026-11-05（P-11）：★ clamp **必须有自己的 ctx**。
//
//	★ 形态：★ 它曾经与泵查询共用同一个 10s qCtx ⇒ ★ clamp 一超时/等锁 ⇒ ★ **同一个 ctx 被取消**
//	★ ⇒ ⇒ 泵查询随之失败 ⇒ ⇒ 整轮「到期探测入队」被丢掉（★ 症状同类 2026-08-18 的
//	★ 「仪表盘显示 ready probes、实际零探测」⇒ ★ 排空队列那一步没跑）。
func TestPumpDueStatesClampDoesNotSharePumpContext(t *testing.T) {
	src, err := readSource("node_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "func (w *NodeProbeWorker) pumpDueStatesToQueue"
	idx := strings.Index(src, anchor)
	if idx < 0 {
		t.Fatal("pumpDueStatesToQueue not found")
	}
	body := src[idx : idx+1400]
	if !strings.Contains(body, "context.WithTimeout(ctx, nodeProbeClampTimeout)") {
		t.Fatal("clamp must run on its own context/budget (★ 共用 qCtx ⇒ clamp 超时会取消泵查询)")
	}
	// ★ clamp 调用与泵查询之间必须有一个**独立的** ctx 变量 ⇒ 不能是同一个 qCtx。
	clampAt := strings.Index(body, "w.clampRecoverableProbeSchedules(clampCtx)")
	queryAt := strings.Index(body, "w.db.Query(qCtx")
	if clampAt < 0 {
		t.Fatal("clamp must be called with clampCtx (★ not the shared qCtx)")
	}
	if queryAt < 0 {
		t.Fatal("pump query not found")
	}
	if clampAt > queryAt {
		t.Fatal("clamp must run before the pump query (★ clamp 拉回的行才进得了这一轮)")
	}
}

// ★★★ 真库行为回归（2026-11-05 · 12h 审计轮 · P-11）★★★
//
// ★★ 「SQL 里有 LIMIT」只是**形态**；★ 真正的承诺是【一批只改 N 行】⇒ ★ 必须在真库上量。
//
//	★ 同 bg/analyze_throttle_840_realdb_test.go 的教训：★ 机制断言 ≠ 效果断言。
//
// ★★ 用【临时表】遮蔽真表（★ temp schema 优先）⇒ ★ 绝不碰真实 node_probe_state。
// ★★ 门控：★ 无 TEST_DATABASE_URL / TEST_DB_URL ⇒ 显式跳过（★ 不算通过、也不算失败）。
func TestClampRecoverableProbeSchedulesBoundRealDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("TEST_DB_URL"))
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 ⇒ 跳过 clamp 上界真库回归（形态判据仍跑）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连库失败：%v", err)
	}
	defer p.Close()

	if _, err := p.Exec(ctx, `
		CREATE TEMP TABLE node_probe_state (
			credential_id bigint NOT NULL,
			raw_model_name text NOT NULL,
			consecutive_failures integer DEFAULT 0 NOT NULL,
			consecutive_successes integer DEFAULT 0 NOT NULL,
			last_attempt_at timestamptz,
			next_retry_at timestamptz DEFAULT now() NOT NULL,
			next_retry_seconds integer DEFAULT 5 NOT NULL,
			paused boolean DEFAULT false NOT NULL,
			last_run_id bigint,
			last_direct_ok boolean,
			last_gateway_ok boolean,
			last_err_code text,
			updated_at timestamptz DEFAULT now() NOT NULL
		)`); err != nil {
		t.Fatalf("建临时表失败：%v", err)
	}
	// 5 行可恢复类（超期 ⇒ 应被拉回）+ 1 行永久类（404 ⇒ 不得动）+ 1 行未超期（⇒ 不得动）
	// + 1 行「持续失败类」（consecutive_failures ≥ 阈值 ⇒ 远期排程是**升级裁决**，
	//   不是遗留漂移 ⇒ clamp 不得把它拉回 5 分钟 ⇒ 见 $3 谓词）
	threshold := sustainedFailureAttemptThreshold()
	if _, err := p.Exec(ctx, `
		INSERT INTO node_probe_state
			(credential_id, raw_model_name, next_retry_at, next_retry_seconds, last_err_code, consecutive_failures, last_direct_ok)
		SELECT g, 'm'||g, now() + interval '3 hours', 10800, 'http_503', 3, false
		FROM generate_series(1,5) g;
		INSERT INTO node_probe_state
			(credential_id, raw_model_name, next_retry_at, next_retry_seconds, last_err_code, consecutive_failures, last_direct_ok)
		VALUES (99,'perm', now()+interval '3 hours', 10800, 'probe_http_404', 2, false),
		       (98,'fresh', now()+interval '10 seconds', 10, 'timeout', 1, false)`); err != nil {
		t.Fatalf("造数失败：%v", err)
	}
	// ★ 持续失败行单独一条（带参数 ⇒ pgx 走扩展协议 ⇒ 不能与其它语句同批）。
	if _, err := p.Exec(ctx, `
		INSERT INTO node_probe_state
			(credential_id, raw_model_name, next_retry_at, next_retry_seconds, last_err_code, consecutive_failures, last_direct_ok)
		VALUES (97,'sustained', now()+interval '3 hours', 10800, 'http_503', $1, false)`, threshold); err != nil {
		t.Fatalf("造数失败（持续失败行）：%v", err)
	}

	countClamped := func(tag string) int {
		var n int
		if err := p.QueryRow(ctx,
			`SELECT count(*) FROM node_probe_state WHERE next_retry_seconds = 300`).Scan(&n); err != nil {
			t.Fatalf("%s 统计失败：%v", tag, err)
		}
		return n
	}

	// ① 一批上界：LIMIT=2 ⇒ 精确只改 2 行
	//
	// ★★ 2026-11-10 修正（过期断言修复）：生产调用点
	//   （bg/node_probe.go 的 clamp 调用）在 2026-11-07 的提交 60c7de29f 里
	//   给 clamp SQL 增加了第三个占位符 $3（持续失败阈值），
	//   **但本测试仍只传 2 个参数** ⇒ 在真库环境下必然报
	//   “mismatched param and argument count”。
	//   ★ 该用例平时被 skip 掩盖（无 TEST_DATABASE_URL ⇒ 不跑）⇒ 真库门才暴露。
	//   ⇒ 这里按生产调用点的形态传第三个参数，**并保留原有意图**
	//     （一批只改 N 行 / 永久类与未超期行不得动）。
	if _, err := p.Exec(ctx, clampRecoverableProbeSchedulesSQL(), 300, nodeProbeClampBatch/100, threshold); err != nil {
		t.Fatalf("clamp 执行失败：%v", err)
	}
	if got := countClamped("LIMIT=2"); got != 2 {
		t.Fatalf("clamp 一批上界失效：LIMIT=2 却改了 %d 行（★ 期望恰好 2 行）", got)
	}

	// ② 永久类与未超期行【不得】被动
	var perm, fresh, sustained int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM node_probe_state WHERE raw_model_name='perm' AND next_retry_seconds=10800`).Scan(&perm); err != nil {
		t.Fatalf("统计 perm 失败：%v", err)
	}
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM node_probe_state WHERE raw_model_name='fresh' AND next_retry_seconds=10`).Scan(&fresh); err != nil {
		t.Fatalf("统计 fresh 失败：%v", err)
	}
	// ★★ 新契约判别面（$3）：consecutive_failures ≥ 阈值的行，其远期排程是
	//   「持续失败升级」的裁决结果 ⇒ clamp **不得**把它拉回 5 分钟。
	//   （这一条是 2026-11-07 引入 $3 时**漏写**的断言，本轮补上。）
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM node_probe_state WHERE raw_model_name='sustained' AND next_retry_seconds=10800`).Scan(&sustained); err != nil {
		t.Fatalf("统计 sustained 失败：%v", err)
	}
	if perm != 1 || fresh != 1 || sustained != 1 {
		t.Fatalf("clamp 误伤了不该动的行：perm=%d fresh=%d sustained=%d（★ 期望各 1）",
			perm, fresh, sustained)
	}

	// ③ 大批量 ⇒ 剩下的可恢复行全部被拉回（持续失败行仍不得动）
	if _, err := p.Exec(ctx, clampRecoverableProbeSchedulesSQL(), 300, nodeProbeClampBatch, threshold); err != nil {
		t.Fatalf("clamp 第二批执行失败：%v", err)
	}
	if got := countClamped("LIMIT=full"); got != 5 {
		t.Fatalf("clamp 未覆盖全部可恢复行：改了 %d 行（★ 期望 5）", got)
	}
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM node_probe_state WHERE raw_model_name='sustained' AND next_retry_seconds=10800`).Scan(&sustained); err != nil {
		t.Fatalf("统计 sustained 失败：%v", err)
	}
	if sustained != 1 {
		t.Fatalf("持续失败行被 clamp 拉回了（sustained 仍为 10800 的行数=%d，★ 期望 1）", sustained)
	}
}
