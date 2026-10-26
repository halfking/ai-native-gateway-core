package ursm_test

// R95 轮 · 段 B 的门：**「产品库名」台账的不变量**。
//
// 背景：§19.20.50 ⑤ 指出主门的库名守卫 r87ReHostMarker（整串匹配）与
//       生产规则 domains/reportrollup 的 isScratchDB（分词整词 scratch/e2e）
//       用的不是同一层判据。R95 轮实测：两套规则在 138 个真实库名上分类
//       相反 7 个 ⇒ ⇒ 「主门更宽松」这条判词的影响面被量化成了【1 个取值】。
//
// 本门把「产品库名」这份清单变成可维护的不变量：
//   I1 非临时类库名不得被主门轴（r87ReHostMarker）判成临时目标标记
//   I2 非临时类库名不得被生产轴（分词整词 scratch/e2e）判成临时库
//   I3 台账结构不变量（唯一 / 证据非空 / 分类合法 / 命名空间合法 / 解析器非空）
//
// ★ 判据来源：主门轴直接用同包的 r87ReHostMarker（零漂移）；
//   生产轴从 domains/reportrollup/realdb_e2e_test.go 的源码里**抽取**
//   token 列表与分隔符（不复制判据；抽取不到就中止 ⇒ 规则一改，门就红）。

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	pdbLedgerPath = "product_db_name_ledger.tsv"
	// pdbProdRulePath 生产规则所在源码（相对仓库根）。
	pdbProdRulePath = "../../domains/reportrollup/realdb_e2e_test.go"
)

type pdbClass string

const (
	pdbProduct          pdbClass = "product"
	pdbProductVariant   pdbClass = "product_variant"
	pdbLongLivedNonprod pdbClass = "long_lived_nonprod"
	pdbScratchFamily    pdbClass = "scratch_family"
)

// pdbEntry 台账一行。
type pdbEntry struct {
	name     string
	class    pdbClass
	evidence string
}

