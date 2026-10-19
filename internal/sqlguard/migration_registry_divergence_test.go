package sqlguard

// §143.17 新增门：台账里「同一文件名多条不同 SHA」的登记必须**消歧**。
//
// # 这道门补的是哪个盲点
//
// `TestRegistryShaIsActuallyTheFile`（同目录）把一个文件名的**所有**登记 SHA
// 收进 `recorded[name]` 集合，然后「**任一命中即过**」⇒
// **同一个迁移有多条互相矛盾的登记这件事，对那道门完全不可见**。
//
// §143.17 在生产实测撞上的正是这个形态：迁移 877 在 `docs/db-changelog.md` 里有两条
// SHA 不同的登记 ——
//
//	| 877 | `877_session_turns_search_text_trgm.sql` | `2df3437e…` | pending deploy（尚未应用） |
//	| 877 | `877_session_turns_search_text_trgm.sql` | `2771026d…` | applied+verified        |
//
// 台账于是宣称「已应用 `2771026d`」，而仓库里的文件是 `2df3437e` ——
// **读者无法回答「生产上跑的到底是哪一份字节」**，而校验和门任一匹配即过 ⇒ 完全静默。
//
// ★ git 实测把这件事钉死了（这不是猜的）：
//
//	2771026d… = 首次提交 37944e9d2 的字节（旧版，**已应用在生产**）
//	3bbad1d0… = f27e5f8b3 的字节（中间版，从未登记、从未应用）
//	2df3437e… = 最新提交 c71686fa7 的字节（**尚未应用**）
//
// ⇒ **旧版已上、新版未上**，而台账一个字都没说。
// （另有独立第二信号：旧版在父表上发普通 `CREATE INDEX`，子分区实例被 PG 自动改名为
//   `session_turns_2026_10_search_text_idx`；新版三段式 CONCURRENTLY 显式命名为
//   `..._search_trgm`。生产现场实测到的是前者 ⇒ 新版从未执行。）
//
// # 本门判什么
//
// 对台账里**同一文件名出现 ≥2 个不同 SHA** 的迁移，要求**至少一条登记行显式声明
// 「哪一条是旧字节 / 这一行是补充登记」**。判据是机械的、可复算的：
//
//	(a) 某行文本里出现了**兄弟行** SHA 的前 8 位十六进制
//	    （730/738/740/812/815/816/817/834/856 先例：「原登记行 8ad1cf9a… 保留」）；
//	(b) 某行文本里出现了 `registryDivergenceMarkers` 里的标注词
//	    （655 先例：「…旧 SHA … 为改写前内容」；862 先例：「当前正典」/「修正前指纹」）。
//
// ★ **为什么 (b) 不能省**：862 家族有 4 条登记，用的是「当前正典 / 修正前指纹 / 上一正典」
// 这种**标签式**消歧，一个十六进制都不打 ⇒ 判词收紧到「必须打 SHA」会误伤它。
// ⇒ **判据的宽严必须与仓库既有惯例一致**（§168），不是与理想形态一致。
//
// # 为什么还要一道「量具有牙」的自检
//
// 本门的主体是「台账里有没有未消歧的分歧」。若判定函数写反了（恒 true / 恒 false），
// 主体门会恒绿或恒红 —— 两种错误都**看不出来**（§ 判据纪律：0 命中的第一嫌疑是量具坏了）。
// ⇒ `TestRegistryDivergenceDetectorHasTeeth` 用**合成台账**把两种结论都钉死。

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// registryRowReMatches 是台账登记行的**整行**正则，**逐字照抄**
// `scripts/verify-migration-checksums.sh:53`（与同目录
// `TestRegistryShaIsActuallyTheFile` 用的是同一条）。
//
// ⚠️ 判据必须与权威规则**同宽窄**（§168）：本门要认的是「权威规则认得出的那种登记行」。
// 用宽松正则会把「只有 code-landed、没有 sha 列」的行、以及正文散文里的反引号文件名
// 也算进来（224 号第一版踩过，报出 3 条权威规则根本不会报的假缺陷）。
var registryRowReMatches = regexp.MustCompile(
	`(?m)^\|\s+[0-9]+\s+\|\s+\x60([0-9]+_[a-zA-Z0-9_]+\.sql)\x60\s+\|\s+\x60([0-9a-f]{64})\x60\s+\|`)

