package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// V1 臂视图（§9.258）—— 退役清单真正要的那个类。
//
// # 为什么需要第四个类
//
// 已有两类的判据都是**关系寿命**：
//
//	reads-v1   : 这个关系名在 DROP 时会不会消失？   （v1Tables 里的 4 张基表：会）
//	canonical  : 不会消失。
//
// 而 `request_logs_with_current_month` / `request_logs_bodies_with_current_month
// 属于 canonical —— 视图本身在退役后**还在**。但它今天的**体**里有一条活的
// v1 臂，所以 **DROP 之后它读到的行集会变小**。
//
// ⇒ 退役要问的第三个问题「我读到的行里有多少来自 v1」，前两类都答不了。
// §9.257 的实测把这一点钉死了：把解析率从 73% 提到 90%（多解析出 39 处），
// **v1 桶一处没多** —— 因为 26 处读的是视图，而视图不在 v1Tables 里。
//
// # 这个集合是**推导**出来的，不是手写名单
//
// 手写 `[]string{"request_logs_with_current_month", …}` 就是第二份真相源，
// 而 `resolve.go` 的文件头已经记过那个教训（「多余的真相源不是防御，
// 是看起来在防、实际测不出的缺口」）。所以这里读仓库自己的视图 DDL：
// 凡是 `FROM/JOIN` 到 4 张 v1 基表的视图，就算有一条 v1 臂。
//
// 加一个视图、改一个视图的体，这个集合自动跟着变。
//
// # ⚠ 已知的推导边界（读之前必须知道）
//
// `sql/objects/views/request_logs_with_current_month.sql` 的**文件头自己写着**
// 它不是部署形态的定义，而是 v1 回退体（两条臂）；部署形态由
// `db/request_logs_view_schema.go` 的 composer 生成，是**三条臂**
// （session_turns(_hot) 投影 ∪ v1 臂）。
//
// ⇒ 本函数**答不了「v1 臂占该视图多大比例」**，只能答「有没有 v1 臂」。
//
//	这个区别对退役仍然够用（两种情况下都会少行），但**不能**拿它推断
//	「切到会话源会少多少行」——那个数字要真库量，且已经量过：
//	bodies 视图 has_session_arm=0（纯 v1），turns 视图是三臂。
var createViewRE = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:MATERIALIZED\s+)?VIEW\s+(?:public\.)?([a-z_][a-z0-9_]*)`)

// viewSourceFiles 是推导 v1 臂视图的**两个**来源。
//
// ⚠ §9.260：**只看 `sql/objects/views/` 是不完整的。**
// 那 54 个 DDL 文件里只有 `request_logs_with_current_month` 与
// `request_logs_bodies_with_current_month`；而真库里 `pg_class` 有 **4 个**
// `request_logs*_with_current_month*` 视图，**全部含 v1 臂**。
// 缺的两个 —— `…_without_customer_id` 与 `…_without_request_class_due_at` ——
// **没有仓库 DDL 文件**：它们由 composer 在**运行时**建
// （`db/request_logs_view_schema.go:128` / `:140`），
// 仓库里只在测试里 dump 过。
//
// ⇒ 后果不是「少报两个视图」，而是**读那两个视图的文件被判成 canonical**：
//
//	`admin/attempt_quality_api.go` · `admin/usage_trend_series.go` ·
//	`admin/auto_route_correlations.go` —— **三个文件，方向是「把有风险的报成安全」**。
//
// ★ 教训：推导源要挑「**谁在运行时真正定义它**」，不是「仓库里恰好有文件的那个」。
var viewSourceFiles = []string{
	"sql/objects/views",              // 目录：逐个 .sql
	"db/request_logs_view_schema.go", // 单文件：composer 里的 CREATE VIEW
}

// viewsWithV1Arm 返回「体里含 v1 臂（直接或传递）」的视图名集合。
//
// 找不到任何来源时返回空集合：调用方因此**退回旧行为**（只按基表判 v1），
// 那与 §9.257 之前完全一致，不是错误答案。宁可少报也不猜。
func viewsWithV1Arm(root string) map[string]bool {
	edges := map[string]map[string]bool{} // 视图 → 它引用的关系
	addView := func(name, body string) {
		body = sqlLineCommentRE.ReplaceAllString(body, " ")
		refs := map[string]bool{}
		for _, m := range fromRelationRE.FindAllStringSubmatch(body, -1) {
			refs[firstRelationToken(m[1])] = true
		}
		if len(refs) > 0 {
			if edges[name] == nil {
				edges[name] = map[string]bool{}
			}
			for r := range refs {
				edges[name][r] = true
			}
		}
	}

	// ① sql/objects/views/*.sql
	if entries, err := os.ReadDir(filepath.Join(root, "sql", "objects", "views")); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".sql") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, "sql", "objects", "views", name))
			if err != nil {
				continue
			}
			m := createViewRE.FindSubmatch(b)
			if m == nil {
				continue
			}
			addView(string(m[1]), string(b))
		}
	}

	// ② composer：Go 源码里的 CREATE VIEW 字符串
	if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(viewSourceFiles[1]))); err == nil {
		src := string(b)
		locs := createViewRE.FindAllStringSubmatchIndex(src, -1)
		for i, loc := range locs {
			end := len(src)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			addView(string(src[loc[2]:loc[3]]), src[loc[0]:end])
		}
	}

	// ★ 传递闭包：一个视图可能只引用**另一个** v1 臂视图
	// （`…_without_request_class_due_at` 就是从 `…_without_customer_id` 转包的），
	// 只看「直接 FROM 了基表」会漏掉整条包装链。
	out := map[string]bool{}
	for v := range edges {
		seen := map[string]bool{}
		stack := []string{v}
		hit := false
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[cur] {
				continue
			}
			seen[cur] = true
			if v1Tables[cur] {
				hit = true
				break
			}
			for r := range edges[cur] {
				stack = append(stack, r)
			}
		}
		if hit {
			out[v] = true
		}
	}
	return out
}

// v1ArmViewNames 只用于给报告与门输出排序（确定性）。
func v1ArmViewNames(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
