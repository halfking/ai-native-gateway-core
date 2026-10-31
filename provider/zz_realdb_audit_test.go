package provider

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// TestRealDBModalityFragmentVsGoOnLiveDB —— 【真库逐格对拍】：
// 把 candidateQuerySQL() 的**模态接受臂**逐格跑在真实 PG 上，与 Go 判据
// ModalityAccepts 对拍。此前这一验证只靠「静态解析 SQL 文本」，从未在真库上
// 执行过渲染后的 SQL（realdb 测试因无 DSN 静默 skip）。
//
// ★★★ **登记侧必须锚事实源（★ 2026-10-31 12h 审计轮修正）★★★
//
//	★ 原实现把登记侧写成「真库现存的 DISTINCT modality」⇒ ⇒ ★★ **分母随库内容漂移**
//	★ ⇒ ⇒ ★★ 库里没有 'embedding' / 'audio' 行 ⇒ ⇒ 这些格**一次都不比**
//	★ ⇒ ⇒ ★★ 而 registered 为空时循环零次执行，★ 仍打印「✔ … 0 格，漂移 0」并 PASS
//	★ ⇒ ⇒ ★★ ⇒ **最需要真库的时候变成空跑** ⇒ ⇒ ⇒
//	★ ⇒ ★★ **修法**：★ 登记侧 = **事实源**（models_canonical.modality 的 CHECK 值域，
//	★   由 tests/testutil.CanonicalModalityValueDomain 从迁移文件派生）
//	★   **∪** 真库实际值（★ 库里若出现事实源之外的值，★ 也要被比到）⇒ ⇒
//	★ ⇒ ★★ ⇒ 分母不再随库内容漂移 ⇒ ⇒ ⇒ ★ 空集 ⇒ ⇒ 门必须红（fail-closed）。
//
// ★★★ **诚实边界（★ 明确登记，不许含糊过去）★★★
//
//	★ ① 本门覆盖的是**接受臂**（ModalityAcceptArmsSQL）⇒ ⇒ **verdict gate
//	★   （modalityVerdictGateSQL）不在本门覆盖内** ⇒ ⇒ ★ 它**没有 Go 侧对照实现**
//	★   ⇒ ⇒ ⇒ ★ 真库跑它只能证明「不报错」，不能证明「语义对」⇒ ⇒ ⇒ ★ 已列入
//	★   12h 审计待修正清单（★ 不在本门假装覆盖）。
//	★ ② 无 DSN 时本门 skip ⇒ ⇒ ★★ 非 verbose 模式下 skip 不打印 ⇒ ⇒ ★★ CI 里
//	★   「skip」与「通过」在日志上**不可区分** ⇒ ⇒ ⇒ ★ 已列入 12h 审计待修正清单。
func TestRealDBModalityFragmentVsGoOnLiveDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set ⇒ ★ 本门是【真库】腿 ⇒ 无 DSN 只能 skip")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── 登记侧：事实源（CHECK 值域）∪ 真库实测值 ──
	// ★ 事实源读不到 ⇒ ⇒ 门必须红（fail-closed）⇒ ⇒ ★★ 不许退回「拿库里有什么算什么」。
	domain, err := testutil.CanonicalModalityValueDomain("..")
	if err != nil {
		t.Fatalf("★ 取模态事实源失败（models_canonical.modality 的 CHECK 值域）：%v", err)
	}
	if len(domain) == 0 {
		t.Fatalf("★ 模态事实源为空 ⇒ ⇒ ⇒ 【门失效】⇒ ⇒ 不许退回「拿库里有什么算什么」")
	}

	rows, err := pool.Query(ctx, "SELECT DISTINCT COALESCE(modality,'text') FROM models_canonical ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	var live []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		live = append(live, m)
	}
	// ★★★ **rows.Err() 必须查**：★★★ ★ 不查 ⇒ ⇒ ctx 超时 / 连接中断时**静默截断分母**
	// ★★★ ⇒ ⇒ 而分母少一半仍可能全绿 ⇒ ⇒ ⇒ ★★ ⇒ 只打印一个「少算了」是查不出来的。
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("★ 读登记侧时迭代出错（★ 不查 rows.Err() ⇒ 分母会被静默截断）：%v", err)
	}
	rows.Close()

	registered := mergeModalitySet(domain, live)
	if len(registered) == 0 {
		t.Fatalf("★ 登记侧为空 ⇒ ⇒ ⇒ 【零格假绿】⇒ ⇒ 门必须红")
	}

	// 请求侧：判据词表 ∪ 一个未知值
	requested := append([]string{}, ModalityRequestModalities...)
	requested = append(requested, "document", "")

	// 把渲染后的接受臂整体作为布尔表达式求值（单行标量，不需要真表）。
	// COALESCE(mc.modality, 'text') 替换成参数 _reg；占位符 $3 替换成 $1。
	exprTemplate := strings.ReplaceAll(ModalityAcceptArmsSQL("$3"),
		"COALESCE(mc.modality, 'text')", "_reg")
	exprTemplate = strings.ReplaceAll(exprTemplate, "$3", "$1")

	drift := 0
	cells := 0
	for _, req := range requested {
		for _, reg := range registered {
			// ★ 参数绑定：$1 = 请求模态（exprTemplate 里的占位），$2 = 登记模态（_reg）。
			q := "SELECT COALESCE($2,'text'), (" + strings.TrimSpace(exprTemplate) + ") FROM (SELECT $2 AS _reg) t"
			var regN string
			var sqlV bool
			if err := pool.QueryRow(ctx, q, req, reg).Scan(&regN, &sqlV); err != nil {
				t.Fatalf("SQL 求值失败 (req=%s reg=%s): %v\nSQL: %s", req, reg, err, q)
			}
			cells++
			goV := ModalityAccepts(reg, req)
			if sqlV != goV {
				drift++
				t.Errorf("★ 真库对拍漂移: registered=%q requested=%q SQL=%v Go=%v", reg, req, sqlV, goV)
			}
		}
	}
	// ★★ 零格 ⇒ ⇒ 必须红：★★ 循环零次执行时上面的漂移计数恒为 0 ⇒ ⇒ ⇒
	// ★★ **「一格没比」和「比了且全对」在旧实现里是同一个输出** ⇒ ⇒ ⇒ ★ 这里补上。
	if cells == 0 {
		t.Fatalf("★ 对拍格数为 0 ⇒ ⇒ ⇒ 【零格假绿】⇒ ⇒ 登记侧 %v × 请求侧 %v",
			registered, requested)
	}
	if want := len(registered) * len(requested); cells != want {
		t.Fatalf("★ 对拍格数 %d ≠ 预期 %d（%d registered × %d requested）⇒ ⇒ 分母被截断了",
			cells, want, len(registered), len(requested))
	}
	fmt.Printf("✔ 真库逐格对拍: %d registered（含事实源 %d 个 + 真库实测 %d 个）× %d requested = %d 格，漂移 %d\n",
		len(registered), len(domain), len(live), len(requested), cells, drift)
}

// mergeModalitySet 求两个模态集合的并集（★ 保序、去重）⇒ ⇒ 事实源 ∪ 真库实测。
func mergeModalitySet(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
