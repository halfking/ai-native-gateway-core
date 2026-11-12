package sanitize

// r1046（2026-11-13，211 rv-1735）：银行卡形状的可信度守卫。
//
// 生产实证（磁盘清理会话 gw_64107a4c…，minimax-m3.1-flash thinking lane）：
//
//	· 输入面一晚把 ps/du 输出里的 Chrome 数字 ID（--variations-seed-version /
//	  --field-trial-handle 的 16-19 位串）铸成 550 个 credit_card 占位符
//	  ⇒ 模型连自己上下文里的进程 ID / 字节数都看不见（反复重新推理的
//	  直接原因之一）；
//	· 模型在 thinking 尾部复述其中一个 16-19 位数字 ⇒ 输出面
//	  financial.bank_card（快照 v2 里的**r1039 修复前旧正则**：
//	  `\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4,7}\b`＝任意 16-19 位数字串）
//	  想遮蔽 ⇒ thinking 是签名 lane 不可改写 ⇒ rewrite_refused 杀整回合
//	  （rv-1735，用户报障）。
//
// 根因是**三层载体只有一层被修过**：r1039（2026-10-20）把
// configs/sensitive_patterns.yaml 的 bank_card 收窄成 IIN 前缀族+长度，但
// 生产生效的是 DB 规则快照 v1(10-16)→v2(10-29) 血统——v1 在 r1039 之前
// 切出，旧正则被快照冻结，YAML 的修复对已发布快照不生效（与 §694
// 「重写视图前必须以最后一个改它的迁移为基线」、r1043「同一语义多载体」
// 同族）。本守卫与 r1042 的 git-remote/email 豁免同层：**引擎级判据，
// 对所有规则来源（内置 fallback / YAML / 已发布快照）同口径生效**，不动 DB。
//
// 判据（「形状像卡」≠「是卡」——补上两条卡号必过、数字 ID/字节数几乎
// 必不过的检验）：
//
//	① IIN 前缀族 + 长度：Visa 4 / Mastercard 51-55 / Discover 6011,65 /
//	  UnionPay 62 / JCB 35 → 16-19 位；AmEx 34,37 → 恰 15 位；
//	② Luhn 校验和通过。
//
// ★ 取舍（与 r1039 的 yaml 注释同款，明确写下）：不以已知 IIN 开头、或
// 长度不符、或 Luhn 不过的数字串**不再**被当卡号——时间戳、纳秒 tick、
// Chrome 进程参数 ID、字节数、流水号全部出局。这些正是 550 例误遮蔽的
// 全部来源。真卡号（含四位分组空格/连字符形态）不受影响（钉测钉住）。
// 自定义规则若把 type=credit_card 用于「内部固定格式账号」（非真卡），
// 会被本守卫一并收窄——内部号请用 custom/account 类型承载。
func credibleCardCandidate(value string) bool {
	digits := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		switch c := value[i]; c {
		case ' ', '\t', '-':
			// 四位分组之间的分隔符：真卡排版形态，剥掉再验。
		default:
			if c < '0' || c > '9' {
				return false
			}
			digits = append(digits, c)
		}
	}
	n := len(digits)
	if n < 15 || n > 19 {
		return false
	}
	if !cardIINFamily(digits) {
		return false
	}
	// Luhn：从校验位（最右）起，偶数位翻倍求和，总和模 10 必须为 0。
	var total int
	for i := 0; i < n; i++ {
		d := int(digits[n-1-i] - '0')
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		total += d
	}
	return total%10 == 0
}

// cardIINFamily 校验「去掉分隔符后的纯数字串」是否落在真实发卡行前缀族
// 且长度匹配（AmEx 恰 15 位；其余族 16-19 位）。
func cardIINFamily(digits []byte) bool {
	n := len(digits)
	amex := n == 15 && (digits[0] == '3' && (digits[1] == '4' || digits[1] == '7'))
	if amex {
		return true
	}
	if n < 16 || n > 19 {
		return false
	}
	switch {
	case digits[0] == '4': // Visa
		return true
	case digits[0] == '5' && digits[1] >= '1' && digits[1] <= '5': // Mastercard
		return true
	case digits[0] == '6' && digits[1] == '0' && digits[2] == '1' && digits[3] == '1': // Discover 6011
		return true
	case digits[0] == '6' && digits[1] == '5': // Discover 65xx
		return true
	case digits[0] == '6' && digits[1] == '2': // UnionPay
		return true
	case digits[0] == '3' && digits[1] == '5': // JCB
		return true
	default:
		return false
	}
}