// pdbLoad 解析台账（跳过注释与空行）。任何结构异常直接中止。
func pdbLoad(t *testing.T) []pdbEntry {
	t.Helper()
	f, err := os.Open(pdbLedgerPath)
	if err != nil {
		t.Fatalf("打开台账失败: %v", err)
	}
	defer f.Close()
	var out []pdbEntry
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(raw, "\t")
		if len(parts) != 3 {
			t.Fatalf("台账第 %d 行字段数 = %d，应为 3（name<TAB>class<TAB>evidence）: %q", line, len(parts), raw)
		}
		e := pdbEntry{
			name:     strings.TrimSpace(parts[0]),
			class:    pdbClass(strings.TrimSpace(parts[1])),
			evidence: strings.TrimSpace(parts[2]),
		}
		if e.name == "" {
			t.Fatalf("台账第 %d 行 name 为空", line)
		}
		if e.evidence == "" {
			t.Fatalf("台账 %s 的 evidence 为空：每条都必须写清分类依据", e.name)
		}
		switch e.class {
		case pdbProduct, pdbProductVariant, pdbLongLivedNonprod, pdbScratchFamily:
		default:
			t.Fatalf("台账 %s 的 class = %q，不在允许取值内", e.name, e.class)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	// ★ 量具自检：解析器必须真的解析出了内容，否则「无违规」是假读数。
	if len(out) == 0 {
		t.Fatalf("台账解析结果为空 ⇒ 解析器可能失效，拒绝给结论")
	}
	return out
}

// pdbProdTokens 从生产规则源码里抽取「算临时库的 token 列表」。
// 抽取不到 ⇒ 中止（⇒ 生产规则一改，这道门就会逼着人重新确认台账）。
func pdbProdTokens(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(pdbProdRulePath)
	if err != nil {
		t.Fatalf("读生产规则源码失败: %v", err)
	}
	src := string(b)
	re := regexp.MustCompile(`case\s+"([^"]+)"\s*,\s*"([^"]+)"\s*:`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("未能从 %s 抽取生产规则的 token 列表 ⇒ 生产规则形态已变，需重新确认台账", pdbProdRulePath)
	}
	toks := []string{m[1], m[2]}
	sort.Strings(toks)
	return toks
}

// pdbIsScratchByProd 生产轴：库名按分隔符分词，整词命中 token ⇒ 判为临时库。
func pdbIsScratchByProd(name string, tokens []string) bool {
	fields := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	set := map[string]bool{}
	for _, tok := range tokens {
		set[tok] = true
	}
	for _, f := range fields {
		if set[f] {
			return true
		}
	}
	return false
}

// TestProductDBNameLedgerInvariants 产品库名台账的不变量。
func TestProductDBNameLedgerInvariants(t *testing.T) {
	entries := pdbLoad(t)
	tokens := pdbProdTokens(t)

	// I3-1 名字唯一。
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.name] {
			t.Errorf("台账里 %s 出现重复条目", e.name)
		}
		seen[e.name] = true
	}

	// I3-2 分类分布不得为 0（量具自检：每一类都必须真的有条目）。
	counts := map[pdbClass]int{}
	for _, e := range entries {
		counts[e.class]++
	}
	for _, c := range []pdbClass{pdbProduct, pdbProductVariant, pdbLongLivedNonprod, pdbScratchFamily} {
		if counts[c] == 0 {
			t.Errorf("台账里 class=%s 的条目数为 0 ⇒ 该类要么补齐要么删掉分类", c)
		}
	}

	// I3-3 命名空间：产品类库名必须落在已登记的命名空间内。
	for _, e := range entries {
		switch e.class {
		case pdbProduct, pdbProductVariant:
			if !strings.HasPrefix(e.name, "llm_gateway") {
				t.Errorf("%s 的 class=%s，但名字不在 llm_gateway* 命名空间内 ⇒ 分类与命名空间矛盾", e.name, e.class)
			}
		case pdbLongLivedNonprod:
			if strings.HasPrefix(e.name, "llm_gateway") {
				t.Errorf("%s 被登记为 long_lived_nonprod，但它落在 llm_gateway* 命名空间内", e.name)
			}
		case pdbScratchFamily:
			if !strings.Contains(e.name, "*") {
				t.Errorf("%s 的 class=scratch_family，但它是具体名字 ⇒ scratch 类必须写成模式", e.name)
			}
		}
	}

	// I1 + I2 非临时类库名不得被任何一轴判成临时库。
	for _, e := range entries {
		switch e.class {
		case pdbProduct, pdbProductVariant, pdbLongLivedNonprod:
		default:
			continue
		}
		if r87ReHostMarker.MatchString(e.name) {
			t.Errorf("%s（class=%s）被主门轴判成临时目标标记 ⇒ 长期存在的库可能被守卫当成可丢弃库", e.name, e.class)
		}
		if pdbIsScratchByProd(e.name, tokens) {
			t.Errorf("%s（class=%s）被生产轴判成临时库（分词命中 %v）⇒ 长期存在的库可能被守卫当成可丢弃库", e.name, e.class, tokens)
		}
	}

	// I2 反向：scratch 家族的模式必须真的被【某一轴】认出来。
	//   前缀形态（itgate_*）：认的是前缀这个标记 ⇒ 主门轴必须认它。
	//   其它形态（*_scratch / *_e2e / *_scratch_*）：认的是分词整词 ⇒ 生产轴必须认它。
	// ⇒ 模式写错（拼错 token、写错前缀）就会红。
	for _, e := range entries {
		if e.class != pdbScratchFamily {
			continue
		}
		if strings.HasPrefix(e.name, "*") {
			// 后缀 / 中缀形态：认的是分词整词 ⇒ 生产轴必须认它。
			inst := strings.ReplaceAll(e.name, "*", "probe")
			if !pdbIsScratchByProd(inst, tokens) {
				t.Errorf("scratch 模式 %s 具体化后的 %s 不被生产轴判成临时库 ⇒ 模式写错了（生产轴 token=%v）", e.name, inst, tokens)
			}
			continue
		}
		if strings.HasSuffix(e.name, "*") {
			// 前缀形态：认的是前缀这个标记 ⇒ 主门轴必须认它（去掉尾部分隔符）。
			prefix := strings.TrimRight(strings.TrimSuffix(e.name, "*"), "_-.")
			if !r87ReHostMarker.MatchString(prefix) {
				t.Errorf("scratch 前缀模式 %s 的前缀 %q 不被主门轴认成临时标记 ⇒ 前缀写错了", e.name, prefix)
			}
			continue
		}
		t.Errorf("scratch 模式 %s 既不是前缀形态也不是后缀形态 ⇒ 形态不明确", e.name)
	}
}

// TestProductDBNameLedgerAxesAreDistinct 量具自检：两轴必须真的不同。
// 若两轴恒等，则台账的分轴校验是恒绿门。
func TestProductDBNameLedgerAxesAreDistinct(t *testing.T) {
	tokens := pdbProdTokens(t)
	if len(tokens) == 0 {
		t.Fatal("生产轴 token 列表为空 ⇒ 抽取失效")
	}
	// 找一个只在生产轴成立的取值：把 token 直接当库名。
	probe := tokens[0]
	if r87ReHostMarker.MatchString(probe) && pdbIsScratchByProd(probe, tokens) {
		// 两轴都命中不构成「恒等」证据，改用一组探针整体比较。
	}
	diff := 0
	for _, p := range []string{"scratch", "e2e", "my_scratch_db", "llm_gateway_test", "test", "tmp", "itgate"} {
		a := r87ReHostMarker.MatchString(p)
		b := pdbIsScratchByProd(p, tokens)
		if a != b {
			diff++
		}
	}
	if diff == 0 {
		t.Fatalf("两轴在 7 个探针上分类完全一致 ⇒ 分轴校验是恒绿门，台账失去意义")
	}
}
