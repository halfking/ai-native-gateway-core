//go:build s4audit

package admin

// s4_session_family_unservable_realdb_test.go — 2026-10-04（审计 §9.192）。
//
// S4 的**硬前置之一**：`db.RetirementUnservableColumns` 里登记的列，
// session 族必须已经能供给。登记在那张表里不等于供给存在——§9.161 测的是
// 填充率，结论是「0%」；这道门把那个结论变成**会红的前置条件**。
//
// 跑法：
//
//	go test -tags s4audit ./admin/ -run TestS4SessionFamilyCanServeUnservableColumns
//
// # 为什么按 build tag 分开（沿用本仓既有先例）
//
// `request_logs_stop_write_coverage_gate_test.go` 的理由原样适用：这是
// 「把一件事做完」的闸，不是「防止事情变坏」的守卫。放进常跑测试会让
// `go test ./...` 长期红，挡住所有人，却不会让缺口被填上。
//
// # 量的是**投影后的视图**，不是源表
//
// 两条理由，缺一不可：
//  1. 判据要回答的是「视图读者会拿到什么」，不是「表里存了什么」。
//     两者可以不同——`outbound_model` 就是典型（投影第 8 项是 `t.model`）。
//  2. 视图按 `request_id` 去重（v1 臂有 `NOT EXISTS(session_turns*)`），
//     所以「session 臂行数」== 业务行数，而 v1 臂行数 == 未被镜像的行数。
//     两侧数字必须**分别**量，取补集会把「两边都空」算成缺口。
//
// # 零行不许算绿
//
// 库里没有近期流量时，0/0 的比值没有意义。此时**指名 Skip**并说明原因，
// 而不是让 `0 == 0` 变成一条通过——那是本项目已经栽过一次的形态
// （§9.184：普查脚本的 `PGPASSWORD` 从未赋值，12 次连接失败全被默认判 OK）。
//
// # 本门**当前是红的**，且红因已核实
//
// 真库 7 天实测（2026-10-04）：
//
//	session_turns ∪ session_turns_hot：31,274 轮，is_final_success 非空 **0**
//	request_logs：                 73,264 行，is_final_success 非空 **9,614**
//
// 写侧已定位：`claimSessionFinalSuccess`
// （domains/hooks/observability/telemetry/client.go:2711 起）只 UPDATE
// `request_logs_hot`，session 族**没有任何等价写方**。读路已被
// `session_final_success_readpath_realdb_test.go` 的阳性对照证明是好的。
// ⇒ 这是**写侧缺口**，补它属生产行为改动（决策表 D28），本轮不改。

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// minSessionArmRows 是「可以下结论」的最小样本量。低于它就 Skip。
//
// 取 500 而不是 1：单行样本上「0 列非空」与「这一列真的没写」不可区分。
const minSessionArmRows = 500

func TestS4SessionFamilyCanServeUnservableColumns(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database unservable-column gate")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}

	cols := append([]string(nil), dbpkg.RetirementUnservableColumns...)
	sort.Strings(cols)

	// 窗口取近 24h：审计 §9.36 确立的纪律——710 补位列的判级必须量**近期**
	// 填充率，全历史均值会把同一列判出相反结论。
	const window = 24 * time.Hour

	// 组装聚合：每个列两侧各一个 count。
	sel := make([]string, 0, len(cols)*2+2)
	sel = append(sel,
		"count(*) FILTER (WHERE id IS NULL) AS sess_rows",
		"count(*) FILTER (WHERE id IS NOT NULL) AS v1_rows")
	for _, c := range cols {
		if !validIdent(c) {
			t.Fatalf("登记表里的列名 %q 不是合法标识符 —— 本门拒绝拼接不可信标识符", c)
		}
		// 裸拼而非 %q：Go 的 %[1]q 产出的是 Go 字符串字面量（双引号），
		// 那是**字符串常量**不是标识符，PG 直接报 42601（第一版踩到）。
		// 安全性由上面的 validIdent 保证——它已把字符集限死，
		// 所以这里不需要引号；加了引号反而是「看起来更安全、实际是语法错」。
		sel = append(sel,
			fmt.Sprintf("count(*) FILTER (WHERE id IS NULL AND %[1]s IS NOT NULL) AS s_%[1]s", c),
			fmt.Sprintf("count(*) FILTER (WHERE id IS NOT NULL AND %[1]s IS NOT NULL) AS v_%[1]s", c))
	}
	query := fmt.Sprintf(
		`SELECT %s FROM public.request_logs_with_current_month WHERE ts >= now() - $1::interval`,
		strings.Join(sel, ", "))

	var sessRows, v1Rows int
	scan := []any{&sessRows, &v1Rows}
	// 不能对 map 下标取地址（scan 要的是 *int），所以先落到切片里再归档。
	sessVals := make([]int, len(cols))
	v1Vals := make([]int, len(cols))
	for i := range cols {
		scan = append(scan, &sessVals[i], &v1Vals[i])
	}
	if err := pool.QueryRow(ctx, query, window.String()).Scan(scan...); err != nil {
		t.Fatalf("量 session 臂填充率失败: %v", err)
	}
	sessNN := map[string]int{}
	v1NN := map[string]int{}
	for i, c := range cols {
		sessNN[c] = sessVals[i]
		v1NN[c] = v1Vals[i]
	}

	t.Logf("窗口 %s：视图 session 臂 %d 行 / v1 臂 %d 行", window, sessRows, v1Rows)
	for _, c := range cols {
		t.Logf("  %-20s session=%d  v1=%d", c, sessNN[c], v1NN[c])
	}

	if sessRows < minSessionArmRows {
		// ★ 零样本必须**指名 Skip**，绝不能因为「0/0 看起来相等」而通过。
		t.Skipf("近 %s 的 session 臂只有 %d 行（< %d）：本门不下结论。"+
			"**这不是通过**——它只是没有样本。若要真的判定 S4 前置，请在有近期流量的库上跑。",
			window, sessRows, minSessionArmRows)
	}

	var unserved []string
	for _, c := range cols {
		if sessNN[c] == 0 {
			unserved = append(unserved, fmt.Sprintf("%s（session=%d, v1=%d）", c, sessNN[c], v1NN[c]))
		}
	}
	if len(unserved) == 0 {
		return
	}
	t.Fatalf("S4 硬前置未达成：%s\n"+
		"这些列在 session 臂上**一行都供不出**，而 v1 臂仍有值 —— 停写后读它们的\n"+
		"读点会拿到「有行、该列为空」或直接查不到，且没有任何错误信号。\n"+
		"已定位：is_final_success 的写侧只 UPDATE request_logs_hot，session 族无等价写方\n"+
		"（读路已由 session_final_success_readpath_realdb_test.go 的阳性对照证明是好的）。\n"+
		"处置属生产行为改动 —— 决策表 D28，本轮只刻画不改行为。",
		strings.Join(unserved, "\n  "))
}

// validIdent 只允许小写字母、数字与下划线，且不以数字开头。
//
// 用途是**阻止把登记表内容拼进 SQL**。这张表目前是硬编码常量，但它是
// 「可被测量推翻的清单」，将来可能由迁移或工具生成；那时拼接就成了注入面。
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	// 首位单独判：把「数字」放进下面的通用分支会让 "1abc" 通过
	//（第一版就这么写的——首个 case 先命中，i==0 那条永远到不了）。
	// 这正是「判据的分支顺序本身是判据的一部分」的又一例。
	if r := rune(s[0]); !((r >= 'a' && r <= 'z') || r == '_') {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}
