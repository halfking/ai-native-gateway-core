package vendorprice

// 规格表式（run-on）定价页的解析路径。
//
// ★ 为什么单独一条路径：页面上**一个 markdown 表格都没有**时，块解析器会产出
//   **零候选** —— 连 orphanCandidate 都不会触发（它只对 `|` 开头的行调用）。
//   于是 deepseek / doubao / zhipu 三页的钱**整页静默消失**，而本包写着
//   「块外孤儿行必须报出来，这是钱去哪了的唯一线索」与 `TestNoMoneyEverDisappears`。
//   那条不变量在措辞上是全局的，实现和测试却只覆盖 `|` 开头的行 —— 三页正好
//   落在覆盖之外。
//
// 实测版式（docs/02-resources/research/pricing/raw/deepseek.md 第 14–30 行）：
//
//	**MODEL deepseek-v4-flash(1)deepseek-v4-pro          ← Jina 压烂的一行，不用
//	MODEL VERSION DeepSeek-V4-Flash DeepSeek-V4-Pro     ← ★ 权威列头：有序模型名
//	PRICING 1M INPUT TOKENS (CACHE HIT)$0.0028$0.003625  ← 行=角色，列=模型
//	1M INPUT TOKENS (CACHE MISS)$0.14$0.435
//	1M OUTPUT TOKENS$0.28$0.87
//	Concurrency Limit(2)2500 500**                      ← 无货币符号，因此不匹配
//
// ★ 注意这里的**轴是反的**：行是价格角色、列是模型，而块解析器假设行是模型、
//   列是角色。两者正交，不能硬塞进表块模型。
//
// ★ 只认 `MODEL VERSION` 那一行作列头，不要用上面那行 `**MODEL a(1)b`：
//   后者是 Jina 把同一份信息压进一个格子后的产物，模型名与脚注标号连在一起，
//   从它切列就是在猜。
//
// 契约（每一条都是为了「宁可少提，不可提错」）：
//   1. 必须存在 `MODEL VERSION` 且能切出 **≥2** 个模型名 —— 一列没法对账；
//   2. 每一行的货币金额个数必须**正好等于**模型数，不等就报诊断、**不猜**；
//   3. 同一行的金额必须**同一货币符号**，混了就拒；
//   4. 行标签必须明确说出角色（INPUT/OUTPUT/CACHE HIT/CACHE WRITE）与单位；
//   5. 同一个 (模型, 角色) 出现第二次 ⇒ **分档定价** ⇒ 全部按不可用报出；
//   6. 金额必须自带货币符号（`$0.14`），裸数字（`2500 500`）不算钱 ——
//      「Concurrency Limit(2)2500 500」正是靠这一条被排除的。
import (
	"regexp"
	"strconv"
	"strings"
)

// modelVersionRE 匹配有序模型列头。**只认 MODEL VERSION** 这一形态。
var modelVersionRE = regexp.MustCompile(`(?i)MODEL\s+VERSION\s+(.+)$`)

