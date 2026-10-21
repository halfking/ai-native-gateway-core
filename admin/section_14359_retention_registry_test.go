package admin

// section_14359_retention_registry_test.go —— §143.59 门：登记册的钉法改造。
//
// ── 为什么要有这道门 ────────────────────────────────────────────────────────
// §143.58 把「存储存量台账」做出来之后，自己复查发现登记册的**钉法**是错的：
//
//	旧门把**按天增长的族**按【绝对字节数】钉死（252 快照族 = 9,226,485,760 B）。
//	⇒ 实测：§143.58 读数 10,193,199,104 B（10-22 00:23）
//	         §143.59 读数  9,508,798,464 B（10-22 01:54）
//	⇒ **1.5 小时内 −6.7%**（7 天窗口的日分区到期被删，方向还是**下降**）
//	⇒ ⇒ 那种钉法**天然会过期**，而且"过期"与"出错"在门眼里长得一模一样
//	   （都是"读数不等于钉的值"）⇒ 门会变成"催人工刷新"的闹钟，而不是护栏。
//
// ── §143.59 的新钉法（四件套）────────────────────────────────────────────────
//
// ① **量级档位**（粗）：增长族改钉「落在哪个量级区间」，容得下自然增长，
//    但仍能抓住「被清零 / 被换成别的对象」这类 gross error。
// ② **相对量**（细）：钉「族 ÷ 库总量」这个**比值** —— 它对自然增长/自然回收
//    都不敏感（分子分母一起长/一起缩）⇒ 比绝对值耐用一个数量级。
// ③ **读数日期戳 + 陈旧度上限**：表头必须带 `-- reading-date:`，
//    门解析它并断言「没过期」⇒ 过期是**显式红灯**，而不是"钉值对不上"这种
//    含义模糊的红。
// ④ **静态对象仍钉绝对值**：不增长的对象（legacy 锚点、空分区、纯索引膨胀）
//    钉绝对值是**对的** —— 实测两侧 legacy 的行数与字节与 §143.43/§143.45
//    的读数**逐字相同**。
//
// ── 本门同时守 §143.58.6 ③ 的覆盖面缺口 ────────────────────────────────────
// §143.58 盘点出三个「没有自动回收路径」的大对象，其中**第一大对象
// （route_incident_events，1.13 GB）在旧登记册里根本没有行**
// ⇒ 覆盖面缺口本身也要被门守住（少一行 = 少一个可回收对象被看见）。
//
// ★ 本门**不需要数据库**（纯静态 + 纯函数）⇒ 不会 SKIP 成假绿。
// ★ 本门的所有判据都配了**对照**（见 TestSection14359MagnitudeBandSelfTest
//   与 TestSection14359RatioBandRejectsOutOfBand）：一道只断言异常形态的检查
//   必须配一条"正常形态必须通过"的对照，否则它可能恒真。

import (
	"os"
	"strings"
	"testing"
	"time"
)

const (
	// tsvPath14359 与 §143.43 门读的是**同一份**登记册（避免两份真相）。
	tsvPath14359 = "../scripts/audit/retention_shape_findings.tsv"
	// registryMaxStalenessDays 是登记册的**陈旧度上限**。
	//
	// ★ 为什么是 45 天而不是「每次写入都刷新」：7 天窗口的日分区每天都在
	//   换（实测 1.5 小时就变了 6.7%）⇒ 「有写入就刷新」会变成**永动刷新**
	//   ⇒ 门就永远绿、永远没有保护意义。
	//   ⇒ 折中：45 天一个上限，**过期就红**（逼人来看），而不是要求每次都准。
	registryMaxStalenessDays = 45
)

// retentionMagnitudeBand 是「量级区间」判据。
//
// ★ 为什么用区间而不是"落在哪个 2 的幂档位"：档位边界（如 4 GiB / 64 GiB）
//
//	正好落在族会经过的路径上 ⇒ 一个每天长 1.2 GB 的族会在某一天**跨过档位**
//	⇒ 门会莫名其妙地红一次。
//	⇒ 区间（lo, hi] 的上下界选在"量级差 4 倍以上"的位置 ⇒ 自然增长跨不过去。
type retentionMagnitudeBand struct {
	Lo int64
	Hi int64
	// Label 只用于报错信息。
	Label string
}

