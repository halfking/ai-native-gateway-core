package sanitizerules

// legacy_baseline.go — legacy `pii_patterns` 行 → 托管规则的**生产映射**。
//
// ## 为什么住在这个包里
//
// 这段映射原先住在 `cmd/gateway`（package main）里，**无法被测试直接调用**
// ⇒ 任何想验证「生产真实规则集长什么样」的判据都只能**手抄一份映射**，
// 抄的那份和生产那份会漂移（§LESSONS「量具口径比事实源窄 ⇒ 伪绿」的同族）。
//
// r1038b（2026-10-27）把它下沉到 `domains/sanitizerules`：
//   - 生产侧（cmd/gateway 的 loadSanitizeRulesBaseline）调用**同一个函数**；
//   - 判据侧（security/sanitize 的 r1038b_* 门禁）也调用**同一个函数**；
//   ⇒ 判据锚在**生产事实源**上，不是抄件。
//
// ## 形状约定
//
//   - 类型映射：`ip` / `ip_address` / `ipv6` → `server_ip`；`bank_card` →
//     `credit_card`；已知类型原样；**未知类型一律 `custom`**（最后一条
//     有一个已知副作用，见下方「已知缺口」）。
//   - 作用域：`["output"]`（legacy 行只作用于输出面）。
//   - 动作：默认 `mask`；**地址族（server_ip / internal_ip）一律 `log`**。
//
// ## 地址族为什么是 log 而不是 mask（r1038，2026-10-20）
//
// 316 播种的 `ip_address` 行（正则 `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`，
// 连版本号与环回地址一并命中）若按默认 `mask` 落到输出面，会把**每个** IPv4
// 遮成 `***.***.***.***`——「export https_proxy=http://127.0.0.1:7890」
// 这类**可复制执行**的命令变成坏命令。
//
// 851 迁移停用了那一行，但那是**数据**修复：基线快照不可变，851 之前发布的
// 部署拿到的快照里那行仍然是 enabled ⇒ 遮蔽会一直存在，直到有人手工重发
// 一版规则。所以形状本身必须正确（这里），同时 `security/sanitize` 的
// `DetectionScope` 另有一道**代码级**保证（地址族在脱敏面一律放行）。
// 两层都不依赖运维记得停用哪一行。
//
// ## 已知缺口（r1038b 记录，未擅自改）
//
// `default: typ = "custom"` 这条分支把**所有未知类型**降级成 `custom`。
// `custom` **不在**地址族豁免名单里（豁免按**类型**判定，不按正则内容判定）。
// 于是：若有人给 `pii_patterns` 增一行 `pattern_type='domain'` / `'url'` /
// `'hostname'`，它会被降级成 `custom` ⇒ 在脱敏面**仍然遮蔽**域名/URL，
// 与用户口径「脱敏的范围应该不包含域名、地址、ip」相违。
//
// 生产上当前没有这种行（`pii_patterns` 实测 12 行，类型只有 email / phone /
// id_card / credit_card / ssn / ip / bank_card），所以这是**潜在**缺口而不是
// 现行缺陷。修法见 `r1038b_legacy_baseline_test.go` 的对照说明。

import "strconv"

// LegacyPIIRule 把 legacy `pii_patterns` 的一行造造成托管规则。
//
// 生产调用点：cmd/gateway/sanitize_rules_control.go 的 loadSanitizeRulesBaseline。
// 判据调用点：security/sanitize 的 r1038b 门禁（用**生产真实种子**复现
// 「部署拿到的规则集」，而不是手搓一条合成规则）。
func LegacyPIIRule(id int, name, typ, pattern string, enabled bool) Rule {
	// Keep the same canonical type mapping as Checker.RuleConfig.
	switch typ {
	case "bank_card":
		typ = "credit_card"
	case "ip", "ip_address", "ipv6":
		typ = "server_ip"
	case "domain", "hostname", "address":
		// r1038b（2026-10-27）：地址族声明**不许被降级成 custom**。
		//
		// 此前这三种类型都落进 `default: typ = "custom"` ⇒ 运维在
		// `pii_patterns` 里声明「这条规则抓的是域名」，类型却被**静默丢弃**
		// ⇒ 脱敏面照遮域名，与用户口径「脱敏的范围应该不包含域名、地址、
		// ip」相违。而 `custom` 不在地址族豁免名单里（豁免按**声明类型**
		// 判定，不按正则内容判定）。
		//
		// 映射到 `server_ip` 而不是新造一个 `domain` 类型：**规则类型词表是
		// 封闭的**（Compile 只接受 phone/id_card/email/credit_card/secret/
		// internal_ip/server_ip/account/name/custom 这 10 个）⇒ 造新类型会让
		// **整份规则集装载失败**（实测：`invalid sanitize rule configuration:
		// rules[0].type must be a restorable sensitive type`）。
		// `server_ip` 是仓内既有的「网络定位符」类型（legacy 导入已经把
		// ip/ip_address/ipv6 全映射成它）⇒ 沿用同一约定，语义与词表都不破。
		//
		// ★ `url` **不**在这里：URL 能携带凭据（`?token=…`），整类豁免等于
		// 给查询串里的令牌开后门 ⇒ 它继续走 default（custom，要遮）。
		typ = "server_ip"
	case "phone", "id_card", "email", "credit_card", "secret", "account", "name", "internal_ip", "server_ip", "custom":
	default:
		typ = "custom"
	}
	action := "mask"
	if IsAddressFamilyType(typ) {
		// 地址族：只记录、不遮蔽（与脱敏面的豁免同口径，读同一份名单）。
		action = "log"
	}
	return Rule{
		ID: "compliance.pii." + strconv.Itoa(id), Name: name, Type: typ,
		Kind: "pattern", Pattern: pattern, Scopes: []string{"output"}, Action: action, Enabled: enabled,
	}
}

// LegacyAddressFamilyTypes 是「地址族」在托管规则类型上的集合。
//
// 脱敏面的地址族豁免（security/sanitize 的 scopedType）与 legacy 映射的
// 「action=log」判定**必须用同一份名单**——否则会出现「一边认为它是地址、
// 另一边认为它要遮蔽」的裂口。两处都读这里。
func LegacyAddressFamilyTypes() []string {
	return AddressFamilyTypes()
}
