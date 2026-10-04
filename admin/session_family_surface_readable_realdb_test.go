package admin

// 存储面可读性门（2026-10-05，§9.184）。
//
// # 这道门测的是什么
//
// 会话族的每一对存储面（父表 + `_hot`）在产品里都被同一种 SQL 形状读取：
// **子查询内 `UNION ALL` 两个面**（`annotation_handler.go:680-693`、
// `session_sanitize_matches.go:198-200`、`bg/credential_selfcheck.go:309-311`、
// 710 视图体、以及 `validate_sessions_v2/loader.go:115` 的 `v1BodyQuery`）。
//
// 静态门（`TestNoBareParentSessionFamilyRead`）管的是**代码有没有读两个面**。
// 本门管的是另一件事：**这个数据库到底能不能执行那个形状**。
// 两者不互相替代——代码写对了、库跑不了，对使用者是同一件事：读不到。
//
// # 为什么要单独立这一条
//
// §9.184 的发现：本机库上 `public.request_logs_bodies`（V1 body 存储，分区父表）
// 一旦出现在子查询内 UNION ALL 里就报
// `invalid perminfoindex 0 in RTE with relid 0`，**12/12 确定性失败**，
// `EXPLAIN` 阶段即失败（执行器初始化，不是执行）。
// 后果是 `validate_sessions_v2` 的 `LoadV1Turns` **恒失败** ⇒ `ExecuteRepair`
// 在本机从来跑不起来。
//
// **这类故障不会被任何静态门发现**：代码是对的，是库坏了。
// 而它对「退役 `request_logs`、只用 `session_*`」这条主线是直接相关的——
// V1 侧 body 存储读不出来，就无法用它做迁移源的交叉校验。
//
// # 口径
//
//   - 每对都**真的执行**一次生产同款形状，取 `count()`。
//   - **不是** EXPLAIN：EXPLAIN 只过计划期；本故障恰好在计划期就炸，
//     但用执行取数可以顺带确认「跑得动且读得到行」，两者都覆盖。
//   - 失败按**表的实际报错**分类，不允许任何「默认判过」。
//     本门第一版的分类器只匹配 `perminfoindex`、把**连接失败**默认判成 OK，
//     导致整张矩阵读数全错（§9.184.4）。分类器的默认分支必须显式。
//   - 表名与 hot 表名都取自本文件常量，**不接受外部输入**。
//
// # 本门**不**判什么
//
//   - 不判 hot 表是否存在（`sessions` 就只有一张面，缺表不算故障）。
//   - 不判行数对不对（那是保真门的事）。
//   - 不判这是本机问题还是生产问题——**门只报告现象，归因留给读数的人**。
//     §9.184 的结论是「本机 catalog 异常」，依据是**同样形状在其余五张
//     分区父表上全部正常**，不是依据本门。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionFamilySurfaces 是「父表 + _hot 孪生」的真实配对，逐条实测过存在性。
// `sessions` 不在其中：它没有 hot 孪生面。
var sessionFamilySurfaces = [][2]string{
	{"public.request_logs_bodies", "public.request_logs_bodies_hot"},
	{"public.request_logs", "public.request_logs_hot"},
	{"public.session_turns", "public.session_turns_hot"},
	{"public.session_turn_details", "public.session_turn_details_hot"},
	{"public.session_bodies", "public.session_bodies_hot"},
}

func TestSessionFamilyTwoSurfaceUnionShapeIsExecutable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 存储面可读性门未运行（非通过）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	// 确认连得上：连不上必须**报红**，不能落到下面的分类里被当成「形状可执行」。
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败 —— 门无法测量（这是环境故障，不是「形状可执行」）：%v", err)
	}

	type row struct {
		parent, hot string
		outcome     string
		detail      string
	}
	var rows []row
	for _, p := range sessionFamilySurfaces {
		// 生产同款形状：子查询内 UNION ALL 两个面，外面套 count()。
		q := fmt.Sprintf(
			`SELECT count(request_id) FROM (SELECT request_id FROM %s UNION ALL SELECT request_id FROM %s) s`,
			p[1], p[0])
		var n int64
		err := pool.QueryRow(ctx, q).Scan(&n)
		switch {
		case err == nil:
			rows = append(rows, row{p[0], p[1], "ok", fmt.Sprintf("%d 行", n)})
		default:
			// 分类必须穷举，且每一类都要说明它是什么。
			msg := err.Error()
			var outcome string
			switch {
			case strings.Contains(msg, "perminfoindex"):
				outcome = "PLAN/EXEC-INIT FAIL(invalid perminfoindex 0 in RTE with relid 0)"
			case strings.Contains(msg, "does not exist"):
				outcome = "MISSING RELATION"
			case strings.Contains(msg, "permission denied"):
				outcome = "PERMISSION"
			default:
				outcome = "UNCLASSIFIED FAIL —— 分类器没有这一类，请补而不是让它落进「可执行」"
			}
			rows = append(rows, row{p[0], p[1], outcome, firstLine(msg)})
		}
	}

	var bad []string
	for _, r := range rows {
		if r.outcome != "ok" {
			bad = append(bad, fmt.Sprintf("  %-34s + %-36s ⇒ %s\n      %s",
				r.parent, r.hot, r.outcome, r.detail))
		}
	}
	if len(bad) > 0 {
		t.Errorf("以下存储面**无法执行生产同款的子查询 UNION ALL 形状**（%d/%d 对）：\n%s\n\n"+
			"产品代码在这些表上用的正是这个形状（如 annotation_handler.go:680、\n"+
			"credential_selfcheck.go:309、validate_sessions_v2/loader.go:115 的 v1BodyQuery），\n"+
			"⇒ 读这两个面的功能在这套库上是不可用的。\n"+
			"这不是静态门能发现的故障：代码是对的，是这套库执行不了该形状。",
			len(bad), len(rows), strings.Join(bad, "\n"))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
