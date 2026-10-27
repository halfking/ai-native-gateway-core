package outputcompliance

// r1038b_address_family_parity_test.go — 2026-10-27 批判式审计：
// 脱敏面与输出合规面**必须用同一份地址族名单**。
//
// ## 背景（r1038b 取证）
//
// 「地址族 = 域名 / 地址 / ip」这条口径在仓内被写了两遍：
// `security/sanitize` 的 `scopedType` 与本包的 `isAddressPIIType`。
// 两份名单**已经漂移**：本包多出 ip / ip_address / ipv6 / address /
// domain / hostname，那边没有 ⇒ **同一个类型在两个面得到相反待遇**
// （一条 domain 类型的规则：在合规面「只检出不遮蔽」，在脱敏面照遮）。
//
// r1038b 把口径收进 `domains/sanitizerules.IsAddressFamilyType`，两个面各调
// 同一个函数。本文件钉住「它们真的调的是同一个」——
// **不是**再抄一份名单（那会重新引入漂移），而是：
//
//  1. 本包的 `isAddressPIIType` 对**名单里的每一个类型**都必须返回 true；
//  2. 对**不该豁免的类型**（url / custom / secret …）必须返回 false
//     —— 豁免名单里混进 url 就是给查询串里的凭据开后门。
//
// ★ 变异验证：
//   M1 把本包的 `isAddressPIIType` 改回本地列表（删掉 domain）⇒ 判据 1 转红；
//   M2 把 `url` 加进共享名单 ⇒ 判据 2 与 legacy 侧的 url 反向对照同时转红。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func TestR1038bOutputComplianceSharesTheSameAddressFamilyList(t *testing.T) {
	for _, typ := range sanitizerules.AddressFamilyTypes() {
		if !isAddressPIIType(typ) {
			t.Errorf("类型 %q 在共享名单里，但本包的 isAddressPIIType 说不是地址族 ⇒ 两个面口径漂移", typ)
		}
	}
}

func TestR1038bOutputComplianceDoesNotExemptCredentialBearingTypes(t *testing.T) {
	for _, typ := range []string{"url", "custom", "secret", "account", "email", "phone", "credit_card", "id_card"} {
		if isAddressPIIType(typ) {
			t.Errorf("类型 %q 被当地址族豁免了（安全后门：URL/凭据类不得放行）", typ)
		}
	}
}
