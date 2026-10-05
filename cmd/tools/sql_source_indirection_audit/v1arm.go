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

// viewsWithV1Arm 扫 sql/objects/views/ 下的 DDL，返回「体里含 v1 臂」的视图名集合。
//
// 找不到目录 / 目录为空时返回空集合：调用方因此**退回旧行为**（只按基表判 v1），
// 那与今天完全一致，不是错误答案。宁可少报也不猜。
func viewsWithV1Arm(root string) map[string]bool {
	dir := filepath.Join(root, "sql", "objects", "views")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		m := createViewRE.FindSubmatch(b)
		if m == nil {
			continue
		}
		view := string(m[1])
		// 先剥 SQL 行注释：注释里出现 FROM request_logs 不构成 v1 臂。
		body := sqlLineCommentRE.ReplaceAllString(string(b), " ")
		for _, fm := range fromRelationRE.FindAllStringSubmatch(body, -1) {
			if v1Tables[firstRelationToken(fm[1])] {
				out[view] = true
				break
			}
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