// standaloneDigitRun 判定匹配片段是否是**独立的数字串**（左右两侧都不是数字）。
//
// r1046 同族第二层：快照血统里的 legacy compliance.pii.* 规则（pii_patterns
// 表导入）全部没有词边界——`\d{17}[\dXx]`、`\d{15}`、`1[3-9]\d{9}`、
// `\d{16,19}` 都会在**更长的数字串内部**截一段当 PII：19 位 Chrome 进程
// ID 里的第 1-18 位被当身份证、第 1-11 位被当手机号。一个「更长数字串的
// 子串」几乎从不是 PII——真手机号/身份证/卡号被数字直接粘住的概率可以
// 忽略（分隔符/文字边界才是常态），而数字 ID 互相粘连是常态（命令行、
// 时间戳、字节数）。
//
// 该检查对带 \b 的严格规则（pii.phone / pii.id_card）是恒真的空操作，
// 只收掉无边界规则的子串命中。
func standaloneDigitRun(text string, start, end int) bool {
	if start > 0 && isASCIIDigit(text[start-1]) {
		return false
	}
	if end < len(text) && isASCIIDigit(text[end]) {
		return false
	}
	return true
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// credibleIDCardCandidate 校验值是否是**结构合法的中国居民身份证号**（18 位）：
// 省级行政区前缀 + 合法出生日期（YYYYMMDD）+ 末位校验码（数字或 Xx）。
//
// r1046：legacy `\d{15}`（15 位老身份证——现行检测面从未真正支持过它的
// 语义）与无边界 `\d{17}[\dXx]` 把任意 15/18 位数字串当身份证——Chrome
// 进程 ID、纳秒时间戳、字节数全部中招。结构判据（省级前缀表 + 真实日历
// 日期）是「形状像」到「结构是」的最小收窄：
//
//	· 前缀表＝GB/T 2260 省级代码前两位（11-15/21-23/31-37/41-46/50-54/
//	  61-65/71/81/82）；
//	· 日期＝1800-01-01 至 2099-12-31 的真实日历日（含闰二月）；
//	· 校验码（第 18 位）不做 MOD 11-2 校验——人们常写「改过几位」的
//	  匿名化身份证（结构在、校验不过），隐私面把它们继续遮蔽是合理默认；
//	  该取舍与快照 pii.id_card 的 (18|19|20) 世纪约束同宽。
func credibleIDCardCandidate(value string) bool {
	if len(value) != 18 {
		return false
	}
	for i := 0; i < 17; i++ {
		if !isASCIIDigit(value[i]) {
			return false
		}
	}
	if !isASCIIDigit(value[17]) && value[17] != 'X' && value[17] != 'x' {
		return false
	}
	if !cnProvincePrefix(value[:2]) {
		return false
	}
	year := int(value[6]-'0')*1000 + int(value[7]-'0')*100 + int(value[8]-'0')*10 + int(value[9]-'0')
	month := int(value[10]-'0')*10 + int(value[11]-'0')
	day := int(value[12]-'0')*10 + int(value[13]-'0')
	if year < 1800 || year > 2099 || month < 1 || month > 12 || day < 1 {
		return false
	}
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return day <= 31
	case 4, 6, 9, 11:
		return day <= 30
	default: // 二月
		leap := year%4 == 0 && (year%100 != 0 || year%400 == 0)
		if leap {
			return day <= 29
		}
		return day <= 28
	}
}

// cnProvincePrefix 校验两位省级代码（GB/T 2260 前两位）。
func cnProvincePrefix(prefix string) bool {
	switch prefix {
	case "11", "12", "13", "14", "15", // 华北
		"21", "22", "23", // 东北
		"31", "32", "33", "34", "35", "36", "37", // 华东
		"41", "42", "43", "44", "45", "46", // 中南
		"50", "51", "52", "53", "54", // 西南
		"61", "62", "63", "64", "65", // 西北
		"71", "81", "82": // 台湾 / 香港 / 澳门
		return true
	}
	return false
}
