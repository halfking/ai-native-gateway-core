package ursm_test

// §143.69 的门：把 841 `monthly_partition_retention` 的**安全约束**钉成静态判据。
//
// ★ 为什么这道门现在才写（§143.69 的起因）：
//
//	§143.68 发现 **v2 正文表（`session_bodies`）与 turn 表（`session_turns`）在 DB 层
//	没有任何保留期** ⇒ 实测 ≈ 230 MB/天 ⇒ 一年 ≈ 84 GB ⇒ 属主决策 ⑪。
//	§143.69 在一次性库里把 841 对会话族启用的行为**实测了 17 条**（全部通过），
//	⇒ 方案是「零代码、可执行」的；
//	★ 但一旦属主拍板，841 就**会被用于会话族** ⇒ 而 841 的 DROP 是**不可逆**的
//	  ⇒ ⇒ **「当月与未来月永不被删」这条安全属性必须被钉住**，否则有一天有人
//	    把 `mth < cutoff` 改成 `mth <= cutoff`，就会把**当月的正文整月删掉**。
//
// ★ 本门只读仓内的 841 迁移字节 ⇒ **不依赖生产状态、不含任何生产读数**。
//
// ★ 判据清单（★ 断言前先【剥注释】—— 承 R65 的教训：
//   「断言 SQL 文本前必须剥注释」，否则注释里的教学示例会把门顶成永绿）：
//   ① 过期判定必须是**严格小于** `mth < cutoff`（不得出现 `<=`）
//      ⇒ 「当月与未来月永不过期」是**不可逆**的安全属性。
//   ② 分区名必须按 `_<YYYY_MM>$` 正则匹配
//      ⇒ ⇒ `default` 分区、非法月、非法形态的名字都不会被认。
//   ③ 月份提取必须用 `substring(... from '([0-9]{4}_[0-9]{2})$')`
//      ⇒ **不得**用 `right(relname, 6)`（★ 841 头注记录的真 bug：
//         `2026_07` 是 7 个字符 ⇒ right(...,6) 得到 `026_07` ⇒ to_date 出无意义日期
//         ⇒ `mth < cutoff` 对**所有**分区成立 ⇒ 当月与预建的未来分区也会被 DROP）。
//   ④ DROP 必须走 `format('DROP TABLE IF EXISTS public.%I', …)`
//      ⇒ 标识符绝不拼字符串。
//   ⑤ 每次 DROP 必须写一行审计（`llm_gateway_partition_drop_log`）
//      ⇒ 「DROP 不可逆 ⇒ 审计表是事后唯一的线索」。
//   ⑥ 配置表必须有 `retain_months >= 1` 的 CHECK。
//   ⑦ ★ 带对照的【正则自证】：`session_bodies_default` **不**匹配
//      `_\d{4}_\d{2}$`（⇒ default 分区永不被删），而 `session_bodies_2026_09` **匹配**
//      ⇒ ⇒ 判据 ② 不是恒真的。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig841Path14369 = "sql/migrations/startup/841_monthly_partition_retention.sql"
)

func repoRoot14369(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// stripSQLComments14369 去掉 `--` 行注释与 `/* */` 块注释。
//
// ★ 为什么必须剥：841 的文件头里有大量说明性文字，其中**就包含**这些判据的
//
//	关键字符串（例如「`mth < cutoff`」「`right(relname, 6)`」的复盘描述）
//	⇒ ⇒ 不剥注释的话，一个「把 `<` 改成 `<=`」的坏版本仍然会在注释里命中
//	⇒ ⇒ **门会恒绿**。
func stripSQLComments14369(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if idx := strings.Index(trimmed, "*/"); idx >= 0 {
				inBlock = false
				line = trimmed[idx+2:]
			} else {
				continue
			}
		}
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		if idx := strings.Index(line, "/*"); idx >= 0 {
			if end := strings.Index(line[idx:], "*/"); end >= 0 {
				line = line[:idx] + line[idx+end+2:]
			} else {
				line = line[:idx]
				inBlock = true
			}
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func readMig84114369(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot14369(t), mig841Path14369)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", mig841Path14369, err)
	}
	return stripSQLComments14369(string(raw))
}

// TestSection14369RetentionStrictlyLessThan pins 判据 ①：过期判定必须严格小于。
func TestSection14369RetentionStrictlyLessThan(t *testing.T) {
	body := readMig84114369(t)

	if !strings.Contains(body, "mth < cutoff") {
		t.Errorf("%s：找不到 `mth < cutoff` ⇒ ★ 过期判定的形态变了 ⇒ 请同步本门（不要直接删判据）",
			mig841Path14369)
	}
	if strings.Contains(body, "mth <= cutoff") {
		t.Errorf("%s：过期判定出现 `mth <= cutoff` ⇒ ★★★ 「当月与未来月永不过期」这条【不可逆】安全属性被打破 ⇒ ⇒ 841 一旦用于会话族，会把当月的正文整月删掉 ⇒ ⇒ 841 头注的安全约束 5 明确要求「由第 4 条的 `<` 严格不等号保证」",
			mig841Path14369)
	}
}

// TestSection14369RetentionOnlyMonthlyPartitions pins 判据 ②：只认 `_<YYYY_MM>$` 形态。
func TestSection14369RetentionOnlyMonthlyPartitions(t *testing.T) {
	body := readMig84114369(t)
	if !strings.Contains(body, `_\d{4}_\d{2}$`) {
		t.Errorf("%s：找不到分区名正则 `\\d{4}_\\d{2}$` ⇒ ★ 判据对象漂移 ⇒ 请同步本门",
			mig841Path14369)
	}
}