// registryDivergenceMarkers 是「本行（或兄弟行）是旧字节 / 补充登记」的标注词。
//
// 每个词都来自**仓库既有台账的真实写法**，不是凭空设计的：
//
//	改写前     —— 655 家族：「changelog 此前记录的旧 SHA b3e04d12… 为改写前内容」
//	修正前     —— 862：「修正前指纹（2026-10-14 f43501d16 引入）」
//	旧指纹     —— 856：「旧指纹 `5ab420d0…` 保留」
//	原指纹     —— 867：「下列原指纹保留」
//	原登记行   —— 730/738/740/812/815/816/817：「原登记行 8ad1cf9a… 保留」
//	另一份字节 —— 834：「与上一行 `089e6194…` 为同一文件的另一份字节」
//	上一正典 / 前正典 / 当前正典 —— 862：「r1030 修正版（**当前正典**）」
//	补登记     —— 680/738/740/815/816/817/824/856
//	补登       —— 834：「**sha 补登**」
//	追认       —— 730/810/811/812/816/817
//
// 「旧版本 / 旧字节」是给未来写法留的口子（语义上就是这俩意思），当前台账里没用到。
var registryDivergenceMarkers = []string{
	"改写前", "修正前", "旧指纹", "原指纹", "原登记行", "另一份字节",
	"上一正典", "前正典", "当前正典", "补登记", "补登", "追认", "旧版本", "旧字节",
}

// registryRow 是一条台账登记行。
type registryRow struct {
	lineNo int    // 行号（1 起）
	sha    string // SHA-256 单元格
	text   string // 该行 SHA 单元格之后的全部内容（Status 列及其后文本）
}

// parseRegistryRows 按 `registryRowReMatches` 把台账解析成「文件名 → 登记行」.
//
// 行的取法：**严格用权威正则找行**，再把该行的剩余部分当作 Status 文本
// ⇒ 匹配到的**行集合**与 `TestRegistryShaIsActuallyTheFile` 逐字一致。
func parseRegistryRows(full string) map[string][]registryRow {
	byFile := map[string][]registryRow{}
	line := 1
	cursor := 0
	for _, m := range registryRowReMatches.FindAllStringSubmatchIndex(full, -1) {
		line += strings.Count(full[cursor:m[0]], "\n")
		cursor = m[0]
		rest := full[m[1]:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		}
		name := full[m[2]:m[3]]
		byFile[name] = append(byFile[name], registryRow{
			lineNo: line,
			sha:    full[m[4]:m[5]],
			text:   strings.TrimSpace(rest),
		})
	}
	return byFile
}

// divergenceDisambiguated 判断同一文件的这些登记行是否**已消歧**。
//
// 消歧 = 至少满足其一：
//
//	(a) 某行文本里出现**兄弟行** SHA 的前 8 位十六进制；
//	(b) 某行文本里出现 registryDivergenceMarkers 里的标注词。
//
// 返回值第二项是人读的「靠什么判的」，报错信息里要用。
func divergenceDisambiguated(rows []registryRow) (bool, string) {
	shas := map[string]bool{}
	for _, r := range rows {
		shas[r.sha] = true
	}
	for _, r := range rows {
		for s := range shas {
			if s == r.sha || len(s) < 8 {
				continue
			}
			if strings.Contains(r.text, s[:8]) {
				return true, "指名兄弟 SHA " + s[:8]
			}
		}
		for _, w := range registryDivergenceMarkers {
			if strings.Contains(r.text, w) {
				return true, "标注词「" + w + "」"
			}
		}
	}
	return false, ""
}