// runOnModelRE 判定一个 token 像个模型名：含字母、含分隔符或数字，不是纯词。
// 「DeepSeek-V4-Flash」过；「both」「modes」不过（无分隔符与数字）。
var runOnModelRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:[._\-][A-Za-z0-9]+)+$`)

// runOnRole 是一行 run-on 价格的角色。
type runOnRole struct {
	role ColumnRole
	re   *regexp.Regexp
}

// ★ 顺序即优先级：CACHE HIT 必须排在 CACHE MISS 与裸 INPUT 之前，
//
//	否则 `1M INPUT TOKENS (CACHE MISS)` 会被裸 INPUT 先接住，
//	把缓存命中价记成输入价 —— 两者在成本核算里方向相反。
var runOnRoles = []runOnRole{
	{RoleCacheRead, regexp.MustCompile(`(?i)CACHE\s*(?:HIT|MATCH|READ)`)},
	{RoleCacheWrite, regexp.MustCompile(`(?i)CACHE\s*WRIT`)},
	{RoleInput, regexp.MustCompile(`(?i)INPUT\s*TOKENS?`)},
	{RoleOutput, regexp.MustCompile(`(?i)OUTPUT\s*TOKENS?`)},
}

// amountRE 取一个自带符号的金额。刻意要求符号：无符号的裸数字在这类页面上
// 大量存在（并发上限、上下文长度），放进来会把「2500」记成价格。
var amountRE = regexp.MustCompile(`([$€£¥])\s*([0-9]+(?:\.[0-9]+)?)`)

// runOnTierTokenRE 匹配**行内**的计费维度词（峰谷、按时段）。
//
// ★ 这条是本路径里最要紧的护栏。实测 2026-09-24 抓的那份 deepseek 页：
//
//	29| PRICING(2)1M INPUT TOKENS
//	30| (CACHE HIT)OFF-PEAK$0.003$0.022
//	31| PEAK$0.006$0.044
//
// 同一角色有**峰、谷两套价**。若只堵「同角色出现两次」，那么某一天厂商把
// PEAK 那一组撤掉、只留 OFF-PEAK 时，护栏反而失效，**峰谷价会被当成挂牌价
// 收进 SSOT** —— 那是一个比读不出价坏得多的结果：它是错的，且看起来完全正常。
// 所以判据是**行内出现维度词就拒**，与它出现几次无关。
var runOnTierTokenRE = regexp.MustCompile(`(?i)(off[\s\-_]*peaks?|peaks?|off[\s\-_]*peak|峰谷|高峰|低谷)`)

// extractRunOnSheet 解析「规格表式」定价页。返回的候选追加在块解析结果之后。
//
// 无 MODEL VERSION 列头时返回 nil（这不是它该管的页面），**不是**返回空切片
// 带诊断 —— 那会让调用方以为「查过了，确实没有」。
func extractRunOnSheet(vendor, sourceURL string, lines []string) []Candidate {
	models, headerLine := findRunOnModelList(lines)
	if len(models) < 2 {
		return nil
	}

	var out []Candidate
	// seen 记 (模型下标, 角色) → 行号，用来发现分档定价。
	seen := map[string]int{}
	// byModel 把同一模型的三行（缓存命中 / 输入 / 输出）合成**一条**候选。
	byModel := map[string]*Candidate{}
	tiered := map[string]bool{}
	// tieredSheet 记「整份规格表因维度词而按分档拒收」的模型。
	tieredSheet := map[string]bool{}
	var order []string

	for i, line := range lines {
		ln := i + 1
		if ln <= headerLine {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "|") {
			continue
		}
		// 必须同时具备：角色措辞 + 每百万单位 + 至少一个带符号的金额。
		if !perMillionRE.MatchString(trimmed) {
			continue
		}
		role, ok := runOnRoleOf(trimmed)
		if !ok {
			continue
		}
		amounts := amountRE.FindAllStringSubmatch(trimmed, -1)
		if len(amounts) == 0 {
			continue
		}

		// ★ 契约 7（最要紧的一条）：行内出现维度词 ⇒ 整份规格表按分档拒收。
		//   放在契约 2/3 之前，因为「有几列对不对得上」在这里是次要问题 ——
		//   峰谷价无论列数是否吻合，都不是可入库的挂牌价。
		if m := runOnTierTokenRE.FindString(trimmed); m != "" {
			for _, mo := range models {
				if !tieredSheet[mo] {
					tieredSheet[mo] = true
					out = append(out, runOnDiagnosis(vendor, sourceURL, mo, trimmed, ln,
						"this line names a time-of-day billing dimension ("+m+"), so the price is "+
							"tiered. A tiered price is NOT the flat list price, and it must not "+
							"enter the SSOT — note this is refused even if only ONE tier is "+
							"present, because \"the peak row is missing\" and \"this is a peak "+
							"price\" look identical after a page edit"))
				}
			}
			continue
		}

		// 契约 2：个数必须正好等于模型数。
		if len(amounts) != len(models) {
			out = append(out, runOnDiagnosis(vendor, sourceURL, models[0], trimmed, ln,
				"this price line carries "+strconv.Itoa(len(amounts))+" amount(s) but the page's "+
					"MODEL VERSION line names "+strconv.Itoa(len(models))+" model(s); the columns "+
					"cannot be attributed without guessing, so nothing is taken from this line"))
			continue
		}
		// 契约 3：同一行必须同一货币符号。
		if !sameCurrency(amounts) {
			out = append(out, runOnDiagnosis(vendor, sourceURL, models[0], trimmed, ln,
				"this price line mixes currency symbols; a single row cannot be read as one "+
					"currency, so nothing is taken from it"))
			continue
		}
		// 契约 5：分档定价检测（先于取值，避免先把数填进去再撤回）。
		tieredHere := false
		for mi := range models {
			k := strconv.Itoa(mi) + "|" + string(role)
			if _, dup := seen[k]; dup {
				tieredHere = true
			}
			seen[k] = ln
		}

		sym := amounts[0][1]
		for mi, m := range models {
			v, err := strconv.ParseFloat(amounts[mi][2], 64)
			if err != nil {
				continue
			}
			// ★ 按**模型**合并，不按 (模型, 角色) 发候选。这类页面上一个模型
			//   的输入价、缓存命中价、输出价分三行给出；每个角色发一条的话，
			//   下游拿到的是三条各缺两项的残缺记录，而 SSOT 与提案都要求
			//   **一条候选同时带 input 与 output**。这是两种轴向的差别：
			//   表块解析器里「一行 = 一个模型」天然成立，这里不成立。
			c := byModel[m]
			if c == nil {
				c = &Candidate{
					Vendor:     vendor,
					SourceURL:  sourceURL,
					Model:      m,
					Currency:   currencyOfSymbol(sym),
					Unit:       UnitPer1M,
					LineNo:     ln,
					Confidence: ConfidenceTableRow,
				}
				byModel[m] = c
				order = append(order, m)
			}
			// Row 记**第一行**的原文：它是最能说明「这个价从哪来」的一行，
			// 而多行拼出来的 Row 会让人以为是页面上的一整行。
			if c.Row == "" {
				c.Row = trimmed
			}
			f := v
			switch role {
			case RoleInput:
				c.Input = &f
			case RoleOutput:
				c.Output = &f
			case RoleCacheRead:
				c.CacheRead = &f
			case RoleCacheWrite:
				c.CacheWrit = &f
			}
			if tieredHere {
				tiered[m] = true
			}
		}
	}

	for _, m := range order {
		c := byModel[m]
		if tiered[m] {
			markTiered(c)
		}
		if tieredSheet[m] && c.Confidence == ConfidenceTableRow {
			c.Confidence = ConfidenceUnusable
			c.Tier = "time-of-day (peak/off-peak) tier"
			c.Warnings = append(c.Warnings,
				"this page publishes a peak/off-peak price for this model; see the diagnosis "+
					"candidate on this page for why the whole sheet is refused")
		}
		// 缺 input 或 output 的模型**必须**带原因出现，不能悄悄消失。
		if c.Input == nil || c.Output == nil {
			c.Confidence = ConfidenceUnusable
			c.Warnings = append(c.Warnings,
				"this page's run-on sheet gives input and output prices on separate lines; "+
					"this model is missing at least one of them, so it cannot become a baseline "+
					"price (a baseline needs both)")
		}
		out = append(out, *c)
	}
	return out
}

// findRunOnModelList 找到有序模型列头。返回模型名（原始大小写）与行号。
func findRunOnModelList(lines []string) ([]string, int) {
	for i, line := range lines {
		m := modelVersionRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var models []string
		for _, f := range strings.Fields(m[1]) {
			f = strings.TrimSpace(f)
			if runOnModelRE.MatchString(f) {
				models = append(models, f)
			}
		}
		if len(models) >= 2 {
			return models, i + 1
		}
	}
	return nil, 0
}

func runOnRoleOf(line string) (ColumnRole, bool) {
	for _, r := range runOnRoles {
		if r.re.MatchString(line) {
			return r.role, true
		}
	}
	return RoleOther, false
}

func sameCurrency(amounts [][]string) bool {
	sym := amounts[0][1]
	for _, a := range amounts[1:] {
		if a[1] != sym {
			return false
		}
	}
	return true
}

func currencyOfSymbol(sym string) string {
	switch sym {
	case "€":
		return "EUR"
	case "£":
		return "GBP"
	case "¥":
		return "CNY"
	default:
		return "USD"
	}
}

// runOnDiagnosis 报出一个被拒的 run-on 价格行。
//
// ★ 它**必须出现**：这条路径存在的全部理由就是「钱不许静默消失」。
//
//	一个人如果只看到「这一页没有候选」，会以为「原厂没公布」—— 而真相是
//	「解析器不敢认」。
func runOnDiagnosis(vendor, sourceURL, model, line string, lineNo int, why string) Candidate {
	c := Candidate{
		Vendor:     vendor,
		SourceURL:  sourceURL,
		Model:      model,
		LineNo:     lineNo,
		Row:        line,
		Unit:       UnitPer1M,
		Confidence: ConfidenceUnusable,
		Warnings: []string{"this page has no markdown table; it is a run-on price sheet and " +
			"this line was NOT read: " + why},
	}
	if amounts := amountRE.FindAllStringSubmatch(line, -1); len(amounts) > 0 {
		c.Currency = currencyOfSymbol(amounts[0][1])
		c.Warnings = append(c.Warnings,
			"this line carries money but was not parsed: either the page publishes a layout "+
				"this extractor does not model, or the column attribution was not provable — "+
				"the amounts below are NOT accounted for anywhere in this proposal")
	}
	return c
}

// markTiered 把「同一 (模型, 角色) 出现多次」这一事实写进候选。
func markTiered(c *Candidate) {
	c.Confidence = ConfidenceUnusable
	c.Tier = "context or product tier"
	c.Warnings = append(c.Warnings,
		"the same model appears more than once for this price role on this page, so the price "+
			"is tiered; a tiered price is not the flat list price and must not enter the SSOT")
}
