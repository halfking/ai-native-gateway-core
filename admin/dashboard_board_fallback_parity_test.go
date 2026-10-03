// dashboard_board_fallback_口径_test.go —— 钉住回退路径与 minute 路径的口径关系
//
// ## 这条测试在挡什么（它挡的是「一个未来的误改」，不是当前缺陷）
//
// 看板有两条取数路径：
//
//	· minute 路径：request_stats_minute / request_stats_dim_minute（聚合表）
//	· 回退路径：  request_logs_with_current_month_without_customer_id（明细表）
//
// 第 8.2 节判定「回退返回真实聚合值、不会把降级误报成零」是对的，
// 但那一条只核了**值是不是真的**，没核**两条路径的口径是否一致**。
// §7/T4 的教训正是这个：两个数据源要核两件事，
// 「聚合值一致」与「覆盖范围一致」是两条独立命题。
//
// ## 2026-10-03 实测结论（本地 5432 / llm_gateway，同一 UTC 日界窗口）
//
//	all_status 77,117 / whitelisted 76,910 / excluded 207 / minute_requests 70,266
//
// ⇒ 两条路径对 `request_status` 的处理**确实不同**，差 0.27%：
//
//	· 回退路径有白名单 `IN ('success','failure','rate_limited')`
//	· minute 路径对 Requests **无条件 +1**（minute_entry.go:103），
//	  只在 SuccessCount/FailureCount 上分档
//
// 被排除的 207 条是 `in_progress`（进行中）与空 status（状态未落）。
// 排除它们是**正确**的：进行中的请求尚未产生终态结论，
// 且长请求的 ts 落在过去但仍在跑，计入会让「已完成请求数」虚高。
//
// ## 为什么这不是缺陷，而是要钉住的契约
//
// 差异方向明确、量级可测、无正确性风险。真正的风险是**将来有人
// 「顺手统一」**——比如给 minute 路径也加个白名单，或把回退的白名单放宽：
// 那会把两件不同的事混成一件（in_progress 与 status 未落都不是终态）。
//
// 所以这条测试不禁止差异，而是要求：**差异必须仍然存在且仍然只针对
// 非终态 status**。一旦有人把白名单改成「全部包含」或「只含 success/failure」，
// 门会红，因为那已经不是原来那个语义了。
//
// ## 口径清单（缺一条即红，防「漏了一个过滤条件」只靠肉眼）
//
// 回退路径每个取数函数都必须带：
//
//	① ts/bucket 窗口
//	② 租户过滤（tenantID 非空时）
//	③ request_status 终态白名单
package admin

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 终态白名单。in_progress / 空 / 其它一律排除。
var boardTerminalStatusRe = regexp.MustCompile(`request_status IN \('success', 'failure', 'rate_limited'\)`)

// minute 路径的取数函数：它们读聚合表，没有 request_status 列可过滤。
//
// 带文件名，因为它们**不都在** dashboard_board_queries.go 里
// （queryErrorDrillMinute 在 dashboard_board_aux.go）。
// 第一版把 5 个函数名写成一个不带文件名的清单、却只在一个文件里找，
// 门立刻报「找不到 queryErrorDrillMinute」——
// 这正是「表项失效即红」该有的表现：清单与现实不符时必须报，而不是跳过。
var boardMinuteQueryFuncs = []struct{ file, decl string }{
	{"dashboard_board_queries.go", "func (h *Handler) queryBoardSummary("},
	{"dashboard_board_queries.go", "func (h *Handler) queryBoardPies("},
	{"dashboard_board_queries.go", "func (h *Handler) queryBoardTrends("},
	{"dashboard_board_queries.go", "func (h *Handler) queryOverviewCountsMinute("},
	{"dashboard_board_aux.go", "func (h *Handler) queryErrorDrillMinute("},
}

