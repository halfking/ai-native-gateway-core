//go:build integration

package admin

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

// §8 第 2 项（2026-10-01 拍板通过）：`loadSessions` 的读源从
// `request_logs_with_current_month` 迁到 session 族原生源。
//
// 这道门钉的是**可证伪的差异**，不是「新旧结果相等」——
// 审计 §8.2 实测两者**本就不相等**，且不等的方向是这次迁移的全部意义：
//
//	会话数              15,088 → 13,660（−1,428 / −9.46%）
//	只在 v1 出现的会话        1,428   真业务轮次 0
//	只在原生源出现的会话          0
//	共有会话 request_count 不同     6 条（合计 +35 轮 = 0.07%）
//	共有会话 error_count 不同        0 条（9,323 = 9,323）
//
// 消失的 1,428 条全是 internal_loopback / non_terminal —— 网关自己生成的
// 标题/摘要 LLM 调用与 in_progress 占位。如果哪天这个前提不成立了（真业务
// 轮次开始从列表消失），这道门必须报红：那就不是修正，是丢数据。
//
// 跑的是**生产同一条 SQL**（buildSessionListCountSQL / buildSessionListRowsSQL），
// 不重抄一遍 —— 重抄的对比在换源那一刻就会与生产分叉。
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://llm_gateway:...@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestSessionListNativeSourceDropIsInternalOnly
func TestSessionListNativeSourceDropIsInternalOnly(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence about the migration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const hours = 72
	tenant := "default"
	// hours 参与 `($2 || ' hours')::interval` 的**文本**拼接，所以必须绑字符串
	// —— 与生产 `fmt.Sprintf("%d", hours)` 同形态。绑 int 会在编码期报
	// "unable to encode 72 into text format"，而不是在 SQL 层给出可读的错。

	// 1) 生产 SQL 本身必须能在真库跑通（这同时是它的可执行性证据）。
	rowsSQL, args := buildSessionListRowsSQL(tenant, "", hours)
	rows, err := pool.Query(ctx, rowsSQL, args...)
	if err != nil {
		t.Fatalf("production session-list rows query: %v", err)
	}
	nativeSessions := 0
	for rows.Next() {
		nativeSessions++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("production rows iteration: %v", err)
	}
	if nativeSessions == 0 {
		t.Skip("no native-source sessions in the window — this run proves nothing either way")
	}

	// 2) 计数与列表行必须同口径，否则分页会出现「总数与末页对不上」。
	countSQL, countArgs := buildSessionListCountSQL(tenant, "", hours)
	var nativeCount int
	if err := pool.QueryRow(ctx, countSQL, countArgs...).Scan(&nativeCount); err != nil {
		t.Fatalf("production session-list count query: %v", err)
	}
	if nativeCount != nativeSessions {
		t.Fatalf("count=%d but rows=%d — the count and the page rows disagree, which shows up "+
			"only in production as 'total says 900 but page 3 is empty':\\n%s\\n%s",
			nativeCount, nativeSessions, countSQL, rowsSQL)
	}

	// 3) 旧口径（v1 视图）作为对照，取它独有的会话。
	var v1Only, v1OnlyWithBusinessTurns int
	err = pool.QueryRow(ctx, `
		WITH v1 AS (
		  SELECT DISTINCT rl.gw_session_id AS sid
		    FROM request_logs_with_current_month rl
		   WHERE rl.gw_session_id IS NOT NULL AND rl.gw_session_id <> ''
		     AND rl.tenant_id = $1 AND rl.ts >= NOW() - ($2 || ' hours')::interval),
		nat AS (
		  SELECT DISTINCT t.session_id AS sid
		    FROM public.session_turns t
		   WHERE t.tenant_id = $1 AND t.ts >= NOW() - ($2 || ' hours')::interval
		  UNION
		  SELECT DISTINCT t.session_id
		    FROM public.session_turns_hot t
		   WHERE t.tenant_id = $1 AND t.ts >= NOW() - ($2 || ' hours')::interval)
		SELECT count(*),
		       count(*) FILTER (WHERE EXISTS (
		         SELECT 1 FROM request_logs_with_current_month rl
		          WHERE rl.gw_session_id = v1.sid AND rl.tenant_id = $1
		            AND rl.ts >= NOW() - ($2 || ' hours')::interval
		            AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'))
		  FROM v1 WHERE NOT EXISTS (SELECT 1 FROM nat WHERE nat.sid = v1.sid)`,
		tenant, strconv.Itoa(hours)).Scan(&v1Only, &v1OnlyWithBusinessTurns)
	if err != nil {
		t.Fatalf("v1-only classification: %v", err)
	}

	t.Logf("原生源会话 %d（生产 SQL 实跑）｜v1 独有 %d 条｜其中含真业务轮次的 %d 条",
		nativeSessions, v1Only, v1OnlyWithBusinessTurns)

	// 这条是**方向性**不变式，不是等价性：原生源少掉的必须是内部调用。
	// 一旦有真业务轮次消失，门必须报红 —— 那就不是修正，是丢数据。
	if v1OnlyWithBusinessTurns > 0 {
		t.Fatalf("%d of the %d sessions the migration drops contain genuine_loss turns "+
			"(real business traffic). The migration was approved on the measured premise that "+
			"only internal_loopback / non_terminal rows are dropped; that premise no longer "+
			"holds, so this list would be hiding real user conversations. Dropped-internal "+
			"sessions: %d.",
			v1OnlyWithBusinessTurns, v1Only, v1Only-v1OnlyWithBusinessTurns)
	}
	if v1Only == 0 {
		t.Log("no v1-only session in this window — the two sources already agree; " +
			"the drop claim is untested by this run")
	}
}