// TestRegistryDivergentShaRowsAreDisambiguated —— 台账的「多 SHA 登记」必须消歧。
func TestRegistryDivergentShaRowsAreDisambiguated(t *testing.T) {
	regBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("读台账 %s：%v", registryPath, err)
	}
	byFile := parseRegistryRows(string(regBytes))

	// 收集「同一文件名多条不同 SHA」的迁移。
	type divergent struct {
		name string
		rows []registryRow
	}
	var list []divergent
	for name, rows := range byFile {
		shas := map[string]bool{}
		for _, r := range rows {
			shas[r.sha] = true
		}
		if len(shas) < 2 {
			continue
		}
		list = append(list, divergent{name: name, rows: rows})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })

	// ⚠️ 空门自检：本门若一个分歧都找不到，就是**恒绿门**（台账格式变了 / 分歧被合并掉了），
	// 而恒绿门看起来和「台账很干净」一模一样（§ 判据纪律：缺失与为零在读数上同形）。
	if len(list) == 0 {
		t.Fatalf("台账里没有任何「同一文件名多条不同 SHA」的登记 ⇒ 本门当前是空门（恒绿）。\n" +
			"要么台账的表格格式变了（正则认不出登记行），要么所有分歧都已被合并。\n" +
			"⚠️ 不要直接放宽本门 —— 先确认台账格式。")
	}

	bad := 0
	for _, d := range list {
		ok, why := divergenceDisambiguated(d.rows)
		if ok {
			t.Logf("✅ %s（%d 条不同 SHA）已消歧（%s）", d.name, len(d.rows), why)
			continue
		}
		bad++
		var detail strings.Builder
		for _, r := range d.rows {
			detail.WriteString("\n    第 " + strconv.Itoa(r.lineNo) + " 行  " + r.sha[:12] + "…  " + trunc14317(r.text, 90))
		}
		t.Errorf("%s 在台账里有 %d 条**不同 SHA** 的登记，但**没有任何一条说明哪一条是旧字节**。%s\n"+
			"⇒ 读者无法回答「生产上跑的到底是哪份字节」：\n"+
			"    · `applied+verified` 行可能登记的是**改写前**的字节（877 就是这样：\n"+
			"      生产跑的是首次提交 37944e9d2 的字节，仓库里已是第三次改写后的 2df3437e…）；\n"+
			"    · 也可能 `pending deploy` 行是陈旧的（627/704 就是这样：pending 行登记的\n"+
			"      字节从未进过任何 commit / 早已被改写覆盖）。\n"+
			"  修法（二选一，照仓库既有惯例）：\n"+
			"    ① 在其中一行的 Status 里**指名兄弟行的 SHA 前 8 位**\n"+
			"       （730/738/740/812/815/816/817/834/856 先例：「原登记行 8ad1cf9a… 保留」）；\n"+
			"    ② 或写明新旧关系（655 先例：「changelog 此前记录的旧 SHA … 为改写前内容；\n"+
			"       改写后实际盘上内容 SHA = …」；862 先例：「当前正典」/「修正前指纹」）。\n"+
			"  ⚠️ **不要删旧行**：旧行供已应用库按旧字节核对（校验和门任一匹配即过）。",
			d.name, len(d.rows), detail.String())
	}
	t.Logf("按台账口径重算：登记文件名 %d 个，其中**多 SHA 分歧** %d 个，未消歧 %d 个",
		len(byFile), len(list), bad)
}

// TestRegistryDivergenceDetectorHasTeeth —— 判定函数自身的**可证伪性**自检。
//
// 为什么要单独一道：主体门是「台账里有没有未消歧的分歧」。若判定函数写反了
// （恒 true / 恒 false），主体门会恒绿或恒红 —— **两种错误都看不出来**。
// ⇒ 这里用**合成台账**（不依赖磁盘文件）把三种结论都钉死：
//
//	· 两条裸登记（都不说明关系）      ⇒ 必须判「未消歧」
//	· 指名兄弟 SHA                    ⇒ 必须判「已消歧」
//	· 标签式消歧（只写「改写前」、不写 SHA）⇒ 必须判「已消歧」
func TestRegistryDivergenceDetectorHasTeeth(t *testing.T) {
	cases := []struct {
		name string
		rows []registryRow
		want bool
	}{
		{
			name: "两条裸登记 ⇒ 未消歧",
			rows: []registryRow{
				{lineNo: 1, sha: strings.Repeat("a", 64), text: "pending deploy |"},
				{lineNo: 2, sha: strings.Repeat("b", 64), text: "applied+verified |"},
			},
			want: false,
		},
		{
			name: "指名兄弟 SHA ⇒ 已消歧",
			rows: []registryRow{
				{lineNo: 1, sha: strings.Repeat("a", 64), text: "pending deploy |"},
				{lineNo: 2, sha: strings.Repeat("b", 64),
					text: "applied+verified（原登记行 aaaaaaaa… 保留） |"},
			},
			want: true,
		},
		{
			name: "标签式消歧（只有标注词、没有 SHA）⇒ 已消歧",
			rows: []registryRow{
				{lineNo: 1, sha: strings.Repeat("a", 64),
					text: "applied+verified（changelog 此前记录的旧 SHA 为改写前内容） |"},
				{lineNo: 2, sha: strings.Repeat("b", 64), text: "pending deploy |"},
			},
			want: true,
		},
	}
	for _, c := range cases {
		got, why := divergenceDisambiguated(c.rows)
		if got != c.want {
			t.Errorf("%s：判定函数返回 %v（%s），期望 %v ⇒ 判定函数本身有 bug，"+
				"主体门此刻的绿/红都不可信", c.name, got, why, c.want)
		}
	}
}

// trunc14317：日志里截断长 Status 文本用（按 rune 截，避免把中文截成半个字）。
func trunc14317(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
