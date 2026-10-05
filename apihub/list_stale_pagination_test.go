package apihub

// listStale 的分页门（2026-10-06，runbook §10.34）
//
// 被测的那条性质只有一句：**stale 数超过一页时，调用方必须拿到全集。**
//
// ★ 为什么这道门值得存在：截断**不报错、不留痕**。
//   bg.AssetHealthProbe 拿到 1000 行就把它们标成 Degraded，剩下的
//   永远停在 Healthy —— 从外面看一切正常，只是有几条资产坏了没人知道。
//   而「拿到了 1000 行」这件事在任何日志、任何监控面板上都不成立。
//
// 门分两层，分工要说清楚：
//   · TestListStaleSQLIsPaged —— 源码结构门，**不需要真库**，抓的是
//     「有人把 OFFSET / 翻页循环改回去」这个具体回归。
//   · TestListStaleRealDB*（list_stale_realdb_test.go）—— 真库门，
//     造 1234 行断言真的拿到 1234，是**唯一**能区分「截断」与「完整」的证据。
//   两者都需要：真库门在没设 TEST_DATABASE_URL 时会 Skip，
//   而「Skip」在 CI 输出里与「通过」肉眼难分 —— 结构门保证那一刻仍有东西在守。

import (
	"os"
	"strings"
	"testing"
)

func TestListStaleSQLIsPaged(t *testing.T) {
	// ① SQL 本身必须有 OFFSET。
	//
	// ★ 这一条是 §10.27 同一个错误的复发防护：那里 Limit 500 因为没有
	//   OFFSET 而同时成了「总量上限」，这里 Limit 1000 当初也是。
	//   页大小必须配游标，否则 LIMIT 就是一个静默的天花板。
	for _, want := range []string{"LIMIT $3", "OFFSET $4"} {
		if !strings.Contains(listStaleSQL, want) {
			t.Errorf("listStaleSQL 里没有 %q。\n"+
				"★ 没有 OFFSET，LIMIT 就同时成了**总量上限**：超出部分既不报错也不留痕，"+
				"调用方永远拿不到全集。（§10.27 在 listAssetsSQL 上犯过同一个错。）\n\n%s",
				want, listStaleSQL)
		}
	}

	// ② 页大小必须是常量，且不能被当成总量上限传进 SQL。
	if listStalePageSize <= 0 {
		t.Fatalf("listStalePageSize = %d，必须为正", listStalePageSize)
	}

	// ③ 函数体里必须真的有翻页循环，而不是只把 OFFSET 参数填成 0。
	//
	// ★ 只查 SQL 会漏掉这个形态：把 offset 永远传 0，SQL 里有 OFFSET，
	//   门 ①② 全绿，但行为与改动前完全一样（每页都取前 1000）。
	//   这是「结构对了但行为没变」——只有看调用点才能抓到。
	src, err := os.ReadFile("pg_store.go")
	if err != nil {
		t.Fatalf("读 pg_store.go 失败: %v", err)
	}
	body := funcBody(string(src), "func (s *pgStore) ListStale(")
	if body == "" {
		t.Fatal("在 pg_store.go 里找不到 ListStale 的函数体")
	}
	// 循环：页计数必须递增，且游标由页计数**推导**出来。
	// 写成 `for offset := 0; ; offset += listStalePageSize` 也可以，
	// 这里只要求「有递增的计数器」与「游标由它算出」两件事分别存在。
	if !strings.Contains(body, "for page := 0; ; page++") {
		t.Errorf("ListStale 里没有递增的翻页循环（找不到 `for page := 0; ; page++`）。\n"+
			"★ OFFSET 参数存在不等于真的翻了页 —— 永远传 0 的话行为与改动前一致。\n%s", body)
	}
	if !strings.Contains(body, "offset := page * listStalePageSize") {
		t.Errorf("游标没有由页计数推导（找不到 `offset := page * listStalePageSize`）。\n"+
			"★ 两者必须绑定：任何一处写死 offset 或 page，循环就会永不退出。\n%s", body)
	}
	// 终止条件
	if !strings.Contains(body, "got < listStalePageSize") {
		t.Errorf("ListStale 的终止条件应为「不满一页」（got < listStalePageSize）。\n"+
			"★ 写成 got == 0 会让「最后一页刚好是 0 行」成为唯一的收尾路径，\n"+
			"  而 stale 数恰好等于页大小整数倍时会多查一轮空页。\n%s", body)
	}
	// SQL 调用必须把游标传下去
	if !strings.Contains(body, "listStaleSQL, tenantID, seconds, listStalePageSize, offset") {
		t.Errorf("调用 listStaleSQL 时没有把游标传下去。\n%s", body)
	}
	// ④ 保险丝必须存在，且触顶时**返回错误**而不是返回部分结果。
	//
	// ★ 这条不是「防御性洁癖」：M23 变异（游标钉死为 0）会让
	//   got == pageSize 永远成立 ⇒ 循环永不退出 ⇒ 每 60 秒一轮的死循环狂查库。
	//   静默的无限循环比截断更糟 —— 截断只是少看几条，无限循环是把库打满。
	//   保险丝的**失败语义**同样重要：返回错误，而不是返回已取到的那部分，
	//   否则它就退化成了一个更靠后的静默截断。
	if !strings.Contains(body, "page >= listStaleMaxPages") {
		t.Errorf("ListStale 里没有翻页保险丝（找不到 `page >= listStaleMaxPages`）。\n%s", body)
	}
	if !strings.Contains(body, "return fmt.Errorf(") {
		t.Errorf("保险丝触顶时应 return fmt.Errorf（报错），而不是静默返回部分结果。\n%s", body)
	}
	if listStaleMaxPages <= 1 {
		t.Errorf("listStaleMaxPages = %d，必须 > 1（单页数据不该触发保险丝）", listStaleMaxPages)
	}
}

// funcBody 从源码里截出某个函数的函数体（按大括号配平）。
func funcBody(src, sig string) string {
	i := strings.Index(src, sig)
	if i < 0 {
		return ""
	}
	depth, started := 0, false
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return src[i : j+1]
			}
		}
	}
	return src[i:]
}