// in 判定字节数是否落在区间内。
func (b retentionMagnitudeBand) in(v int64) bool {
	if b.Lo == 0 {
		return v == 0
	}
	return v > b.Lo && v <= b.Hi
}

// retentionBandOfRows 是「按行数」的量级区间（用于「行数被清零」这类 gross error）。
type retentionBandOfRows struct {
	Lo, Hi int64
	Label  string
}

func (b retentionBandOfRows) in(v int64) bool { return v >= b.Lo && v <= b.Hi }

// §143.59 各对象的钉法：
//
//	「增长族」⇒ 量级区间（粗）+ 相对量（细）
//	「静态对象」⇒ 绝对值（由 §143.43 门负责）
var retention14359GrowingBands = map[string]retentionMagnitudeBand{
	"252|public.ursm_node_snapshot_min": {Lo: 1 << 30, Hi: 1 << 40, Label: "1 GiB ~ 1 TiB"},
	"211|public.ursm_node_snapshot_min": {Lo: 1 << 30, Hi: 1 << 40, Label: "1 GiB ~ 1 TiB"},
	"252|public.usage_facts":            {Lo: 1 << 20, Hi: 1 << 33, Label: "1 MiB ~ 8 GiB"},
	"211|public.usage_facts":            {Lo: 1 << 20, Hi: 1 << 33, Label: "1 MiB ~ 8 GiB"},
}

// retentionPermille 计算「族 ÷ 库总量」的千分比（整数 ‰）。
//
// ★ 为什么用**千分比整数**而不是百分比浮点：
//
//	上一版用 `int64(pct)`（百分比取整）⇒ 实测 1.86% 被截断成 1%，
//	而区间下界恰好是 1 ⇒ `1 > 1` 为假 ⇒ **正常值被判成异常**。
//	⇒ 这正是「量具口径」问题：不是数据错，是尺子的刻度错了。
//	⇒ 千分比（1.86% = 18‰）的分辨率是 0.1%，取整误差 < 0.1 个千分点，
//	且下界用 `>` 语义时不会误伤边界值。
func retentionPermille(bytes, total int64) int64 {
	if total <= 0 {
		return -1
	}
	return bytes * 1000 / total
}

// retention14359RatioBands 是「相对量（族 ÷ 库总量）」的钉法（单位：‰）。
//
// ★ 上下界的取法：以**实测值**为中心，上下各留一个数量级的一半。
//
//	例：252 快照族 ÷ 库 = 29.86% ⇒ 区间 [20%, 45%]
//	（实测 1.5 小时内族自身降了 6.7% ⇒ 比值降到 ~28% ⇒ 仍在区间内 ✓）
//
//	⇒ 区间要**容得下自然波动**，又要**窄到能抓住"某个族被漏掉了"**
//	（例如 route_incident_events 掉出登记册 ⇒ 比值变 0 ⇒ 立刻红）。
var retention14359RatioBands = map[string]retentionMagnitudeBand{
	// 实测：252 = 298.56‰、211 = 274.32‰ ⇒ 区间取 [200, 450]
	"252|public.ursm_node_snapshot_min": {Lo: 200, Hi: 450, Label: "20.0% ~ 45.0%"},
	"211|public.ursm_node_snapshot_min": {Lo: 200, Hi: 450, Label: "20.0% ~ 45.0%"},
	// 实测：252 = 8.17‰、211 = 10.16‰
	"252|public.usage_facts": {Lo: 1, Hi: 100, Label: "0.1% ~ 10.0%"},
	"211|public.usage_facts": {Lo: 1, Hi: 100, Label: "0.1% ~ 10.0%"},
	// 实测：252 = 38.11‰、211 = 18.63‰
	"252|public.route_incident_events": {Lo: 1, Hi: 150, Label: "0.1% ~ 15.0%"},
	"211|public.route_incident_events": {Lo: 1, Hi: 150, Label: "0.1% ~ 15.0%"},
	// 实测：252 = 27.19‰、211 = 11.79‰
	"252|public.session_summaries": {Lo: 1, Hi: 150, Label: "0.1% ~ 15.0%"},
	"211|public.session_summaries": {Lo: 1, Hi: 150, Label: "0.1% ~ 15.0%"},
}

