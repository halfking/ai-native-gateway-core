package admin

// R172 · 会话族轮次读点「两面齐全」的**正面证明**
//
// # 为什么这道门在 admin 侧
//
// `TestNoBareParentSessionFamilyRead`（同包）把
// `internal/sessionfamily/turns_reads.go` 判成「未登记的裸父表读法」，
// 因为它的判据**单位是一条字符串字面量**：本文件的父表名出现在**两个不同的
// 字面量**里（父表腿一个、反连接的 `archived` 引用一个），
// 而 hot 腿在**第三个**字面量里 ⇒ 「同一条字面量里读过 hot 孪生面」
// 这个豁免条件不成立 ⇒ 逐字面量判据看不见「两条面同处一条 SQL」。
//
// ⇒ 该文件因此按仓内既有先例（`db/request_logs_view_schema.go`、
// `admin/turns_filter_options.go`）**具名登记**，并由**本文件**提供正面证明：
// **产物 SQL 里 hot 腿与父表腿同处一条 `UNION ALL` 语句**。
//
// ★ 这道门的作用是让登记条目**不被一句空话支撑**：登记理由说「两面齐全」，
//   那就把两面齐全钉成可执行断言。
// ★ 反向的诚实性由同包的 `TestSessionFamilyBareParentRegistryIsHonest` 保证
//   （登记了却不命中 ⇒ 报红）。

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/sessionfamily"
)

// mentionsRelationIn 报告 `rel` 是否在 `flat` 里作为**完整关系名**出现
// （后面不接标识符字符）。
//
// ★ 本文件**自带**这个判据、不复用 admin 包里那个 `mentionsRelation`
//   （它在 `//go:build !integration` 的文件里）⇒ ★★ 本文件因此**不带 build tag**
//   ⇒ `default` 与 `-tags=integration` **两个构建都跑**这道正面证明。
func mentionsRelationIn(flat, rel string) bool {
	for i := 0; ; {
		j := strings.Index(flat[i:], rel)
		if j < 0 {
			return false
		}
		abs := i + j
		after := abs + len(rel)
		if after >= len(flat) || !isIdentCharIn(flat[after]) {
			return true
		}
		i = abs + len(rel)
	}
}

// isIdentCharIn 判断字节是否是标识符字符（字母/数字/下划线）。
func isIdentCharIn(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// TestSessionFamilyTurnsReadsBothFaces 断言三处轮次读点的产物 SQL
// **确实同时读了 hot 面与父表面**，且父表面是 `UNION ALL` 的合法分支。
func TestSessionFamilyTurnsReadsBothFaces(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"冲突读回", sessionfamily.TurnConflictLookupSQL("$1", "$2")},
		{"GetTurn", sessionfamily.TurnByRequestSQL("$1", "$2")},
		{"ListTurns", sessionfamily.TurnsBySessionSQL("$1", "$2", "$3")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flat := strings.Join(strings.Fields(tc.sql), " ")

			// ① hot 面必须在（父表落后 hot 约 8.7 小时 ⇒ 只读父表会漏最新那批）。
			if !mentionsRelationIn(flat, "public.session_turns_hot") {
				t.Errorf("产物里没有 hot 臂 ⇒ 只读父表会漏掉最近数小时的数据：\n%s", tc.sql)
			}
			// ② 父表面必须在（已 promote 的冷行只在父表）。
			if !mentionsRelationIn(flat, "public.session_turns") {
				t.Errorf("产物里没有父表臂 ⇒ 会漏掉已 promote 的冷行：\n%s", tc.sql)
			}
			// ③ 两条面必须**同处一条语句**（UNION ALL），不能是两条独立语句。
			if !strings.Contains(flat, "UNION ALL") {
				t.Errorf("产物里没有 UNION ALL ⇒ 两条面不在同一条语句里：\n%s", tc.sql)
			}
			// ④ 反连接对父表的引用必须**只过滤不投影**（SELECT 1）——
			//    否则父表就不是「union 的合法分支」而是第二个数据来源。
			if !strings.Contains(flat, "NOT EXISTS (SELECT 1 FROM public.session_turns archived") {
				t.Errorf("反连接不是「只过滤不投影」的形状：\n%s", tc.sql)
			}
			// ⑤ ★ 逐字面量判据看不见的那条（登记理由的根据）：
			//    hot 腿与父表腿**分处不同字面量** ⇒ 必须显式登记。
			//    这里断言「产物整体同时含两面」，把「登记是必要的」钉住。
			if !strings.Contains(tc.sql, "public.session_turns_hot") ||
				!strings.Contains(tc.sql, "public.session_turns") {
				t.Errorf("产物字面量里两面不齐：\n%s", tc.sql)
			}
		})
	}
}
