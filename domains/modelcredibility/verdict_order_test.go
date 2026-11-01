package modelcredibility

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ★ 2026-11-01（12h 审计轮）：**两份事实源互相钉住**。
//
// 形态：`RollupProviderModel` 的「最差」定义存在**两处**——
// ① Go 侧 `VerdictRank()`（types.go，verified=0/unknown=1/suspected=2/diluted=3，
//    越大越严重）；
// ② SQL 侧 `CASE verdict WHEN 'diluted' THEN 0 … WHEN 'verified' THEN 3 END ASC`
//    （store.go 的 RollupProviderModel 查询；数字方向与 ① 相反但**相对次序相同**）。
//
// 机制：① 当前**无任何调用方**（真正的执行面是 ②）⇒ 两份事实源之间**没有**对齐断言
// ⇒ 新增/重排一个 verdict 值时它们会**静默分叉** ⇒ 落库的结论与 Go 语义不一致，
// 而落库那一侧**没有读者**（internal/quality/api.go 只 SELECT 其它分数列）
// ⇒ 分叉不会以任何形式暴露。
//
// ⇒ 本测试把「相对次序必须严格一致」钉成可执行断言：解析 SQL 里的 CASE，
// 取出每个 verdict 的序数，与 `VerdictRank()` 比对**严格同序**（不是相等）。
//
// ★ 为什么用「同序」而不是「相等」：两侧的数字方向刻意相反（Go 是「越大越严重」，
//   SQL 是「越小越严重」以配合 ORDER BY … ASC）⇒ 相等断言会恒红；断言必须是
//   「排出来的先后顺序一样」。
func TestVerdictRankMatchesRollupSQLSeverityOrder(t *testing.T) {
	// ① Go 侧的事实源。
	goOrder := []Verdict{VerdictVerified, VerdictUnknown, VerdictSuspected, VerdictDiluted}
	rank := map[Verdict]int{}
	for _, v := range goOrder {
		rank[v] = v.VerdictRank()
	}
	// 严格递增 ⇒ VerdictRank 越大越严重。
	for i := 1; i < len(goOrder); i++ {
		if rank[goOrder[i]] <= rank[goOrder[i-1]] {
			t.Fatalf("VerdictRank 不是严格递增：%s=%d 应大于 %s=%d",
				goOrder[i], rank[goOrder[i]], goOrder[i-1], rank[goOrder[i-1]])
		}
	}

	// ② SQL 侧的事实源：从 store.go 里把 CASE 抠出来解析。
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	body := string(src)
	caseRe := regexp.MustCompile(`(?s)CASE\s+verdict(.*?)END\s+ASC`)
	m := caseRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("store.go 里找不到「CASE verdict … END ASC」⇒ 严重度序的事实源不在预期位置；" +
			"若它被改写（换表名/换写法/挪走），请同步更新本测试")
	}
	whenRe := regexp.MustCompile(`WHEN\s+'([a-z_]+)'\s+THEN\s+(-?\d+)`)
	pairs := whenRe.FindAllStringSubmatch(m[1], -1)
	if len(pairs) == 0 {
		t.Fatal("CASE verdict 里解析不出 WHEN … THEN ⇒ 写法变了，同步更新本测试")
	}
	sqlOrder := make([]Verdict, 0, len(pairs))
	sqlRank := map[Verdict]int{}
	for _, p := range pairs {
		v := Verdict(p[1])
		n := 0
		for _, c := range p[2] {
			if c == '-' {
				n = -n
				continue
			}
			n = n*10 + int(c-'0')
		}
		sqlRank[v] = n
		sqlOrder = append(sqlOrder, v)
	}

	// ③ 两侧必须覆盖**同一组** verdict（少一个 = 有一侧漏登记）。
	if len(sqlRank) != len(rank) {
		t.Fatalf("verdict 集合两侧不一致：Go %d 个 %v，SQL %d 个 %v",
			len(rank), goOrder, len(sqlRank), sqlOrder)
	}
	for _, v := range goOrder {
		if _, ok := sqlRank[v]; !ok {
			t.Fatalf("verdict %q 在 Go 侧有（rank=%d）但 SQL 的 CASE 里没有 ⇒ 两侧事实源已分叉",
				v, rank[v])
		}
	}

	// ④ 严格同序：**反向**同序。
	//    两侧的数字方向刻意相反 ——
	//      Go 侧 VerdictRank：越大越严重（verified=0 … diluted=3）；
	//      SQL 侧 CASE：越小越严重（diluted=0 … verified=3），配合
	//                   `ORDER BY … CASE … END ASC` ⇒ **先出最差的**。
	//    ⇒ 「按各自的口径排出来的序列」必须是**互为逆序**。
	bySQL := append([]Verdict(nil), sqlOrder...)
	for i := 1; i < len(bySQL); i++ {
		if sqlRank[bySQL[i]] < sqlRank[bySQL[i-1]] {
			t.Fatalf("SQL 的 CASE 序数不是升序：%v", bySQL)
		}
	}
	byGo := append([]Verdict(nil), goOrder...)
	for i := range byGo {
		// byGo 是「轻→重」；bySQL 是「重→轻」⇒ 逐项互为逆序。
		if byGo[i] != bySQL[len(bySQL)-1-i] {
			t.Fatalf("两份事实源顺序不一致：Go 升序(轻→重)=%v，SQL 升序(重→轻)=%v ⇒ "+
				"「最差」的定义已分叉（Go 侧 VerdictRank 无调用方，所以不会以任何形式报错）",
				byGo, bySQL)
		}
	}
	// 附一条可读性断言：最差的那个（Go 侧 rank 最大）在 SQL 侧必须是 CASE 里序数最小的。
	worst := byGo[len(byGo)-1]
	best := byGo[0]
	if sqlRank[worst] >= sqlRank[best] {
		t.Fatalf("最差 verdict %q 的 SQL 序数 %d 应小于最好 verdict %q 的 %d",
			worst, sqlRank[worst], best, sqlRank[best])
	}
}