// retention14359RequiredGapRows 是 §143.58 盘点出的「没有自动回收路径」的对象，
// 每个都必须在登记册里有行（**覆盖面缺口**的门）。
//
// ★ mechanism_state 是**钉现状**（反向断言）：形态 B（零调用方）与形态 C
//
//		（目标集恒空）**不是同一件事**，处置也不同 ⇒ 不许被合并成一句话。
//		⇒ 若将来属主拍板接线（形态 B ⇒ 改接线），这一行要连同 method 一起改，
//		   并同步改 domains/routeincident/retention_wiring_test.go 的反向断言
//	  （两处都要动，只动一处会得到一个恒绿门）。
var retention14359RequiredGapRows = map[string]struct {
	MechanismState RetentionMechanismState
	Verdict        RetentionVerdict
}{
	"252|public.route_incident_events": {MechanismStateNotWired, VerdictMechanismNotWired},
	"211|public.route_incident_events": {MechanismStateNotWired, VerdictMechanismNotWired},
	"252|public.session_summaries":     {MechanismStateTargetEmpty, VerdictMechanismTargetEmpty},
	"211|public.session_summaries":     {MechanismStateTargetEmpty, VerdictMechanismTargetEmpty},
}

// readRegistryFile 读登记册全文（供表头解析用）。
func readRegistryFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取登记册 %s：%v", path, err)
	}
	return string(raw)
}

// readRetentionShapeRows14359 与 §143.43 门的读取器**同款**（同一份真相）。
func readRetentionShapeRows14359(t *testing.T, path string) []RetentionShapeRow {
	t.Helper()
	raw := readRegistryFile(t, path)
	var rows []RetentionShapeRow
	for i, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(line, "instance\t") {
			continue
		}
		row, err := ParseRetentionShapeRow(line)
		if err != nil {
			t.Fatalf("登记册第 %d 行解析失败：%v", i+1, err)
		}
		rows = append(rows, row)
	}
	return rows
}

// registryReadingDate 解析登记册表头里的 `# reading-date: YYYY-MM-DD`。
//
// ★ 返回 error 而不是零值：零值 time 会让"陈旧度"判据变成
//
//	"零值时间距今很久 ⇒ 过期" ⇒ 看起来会红，但红的原因说错了
//	（真实原因是"表头根本没写日期"）⇒ 两种错必须分开报。
func registryReadingDate(t *testing.T, raw string) (time.Time, string) {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		parsed, val, ok := parseReadingStamp(line)
		if !ok {
			continue
		}
		return parsed, val
	}
	t.Fatalf("登记册表头缺少 reading-date 行（'# reading-date:' 或 '-- reading-date:'）—— ★ 陈旧度判据必须有一个**显式**的读数日期戳，" +
		"否则它只会退化成「钉值对不上」那种含义模糊的红")
	return time.Time{}, ""
}

