package sanitize

// r1040_builtin_pattern_address_family_test.go — 2026-10-28：
// **内置 YAML 规则名 → 类型**这条映射（第四面）不许悄悄长出新的地址族。
//
// ## 背景（r1040 普查的发现）
//
// 全仓「谁在判断某类型算不算地址族」的普查
// （`cmd/tools/address_family_decision_audit`）找到四个面：
//
//	① 脱敏面豁免     security/sanitize/detector.go 的 scopedType      → 调 SSOT
//	② 合规面只检出   domains/outputcompliance 的 isAddressPIIType      → 调 SSOT
//	③ legacy 导入    domains/sanitizerules 的 LegacyPIIRule            → 调 SSOT（规范化入口）
//	④ **内置 YAML 规则名 → 类型**  security/sanitize/detector_config.go 的
//	   `sensitiveTypeForPattern`                                      → **不调 SSOT**
//
// ④ 判的是**规则名**（YAML 里的 `patterns:` 键）而不是**声明类型**
// （运维写的 `pii_patterns.pattern_type`）⇒ 它不是同一个问题；但它同样能决定
// 一条内置规则**算不算地址族**：名字 `ip` / `domain` / `hostname` 会落进
// `TypeCustom` ⇒ 脱敏面**照遮** —— 与用户口径相违，而且**没有任何东西会报警**。
//
// ## 这道门钉的是什么
//
// 1. 内置 YAML 里凡是**名字**属于地址族的规则，其类型**必须**也是地址族
//    ⇒ 将来有人往 `configs/sensitive_patterns.yaml` 里加一条 `ip:` 规则，
//    门立刻红（而不是让它静默变成"要遮蔽的域名"）。
// 2. 地址族名字的**集合**必须与登记的一致 ⇒ 新增名字必须先裁决。
//
// ## ★★ 一处必须写清的**同词异义**
//
// 同一个词 `address` 在两个体系里指的是**两件事**：
//
//	SSOT 的 `address`  = **网络地址**（与 ip / domain 并列）⇒ 不遮蔽
//	YAML 的 `address`  = **中文邮政地址**（pii.address）⇒ **要遮蔽**
//
// 后者是 r1036（2026-10-20）**明确冻结**的行为（属主裁决项，见
// `docs/audit/2026-10-28-r1040-地址族判定点普查.md` 的裁决备忘）。
// ⇒ 本门把 YAML 的 `address` 登记成**例外**：它必须保持 `custom`（照遮）。
// ⇒ 这条例外是**冻结项**，不是「暂时没管」：改它必须走属主裁决。
//
// ★ 变异验证：
//   M1 把 YAML 的 `address` 规则改成 server_ip（= 取消冻结）⇒ 判据 1 红；
//   M2 在 YAML 里加一条 `ip:` 规则（落进 TypeCustom）⇒ 判据 1 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1040BuiltinAddressFamilyNames 是**内置 YAML 里被允许出现**的地址族名字，
// 以及每个名字**必须**落成的类型。
//
// ★ `address` 是**登记在册的例外**：YAML 里的它指中文邮政地址（r1036 冻结 ⇒ 照遮），
// 与 SSOT 里的网络地址 `address` **同词异义**。
var r1040BuiltinAddressFamilyNames = map[string]string{
	"address":   "custom", // ★ 例外：中文邮政地址（pii.address，r1036 冻结）⇒ 照遮
	"server_ip": "server_ip",
	// server_ipv6 是**同一个**内置族里的 IPv6 条目；`sensitiveTypeForPattern`
	// 把它也落成 server_ip（typeServerIPv6 只是 YAML 规则名常量，
	// IPv6 字面量永远不分配占位符，所以不单列一个类型）。
	"server_ipv6": "server_ip",
}

// r1040BuiltinPatternName 取出 patternEntry 的 YAML 规则名。
//
// 形状来自 detector_config.go：`id = category.name + "." + name`。
func r1040BuiltinPatternName(id string) string {
	if i := strings.LastIndex(id, "."); i >= 0 {
		return id[i+1:]
	}
	return id
}

// r1040BuiltinPatterns 加载内置 YAML 基线。
func r1040BuiltinPatterns(t *testing.T) []patternEntry {
	t.Helper()
	path := filepath.Join("..", "..", "configs", "sensitive_patterns.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取内置规则基线失败（%s）：%v", path, err)
	}
	entries, err := parsePatternConfig(data)
	if err != nil {
		t.Fatalf("解析内置规则基线失败（%s）：%v", path, err)
	}
	if len(entries) == 0 {
		t.Fatal("内置规则基线解析出 0 条规则 ⇒ 前提失效（不是「没有规则」）")
	}
	return entries
}

// TestR1040BuiltinAddressNamedRulesMustBeAddressFamily 钉住判据 1。
func TestR1040BuiltinAddressNamedRulesMustBeAddressFamily(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range r1040BuiltinPatterns(t) {
		name := r1040BuiltinPatternName(e.id)
		want, isAddressNamed := r1040BuiltinAddressFamilyNames[name]
		if !isAddressNamed {
			continue
		}
		seen[name] = true
		if string(e.sType) != want {
			t.Errorf("内置规则 %q 的类型是 %q，期望 %q ⇒ 内置 YAML 的地址族口径漂了", name, e.sType, want)
		}
	}
	// 判据 2：地址族名字的集合必须与登记的一致。
	for name := range r1040BuiltinAddressFamilyNames {
		if !seen[name] {
			t.Errorf("登记在册的内置地址族规则 %q 不在 YAML 里了 ⇒ 登记与实测漂移（规则被删/被改名）", name)
		}
	}
}

// TestR1040NoUnregisteredAddressNamedBuiltinRule 钉住判据 1 的另一半：
// YAML 里出现**未登记**的地址族名字 ⇒ 红（新增必须先裁决，不能静默生效）。
//
// ★ 这条门是有牙齿的：往 configs/sensitive_patterns.yaml 里加一条 `ip:` 规则，
// 它会被 `sensitiveTypeForPattern` 落成 `TypeCustom` ⇒ 脱敏面照遮域名/ip
// ⇒ 本门在**加载期**就红，而不是等有人发现线上遮掉了地址。
func TestR1040NoUnregisteredAddressNamedBuiltinRule(t *testing.T) {
	for _, e := range r1040BuiltinPatterns(t) {
		name := r1040BuiltinPatternName(e.id)
		if !sanitizerules.IsAddressFamilyType(name) {
			continue
		}
		if _, ok := r1040BuiltinAddressFamilyNames[name]; !ok {
			t.Errorf("内置 YAML 新增了地址族名字 %q（规则 id=%q，类型=%q）⇒ 必须先裁决并登记：\n"+
				"  · 若它是**网络定位符** ⇒ 类型必须是地址族（server_ip/internal_ip…）\n"+
				"  · 若它是**中文邮政地址** ⇒ 那与 r1036 的冻结项同名，须显式登记为例外\n"+
				"  · 不登记就静默生效 = 脱敏面开始遮蔽本不该遮的东西",
				name, e.id, e.sType)
		}
	}
}