// TestSection14369RetentionMonthExtraction pins 判据 ③：月份提取必须用 substring。
func TestSection14369RetentionMonthExtraction(t *testing.T) {
	body := readMig84114369(t)
	if !strings.Contains(body, "substring(child.relname from") {
		t.Errorf("%s：月份提取不再用 `substring(... from ...)` ⇒ ★★ 若改回 `right(relname, 6)`，"+
			"`2026_07` 会被截成 `026_07` ⇒ to_date 出无意义日期 ⇒ `mth < cutoff` 对**所有**分区成立 "+
			"⇒ ⇒ **当月与预建的未来分区也会被 DROP** ⇒ 请同步本门", mig841Path14369)
	}
	if strings.Contains(body, "right(child.relname") || strings.Contains(body, "right(relname") {
		t.Errorf("%s：出现 `right(relname…)` 形态的月份提取 ⇒ ★★ 这是 841 头注记录的真 bug "+
			"（`2026_07` 是 7 个字符）⇒ ⇒ 请同步本门并改回 `substring(... from ...)`", mig841Path14369)
	}
}

// TestSection14369RetentionDropIsSafeAndAudited pins 判据 ④⑤⑥。
func TestSection14369RetentionDropIsSafeAndAudited(t *testing.T) {
	body := readMig84114369(t)

	// ④ 标识符不拼字符串
	if !strings.Contains(body, "%I") {
		t.Errorf("%s：DROP 路径里没有 `%%I` ⇒ ★ 标识符可能走了字符串拼接 ⇒ 请同步本门", mig841Path14369)
	}
	if !strings.Contains(body, "DROP TABLE IF EXISTS public.%I") {
		t.Errorf("%s：DROP 不再是 `DROP TABLE IF EXISTS public.%%I` ⇒ ★ 841 的安全约束 6"+
			"（标识符绝不拼字符串）被打破 ⇒ 请同步本门", mig841Path14369)
	}
	// ⑤ 每次 DROP 写审计
	if !strings.Contains(body, "llm_gateway_partition_drop_log") {
		t.Errorf("%s：找不到审计表 `llm_gateway_partition_drop_log` ⇒ ★ 841 的安全约束 7"+
			"（每次 DROP 写一行审计）被打破 ⇒ ★ DROP 不可逆 ⇒ 审计表是事后唯一的线索", mig841Path14369)
	}
	// ⑥ retain_months >= 1
	if !strings.Contains(body, "retain_months >= 1") {
		t.Errorf("%s：配置表缺少 `retain_months >= 1` 的 CHECK ⇒ ★ 请同步本门", mig841Path14369)
	}
}

// TestSection14369DefaultPartitionIsNeverSelected 是判据 ⑦：**带对照**的正则自证。
//
// ★ 为什么要「带对照」：只断言「default 不匹配」可能恒真（正则写错也「不匹配」）。
//
//	⇒ 必须同时断言「合法月分区**确实**匹配」⇒ ⇒ 两句话合起来才证明正则本身是对的。
func TestSection14369DefaultPartitionIsNeverSelected(t *testing.T) {
	re, err := regexp.Compile(`_\d{4}_\d{2}$`)
	if err != nil {
		t.Fatalf("编译 841 的分区名正则失败：%v", err)
	}
	// 阴性：default 分区、非法形态 ⇒ 都不该被认
	for _, name := range []string{
		"session_bodies_default",
		"session_turns_default",
		"session_bodies_2026_9", // 一位月
		"session_bodies_202609", // 无分隔
		"session_bodies_hot",    // 非分区名
	} {
		if re.MatchString(name) {
			t.Errorf("分区名 %q 竟匹配 `\\d{4}_\\d{2}$` ⇒ ★ default/非法形态会被 841 认成月分区", name)
		}
	}
	// 阳性：合法月分区必须匹配（★ 证明正则不是恒假的）
	for _, name := range []string{
		"session_bodies_2026_09",
		"session_turns_2026_10",
	} {
		if !re.MatchString(name) {
			t.Errorf("合法月分区名 %q 不匹配 `\\d{4}_\\d{2}$` ⇒ ★ 正则写错了（判据恒假）", name)
		}
	}
}

// TestSection14369StrictLessThanJudgeIsNotConstant 用**合成源码**自证判据 ① 不是恒真。
//
// ★ 纪律：一道只断言异常形态的门，必须配一条「正常形态必须通过 + 反例必须失败」的对照。
func TestSection14369StrictLessThanJudgeIsNotConstant(t *testing.T) {
	const bad = `IF mth <= cutoff THEN
    RETURN NEXT;
END IF;`
	const good = `IF mth < cutoff THEN
    RETURN NEXT;
END IF;`
	judge := func(body string) bool {
		hasGood := strings.Contains(body, "mth < cutoff")
		hasBad := strings.Contains(body, "mth <= cutoff")
		return hasGood && !hasBad
	}
	if judge(bad) {
		t.Errorf("反例（`mth <= cutoff`）竟被判为通过 ⇒ ★ 判据 ① 是恒真的 ⇒ 门没有守卫力")
	}
	if !judge(good) {
		t.Errorf("正例（`mth < cutoff`）竟被判为失败 ⇒ ★ 判据 ① 判反了")
	}
}