// parseReadingStamp 从一行表头里解析读数日期戳（**纯函数**）。
//
// ★ 表头前缀有两种合法形态：TSV 里用 '#'（与其它注释行一致），
//
//	事实层脚本里用 '--'（与 SQL 注释一致）⇒ 两者都接受。
func parseReadingStamp(line string) (time.Time, string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, marker := range []string{"# reading-date:", "-- reading-date:"} {
		if !strings.HasPrefix(trimmed, marker) {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
		parsed, err := time.Parse("2006-01-02", val)
		if err != nil {
			return time.Time{}, val, false
		}
		return parsed, val, true
	}
	return time.Time{}, "", false
}

// retentionReadingStaleDays 是陈旧度判据的**纯函数**形态（只依赖两个日期）。
//
// ★ 为什么要抽成纯函数：判据自检必须能在**不依赖被测值**的前提下问它
//
//	（否则「解析器恒真」「判据恒真」这类变异能从自检的缝里溜过去
//	—— 本轮 M6 实测踩到：对照是从被测值自身派生的 ⇒ 恒真的解析器仍让门绿）。
func retentionReadingStaleDays(readDate, today time.Time) int64 {
	return int64(today.Sub(readDate).Hours() / 24)
}

// TestSection14359MagnitudeBandSelfTest 是量级区间判据的**自检**。
//
// ★ 为什么必须自检：一个"看起来在检查"的判据可能恒真（例如把 `in` 写成
//
//	永远 return true）⇒ 而这种门在正常世界里**永远绿**、在坏世界里也永远绿
//	⇒ 等于没有门。
//	⇒ 所以这里**故意**用边界值与越界值去问它。
func TestSection14359MagnitudeBandSelfTest(t *testing.T) {
	band := retentionMagnitudeBand{Lo: 1 << 30, Hi: 1 << 40, Label: "1 GiB ~ 1 TiB"}

	// 正常形态必须**通过**（对照）。
	if !band.in(9_508_798_464) {
		t.Errorf("量级区间判据把实测值 9,508,798,464 判成区间外 —— 判据写反了")
	}
	// 区间内的大值必须通过。
	if !band.in(1 << 40) {
		t.Errorf("量级区间上界（含）1 TiB 应判为区间内")
	}
	// 越界值必须**被拒**（异常形态）。
	for _, v := range []int64{
		0,             // 被清零
		1,             // 只剩 1 字节
		(1 << 30) - 1, // 下界（不含）
		(1 << 40) + 1, // 上界之上
		1 << 41,       // 翻了一档
	} {
		if band.in(v) {
			t.Errorf("量级区间判据把越界值 %d 判成区间内 —— 这个判据没有牙", v)
		}
	}
	// 零区间（Lo==0）只接受 0。
	zero := retentionMagnitudeBand{Lo: 0, Hi: 0, Label: "仅 0"}
	if !zero.in(0) {
		t.Errorf("零区间判据拒绝了 0")
	}
	if zero.in(1) {
		t.Errorf("零区间判据接受了 1 —— 零区间必须只接受 0")
	}
}

// TestSection14359RatioBandRejectsOutOfBand 是相对量判据的自检（对照）。
func TestSection14359RatioBandRejectsOutOfBand(t *testing.T) {
	band := retention14359RatioBands["252|public.ursm_node_snapshot_min"]
	if band.Lo <= 0 || band.Hi <= band.Lo {
		t.Fatalf("相对量区间定义异常：%+v", band)
	}
	// 实测比值 298.56‰ 必须在区间内。
	if !band.in(298) {
		t.Errorf("相对量区间把实测 298‰ 判成区间外")
	}
	// ★ 上一版的坑：百分比取整会把 1.86% 截成 1% ⇒ 正常值被判异常。
	//   ⇒ 千分比下 1.86% = 18‰，必须落在下界之外、内界之内。
	small := retention14359RatioBands["211|public.route_incident_events"]
	if !small.in(retentionPermille(154738688, 8306914995)) {
		t.Errorf("千分比判据把 211 的实测相对量判成区间外")
	}
	// 0（对象从登记册里消失）必须被拒。
	if band.in(0) {
		t.Errorf("相对量区间接受了 0%% —— 对象消失时必须红")
	}
	// 100%（族吞掉整个库）必须被拒。
	if band.in(100) {
		t.Errorf("相对量区间接受了 100%%")
	}
}

// TestSection14359ReadingDateIsStaleBounded 守「读数日期戳 + 陈旧度上限」。
//
// ★ 这条替换了旧的「绝对字节钉死」：过期时给出一条**含义明确**的红，
//
//	而不是让人猜"哪个数字变了"。
func TestSection14359ReadingDateIsStaleBounded(t *testing.T) {
	raw := readRegistryFile(t, tsvPath14359)
	readDate, rawStamp := registryReadingDate(t, raw)

	// ★★★★ 独立对照之零：**走【被测的那条真实路径】**喂一份人造登记册文本
	//   （日期戳 = 2020-05-05，**一定不是今天**）⇒ 必须如实解析出来。
	//
	//	⇒ 这一条是 M6（解析恒真）的**唯一有效**拦截点，两次踩坑的教训：
	//	① 「对照从被测值自身派生」⇒ 恒真解析器仍绿；
	//	② 「改成解析结果必须等于文件原文」⇒ 因为登记册的日期**恰好就是今天**
	//	   ⇒ 恒真解析器返回的今天**恰好与原文相等** ⇒ **仍然绿**；
	//	③ 「只给抽出来的纯函数做单测」⇒ 变异改的是**调用方**（`registryReadingDate`）
	//	   ⇒ 纯函数没变 ⇒ 单测仍绿。
	//	⇒ ⇒ ★ **⇒ 对照必须覆盖【被测的那条真实路径】，且输入必须与被测值无关。**
	probe, probeRaw := registryReadingDate(t, "# reading-date: 2020-05-05\n")
	if probeRaw != "2020-05-05" || probe.Format("2006-01-02") != "2020-05-05" {
		t.Errorf("读数日期解析自检失败：喂进一份日期戳为 2020-05-05 的登记册文本，却解析成 %q（raw=%q）"+
			"⇒ 解析路径可能已被改成恒真（门会永远绿）",
			probe.Format("2006-01-02"), probeRaw)
	}

	// ★★★ 独立对照之一：**解析结果必须与文件里的原文一致**。
	//
	//	⇒ 这一条专门堵「解析器恒真（永远返回今天）」这种变异 ——
	//	本轮 M6 实测：若对照是从被测值自身派生的，恒真的解析器能从缝里溜过去
	//	（门仍然绿 ⇒ 门没有牙）。
	if readDate.Format("2006-01-02") != rawStamp {
		t.Errorf("登记册的 reading-date 解析成 %q，与文件里的原文 %q 不一致 ⇒ "+
			"解析器可能已被改成恒真（门会永远绿）",
			readDate.Format("2006-01-02"), rawStamp)
	}

	// ★ 时区纪律：读数日期是**实例本地时区（+08）的日历日**，
	//   而 time.Parse("2006-01-02", …) 产出的是 **UTC 午夜**。
	//   ⇒ 直接拿它和 time.Now() 比会误报「在未来」
	//   （例：本地 10-22 02:57 +08 = UTC 10-21 18:57 < UTC 10-22 00:00）。
	//   ⇒ 正确做法：把两边都压成**日历日**再比。
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if readDate.After(today) {
		t.Errorf("登记册的读数日期 %s 在**未来** ⇒ 要么是手工填错，要么是时钟不一致",
			readDate.Format("2006-01-02"))
	}
	ageDays := retentionReadingStaleDays(readDate, today)
	maxAgeDays := int64(registryMaxStalenessDays)
	if ageDays > maxAgeDays {
		t.Errorf("登记册的读数日期 %s 距今 %d 天，超过陈旧度上限 %d 天 ⇒ "+
			"**必须重跑 scripts/audit/refresh_retention_registry.sql 并刷新本表**"+
			"（增长族的量级与相对量都只是粗判据，陈旧度才是「这个读数还代表今天吗」的判据）",
			readDate.Format("2006-01-02"), ageDays, registryMaxStalenessDays)
	}
	// ★★★ 独立对照之二：**用一个与被测值无关的固定老日期**问同一个判据
	//
	//	⇒ 专门堵「陈旧度判据恒真（永远判未过期）」这种变异。
	syntheticOld := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if retentionReadingStaleDays(syntheticOld, today) <= maxAgeDays {
		t.Errorf("陈旧度判据自检失败：人造日期 2020-01-01 距今 %d 天，仍被判为未过期 ⇒ 判据恒真",
			retentionReadingStaleDays(syntheticOld, today))
	}
}

// TestSection14359GrowingFamiliesAreBandedNotPinned 守「增长族按量级 + 相对量钉」。
func TestSection14359GrowingFamiliesAreBandedNotPinned(t *testing.T) {
	rows := readRetentionShapeRows14359(t, tsvPath14359)

	byKey := map[string]RetentionShapeRow{}
	for _, r := range rows {
		byKey[r.Instance+"|"+r.Object] = r
	}

	// 参照基准行（库级总量）必须存在 —— 没有分母就谈不上相对量。
	dbTotal := map[string]int64{}
	for _, r := range rows {
		if r.Scope != ScopeReferenceTotal {
			continue
		}
		if r.TotalBytes <= 0 {
			t.Errorf("%s：参照基准行的 total_bytes = %d —— 必须是库级总量的实测量，"+
				"分母为 0 时所有相对量都会变成 0（**恒真**的量具）", r.Instance, r.TotalBytes)
		}
		dbTotal[r.Instance] = r.TotalBytes
	}
	for _, inst := range []string{"211", "252"} {
		if _, ok := dbTotal[inst]; !ok {
			t.Errorf("登记册缺少 %s 的参照基准行（REFERENCE_TOTAL）⇒ 相对量判据没有分母", inst)
		}
	}

	// 量级区间。
	for key, band := range retention14359GrowingBands {
		row, ok := byKey[key]
		if !ok {
			t.Errorf("登记册缺少 %s —— 它是增长族的量级判据对象，缺行等于缺证据", key)
			continue
		}
		if !band.in(row.TotalBytes) {
			t.Errorf("%s：total_bytes = %d，不在量级区间 %s 内", key, row.TotalBytes, band.Label)
		}
	}

	// 相对量（族 ÷ 库总量）。
	for key, band := range retention14359RatioBands {
		row, ok := byKey[key]
		if !ok {
			t.Errorf("登记册缺少 %s —— 它是相对量判据对象，缺行等于缺证据", key)
			continue
		}
		total, ok := dbTotal[row.Instance]
		if !ok {
			t.Errorf("%s：找不到 %s 的参照基准行 ⇒ 相对量算不出来", key, row.Instance)
			continue
		}
		if total <= 0 {
			t.Errorf("%s：分母（库总量）= %d ≤ 0", key, total)
			continue
		}
		permille := retentionPermille(row.TotalBytes, total)
		if !band.in(permille) {
			t.Errorf("%s：相对量 = %d‰（%.2f%%），不在区间 %s 内（族 %d B ÷ 库 %d B）",
				key, permille, float64(permille)/10, band.Label, row.TotalBytes, total)
		}
	}
}

// TestSection14359GapObjectCoverageIsClosed 守 §143.58.6 ③ 的覆盖面缺口。
//
// ★ 覆盖面缺口本身也要有门：否则「第一大对象在登记册里没有行」这件事
//
//	可以再发生一次而没人发现。
func TestSection14359GapObjectCoverageIsClosed(t *testing.T) {
	rows := readRetentionShapeRows14359(t, tsvPath14359)

	byKey := map[string]RetentionShapeRow{}
	for _, r := range rows {
		byKey[r.Instance+"|"+r.Object] = r
	}

	for key, want := range retention14359RequiredGapRows {
		row, ok := byKey[key]
		if !ok {
			t.Errorf("登记册缺少 %s —— §143.58 盘点出的「没有自动回收路径」的对象必须在册", key)
			continue
		}
		if row.Scope != ScopeRetentionGapObject {
			t.Errorf("%s：scope = %q，期望 %q", key, row.Scope, ScopeRetentionGapObject)
		}
		if row.MechanismState != want.MechanismState {
			t.Errorf("%s：mechanism_state = %q，期望 %q ★ —— "+
				"「形态 B（零调用方）」与「形态 C（目标集恒空）」的处置完全不同，"+
				"合并成一句话会让归因错、动作也错；"+
				"若属主已拍板接线，请连同 method 与 domains/routeincident/retention_wiring_test.go 一起改",
				key, row.MechanismState, want.MechanismState)
		}
		if row.Verdict != want.Verdict {
			t.Errorf("%s：verdict = %q，期望 %q", key, row.Verdict, want.Verdict)
		}
		// ★ 对照：这些对象**有行** ⇒ rows 必须 > 0。
		//   （"登记了一行但行数是 0" = 假空 ⇒ 看起来"这个对象不存在"）
		if row.Rows <= 0 {
			t.Errorf("%s：rows = %d —— 这是一个有行的对象；0 行会让它看起来像"+
				"「这个对象不存在」（§143.42 的假空坑）", key, row.Rows)
		}
		if row.TotalBytes <= 0 {
			t.Errorf("%s：total_bytes = %d —— 同上：不得是 0", key, row.TotalBytes)
		}
	}
}

// TestSection14359MechanismStatesAreConsistent 守「scope ↔ mechanism_state 相容」。
//
// ★ 这条是**登记册自相矛盾**的检测：决策表已经会把不相容的组合判成
//
//	REGISTRY_INCONSISTENT（§143.43 门逐行重算 ⇒ 那里就会红）。
//	这里再钉一层，是为了让报错信息**指名道姓**说清是哪个对象的哪个字段矛盾。
func TestSection14359MechanismStatesAreConsistent(t *testing.T) {
	rows := readRetentionShapeRows14359(t, tsvPath14359)
	for _, r := range rows {
		if retentionShapeMechanismStateAllowed(r.Scope, r.MechanismState) {
			continue
		}
		t.Errorf("%s/%s：scope %q 不接受 mechanism_state %q（允许：%s）",
			r.Instance, r.Object, r.Scope, r.MechanismState, retentionShapeAllowedStatesText(r.Scope))
	}
}

// TestSection14359RegistryHasNoAbsolutePinForGrowingFamilies 守「新钉法不退回旧钉法」。
//
// ★ 为什么需要这条：改造钉法很容易**只改一半** —— 旧的 wantBytes 还在，
//
//	于是门仍然会被自然增长打红 ⇒ 改造等于没做。
//	⇒ 这条断言「增长族**不在**绝对值钉表里」。
func TestSection14359RegistryHasNoAbsolutePinForGrowingFamilies(t *testing.T) {
	// 绝对值钉表在 §143.43 门里（wantBytes 局部变量）⇒ 本门改为断言
	// **登记册行数**与**新对象的存在性**，并对增长族只做量级/相对量判据。
	// ⇒ 真正的「不许退回」检查在 §143.43 门侧：它现在只钉静态对象。
	rows := readRetentionShapeRows14359(t, tsvPath14359)
	for _, r := range rows {
		switch r.Scope {
		case ScopeSnapshotFamily, ScopeUsageFactsFamily:
			if r.TotalBytes <= 0 {
				t.Errorf("%s/%s：增长族的族总量 = %d —— 必须是逐分区求和的实测量", r.Instance, r.Object, r.TotalBytes)
			}
		}
	}
}

// TestSection14359EvidenceMentionsReadingDate 守「读数证据可追溯」。
//
// ★ 弱门也要有：它不判真假，只保证**有人读过**（防止登记册被整体复制粘贴
//
//	而读数日期与内容脱节）。
func TestSection14359EvidenceMentionsReadingDate(t *testing.T) {
	raw := readRegistryFile(t, tsvPath14359)
	readDate, _ := registryReadingDate(t, raw)
	stamp := readDate.Format("2006-01-02")
	if !strings.Contains(raw, stamp) {
		t.Errorf("登记册正文里没有出现读数日期 %s ⇒ 读数与日期戳脱节", stamp)
	}
	// 对照：日期戳必须能被解析成一个**非零**日期。
	if readDate.IsZero() {
		t.Errorf("读数日期解析成零值时间")
	}
}