// goFuncBody 截出从声明行开始的函数体（到下一个顶层 func 声明为止）。
func goFuncBody(code, decl string) string {
	i := strings.Index(code, decl)
	if i < 0 {
		return ""
	}
	body := code[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	return body
}

func TestBoardFallbackPathsKeepTerminalStatusWhitelist(t *testing.T) {
	for _, file := range []string{"dashboard_board_fallback.go", "dashboard_board_queries.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		code := stripGoCommentsKeepLines(string(raw))

		// 找出所有直接查 request_logs 的函数（走明细表的那些）。
		// 判据用「函数体里出现 boardLogsWhere」而不是罗列函数名：
		// 新增一个走明细的取数函数时，这条判据自动把它纳入检查，
		// 而名单式的写法会静默漏掉新函数 —— 那正是本轮反复遇到的
		// 「判据的元素集合要先证明覆盖全集」。
		forEachFuncBody(code, func(decl, body string) {
			if !strings.Contains(body, "boardLogsWhere(") {
				return
			}
			if !boardTerminalStatusRe.MatchString(body) {
				t.Errorf("%s 的 %s 走 request_logs 明细但没有终态白名单 ——\n"+
					"进行中/状态未落的请求会被计入「已完成请求数」。\n"+
					"白名单必须是 %s",
					file, decl, boardTerminalStatusRe.String())
			}
		})
	}
}

// TestBoardMinutePathHasNoRequestStatusFilter 钉住「minute 路径没有 status 过滤」
// 这个**当前事实**。
//
// 为什么钉一个「什么都没有」的现状：它让 §19 实测到的 0.27% 口径差
// 变成一条可复核的契约而不是一句散话。哪天有人给 minute 路径加上白名单，
// 意味着聚合表开始丢非终态请求 —— 那需要显式决策 + 改这里，而不是顺手改。
func TestBoardMinutePathHasNoRequestStatusFilter(t *testing.T) {
	// 按文件分组读一次，避免每个函数重复 IO。
	cache := map[string]string{}
	load := func(file string) string {
		if code, ok := cache[file]; ok {
			return code
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		code := stripGoCommentsKeepLines(string(raw))
		cache[file] = code
		return code
	}

	for _, fn := range boardMinuteQueryFuncs {
		body := goFuncBody(load(fn.file), fn.decl)
		if body == "" {
			t.Errorf("在 %s 里找不到 %s —— 看板 minute 路径的函数清单需要更新",
				fn.file, fn.decl)
			continue
		}
		if strings.Contains(body, "request_status") {
			t.Errorf("%s 的 %s 里出现了 request_status 过滤。\n"+
				"minute 路径读的是聚合表 request_stats_minute，聚合表的 requests 计数\n"+
				"在写入时就是无条件 +1（minute_entry.go）。这里加过滤不会生效，\n"+
				"只会让人误以为两条路径口径已统一。若确实要改语义，请先更新本文件\n"+
				"第八节实测的 0.27 个百分点差值结论。", fn.file, fn.decl)
		}
	}
}

// TestBoardTenantFilterParity 核租户过滤在两条路径上等价。
//
// 两边的写法不同（boardMinuteWhere 用裸列名 `tenant_id = $N`、
// boardLogsWhere 用 `alias.tenant_id = $3`），所以不能比完整字符串。
//
// ⚠ 第一版只查函数体里有没有出现过 `tenant_id = $` 字样，
// 变异「把 where 追加那一行删掉、只留 args = append(...)」**逃过了** ——
// 参数还在、谓词没了，而 args 与 where 一旦不同步，pgx 还会直接报错。
// 所以判据要查的是「**where 被追加了**」这件事：要求出现 `where +=`
// 且该行含 tenant_id。只查字样等于查「这个函数提到过租户」。
func TestBoardTenantFilterParity(t *testing.T) {
	raw, err := os.ReadFile("board_time_range.go")
	if err != nil {
		t.Fatalf("read board_time_range.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))

	cases := []struct{ fn, why string }{
		{"func boardMinuteWhere(", "minute 聚合路径"},
		{"func boardLogsWhere(", "request_logs 明细路径"},
	}
	for _, c := range cases {
		body := goFuncBody(code, c.fn)
		if body == "" {
			t.Errorf("找不到 %s", c.fn)
			continue
		}
		if !strings.Contains(body, `tenantID != ""`) {
			t.Errorf("%s 没有「租户非空时加过滤」的分支 —— "+
				"跨租户时会把所有租户的数据混进一个看板（%s）", c.fn, c.why)
			continue
		}
		appended := false
		for _, ln := range strings.Split(body, "\n") {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "where +=") && strings.Contains(t, "tenant_id") {
				appended = true
				break
			}
		}
		if !appended {
			t.Errorf("%s 里没有把 tenant 条件**追加进 where**（只有 args 追加也算缺）——\n"+
				"参数传了但谓词没进 SQL，跨租户看板会把所有租户混在一起（%s）",
				c.fn, c.why)
		}
	}
}

// forEachFuncBody 遍历 code 里的每个顶层函数声明。
//
// 为什么要自己写而不是用 go/ast：本文件已依赖 stripGoCommentsKeepLines
// 这套与主判据同源的去注释规则；混用两套解析会让「门扫过什么」变得不可核。
// 这里只需要函数声明的起始位置，够用且不引入第二个解析器。
//
// ⚠ 第一版把 body 算成「从当前行到下一个 func」却没有推进游标，
// 于是第 2 个函数拿到的 body 起点是第 1 个函数的**声明行**——
// 判据会看到不属于它的代码。用一个必然不成立的断言验出来：
// 一个只存在于 fnA 的白名单串，会被报成「fnB 缺白名单」。
func forEachFuncBody(code string, fn func(decl, body string)) {
	lines := strings.Split(code, "\n")
	// 先记下每个 func 的起始偏移，保证 body 从**它自己**那行开始。
	var offsets []int
	off := 0
	for _, ln := range lines {
		if strings.HasPrefix(ln, "func ") {
			offsets = append(offsets, off)
		}
		off += len(ln) + 1
	}
	for i, start := range offsets {
		end := len(code)
		if i+1 < len(offsets) {
			end = offsets[i+1]
		}
		body := code[start:end]
		fn(strings.TrimSpace(lines[strings.Count(code[:start], "\n")]), body)
	}
}