// ★ 2026-11-01（12h 审计轮）：sql/schema 侧的台账解析器**丢弃 reason 且不校验**，
// 而 ursmcheck 侧强校验（name 形状 / reason 非空 / reason 不含 TAB / 不重名）。
// ⇒ 台账本身写坏时**只有一侧会响** ⇒ 「两侧语义同改」的承诺在坏格式方向上只有单边保证。
// ⇒ 本测试把 sql/schema 侧补齐到与 ursmcheck 侧同强度（共享化留待下轮）。
func TestSharedDsnExclusiveRegistryReasonIsNotDropped(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/audit/dsn_exclusive_registry.tsv")
	if err != nil {
		t.Fatalf("read shared dsn_exclusive registry: %v", err)
	}
	nameRe := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	seen := map[string]bool{}
	dataRows := 0
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		dataRows++
		name, reason, ok := strings.Cut(trimmed, "\t")
		if !ok {
			t.Fatalf("%s:%d 格式坏：应为 name<TAB>reason 两列（got %q）", "dsn_exclusive_registry.tsv", i+1, trimmed)
		}
		if !nameRe.MatchString(name) {
			t.Fatalf("%s:%d 名字 %q 形状坏（应为大写蛇形）", "dsn_exclusive_registry.tsv", i+1, name)
		}
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("%s:%d 名字 %q 的 reason 为空 ⇒ 台账写坏时只有 ursmcheck 侧会响，sql/schema 侧静默放过",
				"dsn_exclusive_registry.tsv", i+1, name)
		}
		if strings.ContainsAny(reason, "\t") {
			t.Fatalf("%s:%d 名字 %q 的 reason 含 TAB（两列格式被破坏）", "dsn_exclusive_registry.tsv", i+1, name)
		}
		if seen[name] {
			t.Fatalf("%s:%d 名字 %q 重复 —— 共享台账一个名字只许一行", "dsn_exclusive_registry.tsv", i+1, name)
		}
		seen[name] = true
	}
	if dataRows == 0 {
		t.Fatal("共享 dsn_exclusive 台账解析出 0 个条目 —— 恒返回空的守卫等于没有守卫")
	}
}
