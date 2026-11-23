package sqlguard

// 投递门家族的**共享工具**：剥「整行注释」。
//
// ★★★ 为什么必须有它（★ 2026-11-23 乙路线轮**变异自证当场抓到的假绿**）：★★★
//
// 投递门判「登记有没有做」用的是「**文件里包含某个字符串**」这种**存在性**口径
// ⇒ ★★ **注释掉的登记也照样命中** ⇒ ⇒ 门会绿、而登记其实没了
// ⇒ ⇒ ★ **假绿比没有门更坏**（★ 门的存在意义就是「交付面断裂 ⇒ CI 可判的红」）。
//
// ★ 实测（本轮当场做的变异）：
//
//	把 installer/internal/dbinit/runner.go 里的
//	    "996_tenant_budget_enforcement_mode.sql",
//	改成注释行
//	    // MUT-EXP: "996_tenant_budget_enforcement_mode.sql",
//	⇒ ★ 996 投递门**照样绿** ⇒ ★ ⇒ 判据被「注释行里的同名字符串」骗过。
//
// ★★ 同样的洞在**同族 8 个门**里都在（★ 977 早就有自己的剥注释器 `pi977StripComments`
//   ⇒ ★ **这个洞早就被 977 的作者踩过一次** ⇒ ★ 但没推广到家族其余成员）。
//
// ★ 只剥「整行注释」：
//   · ★ 行尾注释**不动**（★ 真登记行尾挂个说明仍算登记 ⇒ 不会因为排版而误红）；
//   · ★ Go / shell / SQL / TSV 的整行注释前缀分别是 `//` / `#` / `--` / `#`；
//   · ★ Go 的 `//go:embed` 指令**本身**就是注释（★ Go 语法如此）⇒ 登记面检查要**保留**它
//     （★ 而把它写成 `// //go:embed …` ＝ 注释掉 ⇒ 那才该被判红）。
//
// ★★ 通用式：★ **「包含某串」这种存在性判据，在有注释的语言里永远是假绿的方向**
// ⇒ ⇒ ★ 门要判「登记**真的**在册」，就得先剥掉「不是登记」的那些行。

import "strings"

// sgStripFullLineComments 去掉「整行都是注释」的行（★ 行尾注释不动）。
//
// marker 是整行注释的前缀（★ SQL `--` / shell `#` / Go `//`）。
// keepGoEmbed=true 时保留 `//go:embed` 开头的行（★ 它是 Go 的**指令**、不是注释）。
func sgStripFullLineComments(src string, marker string, keepGoEmbed bool) string {
	if marker == "" {
		return src
	}
	var out []string
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, marker) {
			if keepGoEmbed && strings.HasPrefix(t, "//go:embed") {
				out = append(out, ln)
			}
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// sgStripSQLComments 剥 SQL 的整行注释（`--`）。
func sgStripSQLComments(src string) string { return sgStripFullLineComments(src, "--", false) }

// sgStripShellComments 剥 shell / TSV 的整行注释（`#`）。
func sgStripShellComments(src string) string { return sgStripFullLineComments(src, "#", false) }

// sgStripGoCommentsKeepEmbed 剥 Go 的整行注释（`//`），★ 但保留 `//go:embed` 指令。
func sgStripGoCommentsKeepEmbed(src string) string {
	return sgStripFullLineComments(src, "//", true)
}
