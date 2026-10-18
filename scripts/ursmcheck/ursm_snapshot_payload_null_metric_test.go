package ursm_test

// payload 字节口径必须对 NULL 安全（2026-10-18）
//
// 起因是一个**由自己引入**的缺陷：§63 把 writer 的空残渣从 `'{}'` 改成 NULL
// （约 85% 的行），而 `pg_column_size(NULL)` 返回 NULL、`avg()` 不计 NULL ——
// 于是这个脚本的「payload 平均」从 27.2 B 虚报成 182.0 B（生产实测，6.7 倍），
// 而它正是 818 收益的回归防护。修复后需要一道门，防止它被改回去。
//
// ── 这道门的第一版是**空转的**，记录下来是因为它踩的是本仓反复出现的那类坑 ──
// 第一版用「`pg_column_size(payload)` 前后各 20/120 字符窗口内出现过 coalesce(
// 或 payload IS NOT NULL 就算安全」。变异（把 coalesce 拆掉）后**仍然全绿** ——
// 因为 avg 表达式后面紧跟的 percentile_cont 表达式里就含 `payload IS NOT NULL`，
// 右窗口把 avg 自己一起豁免了。
// ⇒ **窗口式豁免不是豁免，是把判据的断言范围扩到了被测对象之外。**
// ⇒ 现在的形态改成「取 `avg(` 的实参、闭括号配平、再判该实参本身」，
//    判据的范围由**语法**决定，不由字符数决定。
//
// ★ 只钉 avg，不做「全文禁止裸 pg_column_size(payload)」：
//   `percentile_cont(...) ORDER BY pg_column_size(payload)` 是**安全**的
//   （有序集聚合按文档本就忽略 NULL，生产实测两代编码都返回 182.0）。
//   一刀切禁止会逼后人去改一个没坏的地方。⇒ 门只钉**真的坏过的那一项**。
//
// ★ 必须先剥 SQL 行注释再判：
//   本次修复的说明文字里就写着 `avg(pg_column_size(payload))`（在讲「这是被否掉的写法」
//   的语境里）。不剥注释的话断言会匹配到注释而恒红 —— 这是「源码文本型断言」的已知失效形态。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stripSQLLineComments 去掉整行 `--` 注释。本脚本的 SQL 里没有块注释。
func stripSQLLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// avgArgs 返回每个 `avg(` 的实参子串（以配平的右括号收尾）。
// 只处理 avg 的实参里没有嵌套括号的情形 —— 本脚本的 avg 实参是
// `coalesce(pg_column_size(payload), 0)`，有嵌套，故这里做括号配平。
func avgArgs(code string) []string {
	var out []string
	for i := 0; i+4 <= len(code); i++ {
		if code[i:i+4] != "avg(" {
			continue
		}
		// avg 作为独立函数出现时，前面不应是标识符字符。
		if i > 0 && isIdentByte(code[i-1]) {
			continue
		}
		depth := 1
		j := i + 4
		for ; j < len(code) && depth > 0; j++ {
			switch code[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			continue // 括号不配平，交给正向自证报错
		}
		out = append(out, code[i+4:j-1])
	}
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '.' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func TestBloatScriptAveragePayloadBytesIsNULLSafe(t *testing.T) {
	// 与既有门同源：bloatScript 是相对 scripts/ 的，本测试跑在 scripts/ursmcheck/。
	b, err := os.ReadFile(filepath.Join("..", bloatScript))
	if err != nil {
		t.Fatalf("read %s: %v", bloatScript, err)
	}
	code := stripSQLLineComments(string(b))

	args := avgArgs(code)
	// 正向自证：必须真的找到「对 payload 字节求平均」这件事，
	// 否则下面的循环空转、判据会变成「什么都没查」的恒绿。
	var checked int
	for _, arg := range args {
		if !strings.Contains(arg, "pg_column_size") {
			continue
		}
		checked++
		if strings.Contains(arg, "coalesce(") {
			continue
		}
		t.Errorf("%s 里有一处对 payload 字节的 avg，其实参没有做 NULL 保护：\n  avg(%s)\n"+
			"★ §63 起 writer 把空残渣存成 NULL（约 85%% 的行），pg_column_size(NULL)=NULL，\n"+
			"  而 avg() 不计 NULL ⇒ 分母里丢掉那 85%%，均值虚高。\n"+
			"  生产实测：27.2 B → 182.0 B（6.7 倍），把 818 的回归防护变成一台假警报机。\n"+
			"  修法：avg(coalesce(pg_column_size(payload), 0))。", bloatScript, strings.TrimSpace(arg))
	}
	if checked == 0 {
		t.Fatalf("%s 里没有找到任何「对 pg_column_size 求 avg」的位置 ——\n"+
			"  脚本形态已变（字节口径可能改名或改由别处计算）。\n"+
			"  先核对量具是否仍测同一件事，再更新本判据；不要直接删断言。", bloatScript)
	}
	t.Logf("检查了 %d 处 payload 字节的 avg，全部 NULL 安全", checked)
}
