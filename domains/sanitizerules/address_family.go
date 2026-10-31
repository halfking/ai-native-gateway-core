package sanitizerules

// address_family.go — 「地址族」的**唯一判定口径**。
//
// ## 为什么要有这个包（r1038b，2026-10-27）
//
// 「地址族 = 域名 / 地址 / ip」这条口径在仓内**被写了两遍**：
//
//  1. `security/sanitize/detector.go` 的 `scopedType`（脱敏面豁免用）；
//  2. `domains/outputcompliance/checker.go` 的 `isAddressPIIType`
//     （输出合规面「只检出不遮蔽」用）。
//
// 两份名单**已经漂移**：第 1 份只有 server_ip / internal_ip / server_ipv6，
// 第 2 份多了 ip / ip_address / ipv6 / address / domain / hostname。
// 漂移的后果是**同一个类型在两个面得到相反待遇** —— 例如一条 `domain` 类型的
// 规则：在输出合规面被当作地址族（只检出、不遮蔽），在脱敏面却不是地址族
// ⇒ 照样被遮蔽。用户口径是「脱敏的范围应该不包含域名、地址、ip」，
// **两个面必须同口径**。
//
// ⇒ 口径收进本包，两面各调同一个函数。清单是两份名单的**并集**
// （只增不减 ⇒ 不会意外收紧任何一面的豁免）。
//
// ## 清单里**故意没有**的东西
//
//   - `url`：URL 可以携带凭据（`?token=…` / `/user/<secret>`）。把「URL」
//     整类从遮蔽面豁免，等于给查询串里的令牌开后门。**要遮**。
//   - `custom`：运维显式自定义的规则。豁免按**声明的类型**判定，不按正则
//     内容判定 —— 否则一条「自定义规则」只要碰巧长得像域名就放行，攻击面
//     反而变大。**要遮**。
//   - `address` 在这里指的是**网络地址**（与 ip / domain 并列）。中文邮政
//     地址是另一条规则（`pii.address`，r1036 明确冻结其行为），不在这份
//     名单里。

import "strings"

// AddressFamilyTypes 返回「地址族」的类型清单（副本，调用方改它不影响本包）。
//
// 顺序固定，便于日志与判据输出稳定。
func AddressFamilyTypes() []string {
	return []string{
		"ip",
		"ip_address",
		"ipv6",
		"server_ip",
		"server_ipv6",
		"internal_ip",
		"domain",
		"hostname",
		"address",
	}
}

// IsAddressFamilyType 判定某个类型是否属于地址族。
//
// 大小写与空白不敏感（规则类型可能来自 YAML / DB / 手工录入）。
func IsAddressFamilyType(typ string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	if t == "" {
		return false
	}
	for _, v := range AddressFamilyTypes() {
		if t == v {
			return true
		}
	}
	return false
}
