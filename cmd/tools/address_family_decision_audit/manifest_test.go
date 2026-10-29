package main

// manifest_test.go — 2026-10-28（r1040）：全仓「谁在判断某类型算不算地址族」
// 的**判定清单**。形态取自 `cmd/tools/sql_source_indirection_audit`
// （**工具算、清单写，两者互相监督**）。
//
// # 这道门补的是什么
//
// r1038b（2026-10-27）把地址族口径收进 `domains/sanitizerules.IsAddressFamilyType`，
// 并把**当时已知的两个面**改成调它。
//
// ★ 但「口径收进 SSOT」只保证**已知的判定点**调同一个函数，**不保证下一个**。
// r1040 普查发现**第三个面**（`domains/outputcompliance/managed_rules.go` 的
// `Checker.RuleConfig`）持有一份自己的类型映射副本，而且**已经漂移**：
//
//	声明 domain / hostname / address
//	  → legacy 导入：(server_ip, log)  只记录不遮蔽
//	  → 合规面导出：(custom, mask)     照遮蔽
//
// ⇒ 同一个声明类型，两条路径**相反待遇** —— r1038b 的 D1 在新位置上复发。
//
// ★ **它为什么当初没被发现**：r1038b 的判据是「两个面必须同口径」，
// 而那条判据**只钉住了它当时知道的那两个面**。没有任何东西在问
// 「**全仓还有没有别的面**」⇒ 第三面既没进过工单，也没被门看过。
//
// ⇒ 这道门就是那个「还有没有别的面」的问句：
//
//	① 实测（AST 扫描全仓）↔ 清单（人读源码写的判定）**双向对拍**；
//	② 清单里**不许有未分类**的条目（棘轮）；
//	③ **生产代码里不许再出现第二份地址族名单/分支**——
//	   例外只有两个，且都要登记：量具自己的副本、已审阅的例外（内置 YAML 词表）；
//	④ **仓内只允许有一份地址族名单**（SSOT 那一份）。
//
// # 分类词表
//
//	ssot-bound          调 SSOT 的判定面（正牌）
//	definition-site     SSOT 包内的定义点（名单 / 规范化 / 词表）
//	reverse-assertion   测试里的**反向断言清单**（独立复述名单 ⇒ SSOT 少一项就红）
//	measuring-instrument 量具自己的副本（必须与 SSOT 一致，由另一道门把住）
//	not-a-decision      误报：同名不同义（DB 列名 / 响应字段名）
//	reviewed-exception  已审阅的例外（必须写明为什么 + 由哪道行为门把住）

import (
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// 分类常量。
const (
	classSSOTBound           = "ssot-bound"
	classDefinitionSite      = "definition-site"
	classReverseAssertion    = "reverse-assertion"
	classMeasuringInstrument = "measuring-instrument"
	classNotADecision        = "not-a-decision"
	classReviewedException   = "reviewed-exception"
)

// ssotPackage 是 SSOT 所在包（仓内**唯一**允许持有地址族名单的地方）。
const ssotPackage = "domains/sanitizerules/"

// siteAssessment 是清单里的一条判定。
type siteAssessment struct {
	// Class 是上面词表里的分类。必填。
	Class string
	// Why 是理由。必填非空——**空理由的登记等于没有登记**。
	Why string
}

// addressFamilySiteAssessments 是全仓地址族判定面的**判定清单**。
//
// 键是 `文件|函数|形态`（形态见 main.go 的 Kind）。
// ★ 判定依据是**逐个读源码**，不是抄工具输出——工具只负责「有哪些面」，
// 「这个面是什么」由人读源码写在这里。
var addressFamilySiteAssessments = map[string]siteAssessment{
	// ── 调 SSOT 的判定面（正牌）─────────────────────────────────────────
	"cmd/gateway/sanitize_rules_control.go|loadSanitizeRulesBaseline|canonical-call": {
		Class: classSSOTBound,
		Why: "生产侧 legacy `pii_patterns` 导入：把 DB 行造成托管规则。" +
			"类型映射与地址族豁免都走 domains/sanitizerules.LegacyPIIRule。",
	},
	"domains/outputcompliance/checker.go|isAddressPIIType|membership-call": {
		Class: classSSOTBound,
		Why: "合规面「只检出不遮蔽」的判定（detectPII 用它算 DetectOnly）。" +
			"r1038b 起读 SSOT。",
	},
	"domains/outputcompliance/managed_rules.go|RuleConfig|canonical-call": {
		Class: classSSOTBound,
		Why: "★ r1040 的**第三面**：此前这里有一份自己的类型映射副本，与 SSOT 那份" +
			"已漂移（domain/hostname/address 一边 log 一边 mask）⇒ 现在删掉副本，" +
			"与 legacy 导入调**同一个** LegacyPIIRule（同一张表的同一种行）。",
	},
	"security/sanitize/detector.go|scopedType|membership-call": {
		Class: classSSOTBound,
		Why:   "脱敏面的地址族豁免（r1038 起）。r1038b 起读 SSOT。",
	},
	// ── 调 SSOT 的门禁（测试侧）──────────────────────────────────────────
	"domains/outputcompliance/r1038b_address_family_parity_test.go|TestR1038bOutputComplianceSharesTheSameAddressFamilyList|list-call": {
		Class: classSSOTBound,
		Why:   "r1038b 的两面同口径门禁：遍历 SSOT 名单，钉住合规面真的认每一项。",
	},
	"domains/outputcompliance/r1040_address_family_canonical_parity_test.go|TestR1040ManagedExportAndLegacyImportAgreeOnEveryDeclaredType|canonical-call": {
		Class: classSSOTBound,
		Why:   "r1040 的**行为对拍**门：两条路径对同一批声明类型必须给出同样的 (type, action)。",
	},
	"domains/outputcompliance/r1040_address_family_canonical_parity_test.go|TestR1040DeclaredAddressFamilyIsNeverMasked|list-call": {
		Class: classSSOTBound,
		Why:   "r1040 的口径门：地址族导出成 action=log（只记录不遮蔽）。",
	},
	"security/sanitize/r1038b_real_shape_test.go|r1038bProductionRuleSet|canonical-call": {
		Class: classSSOTBound,
		Why:   "r1038b 的门禁用**生产真实规则集**复现部署形态（legacy 行经 LegacyPIIRule）。",
	},
	"security/sanitize/r1038b_real_shape_test.go|TestR1038bProductionRuleSetNeverMasksLocator|membership-call": {
		Class: classSSOTBound,
		Why:   "r1038b 的属性门禁：遍历生产规则集，用 SSOT 判定哪些是地址族。",
	},
	"security/sanitize/r1040_builtin_pattern_address_family_test.go|TestR1040NoUnregisteredAddressNamedBuiltinRule|membership-call": {
		Class: classSSOTBound,
		Why:   "r1040 的内置 YAML 门：YAML 里出现**未登记**的地址族名字 ⇒ 红。",
	},
	"cmd/tools/address_family_decision_audit/manifest_test.go|TestAddressFamilyTypeNameCopyMatchesTheSSOT|list-call": {
		Class: classSSOTBound,
		Why:   "★ 这道门自己也要调 SSOT：量具里的副本必须与 SSOT 的名单逐项相等。",
	},

	// ── SSOT 包内的定义点（仓内**唯一**允许持有名单/分支的地方）─────────
	"domains/sanitizerules/address_family.go|AddressFamilyTypes|type-list": {
		Class: classDefinitionSite,
		Why: "★ **这份名单就是 SSOT 本身**。全仓只此一份（见" +
			"TestAddressFamilyListHasExactlyOneDefinitionSite）。",
	},
	"domains/sanitizerules/legacy_baseline.go|LegacyPIIRule|type-switch": {
		Class: classDefinitionSite,
		Why: "SSOT 的**规范化入口**：声明类型 → 托管规则类型（bank_card→credit_card、" +
			"ip/ip_address/ipv6→server_ip、domain/hostname/address→server_ip；" +
			"未知→custom；url 刻意不映射）。地址族与否由同一份名单判定。",
	},
	"domains/sanitizerules/model.go|Compile|type-switch": {
		Class: classDefinitionSite,
		Why: "★ **另一个问题**：规则的**类型词表**是封闭的（只有这 10 个类型可装载）。" +
			"它管的是「这个词表外的类型会让整份规则集装载失败」，不是「算不算地址族」。" +
			"（r1038b 坑②：曾把域名类型造成一个词表外的新类型 ⇒ 整份规则集起不来。）",
	},
	"domains/sanitizerules/events.go|structuralType|type-switch": {
		Class: classDefinitionSite,
		Why: "★ **另一个问题**：事件台账的**结构标签词表**（证据里能出现哪些类型名）。" +
			"它不决定遮不遮（词表外的类型落进 custom 是**证据**的降级，不是脱敏的豁免）。",
	},
	"domains/sanitizerules/builtin_rules_meta.go|BuiltinHardBlockAll|type-list": {
		Class: classNotADecision,
		Why: "2026-10-30 审计登记：这是**内置规则的 RuleID 词表**（\"builtin.\"+类型名 拼 " +
			"egress 允许项的 ID），不是「算不算地址族」的判定面——遮不遮由 SSOT 编译出的" +
			"规则本身决定。与 SSOT 类型的**拼写一致性**由 r1041 的清单闸守（未知 ID 被" +
			"当「不在清单里」，方向安全）；名单里混入的 server_ip/internal_ip 是 ID 词表" +
			"的成员，不是新写了一份地址族名单。",
	},

	// ── 量具自己的副本 ──────────────────────────────────────────────────
	"cmd/tools/address_family_decision_audit/main.go||type-list": {
		Class: classMeasuringInstrument,
		Why: "★ **故意的副本**：量具必须能**独立地**发现「某处自己写了一份名单」——" +
			"若它用 SSOT 的导出函数去找，抄件与原件长得一样就分不出来。" +
			"⇒ 副本与 SSOT 是否一致由 TestAddressFamilyTypeNameCopyMatchesTheSSOT 把住。",
	},

	// ── 已审阅的例外（生产代码里唯一允许自己写类型分支的地方）──────────
	"security/sanitize/detector_config.go|sensitiveTypeForPattern|type-switch": {
		Class: classReviewedException,
		Why: "★ **第四面**：内置 YAML 的**规则名** → 类型（server_ip/server_ipv6→server_ip、" +
			"internal_ip→internal_ip，其余落进 custom）。它判的是**规则名**不是**声明类型**，" +
			"所以不是同一个问题；但它同样能决定一条内置规则算不算地址族。" +
			"⇒ 由 security/sanitize/r1040_builtin_pattern_address_family_test.go 把住：" +
			"YAML 里出现未登记的地址族名字 ⇒ 红。" +
			"★ 登记在册的例外：YAML 的 `address` 指**中文邮政地址**（pii.address，r1036 冻结 ⇒ 照遮），" +
			"与 SSOT 的 `address`（**网络**地址）**同词异义**。",
	},

	// ── 反向断言清单（测试里独立复述名单 ⇒ SSOT 少一项就红）────────────
	"domains/outputcompliance/managed_rules_test.go|TestLegacyPIITypesImportAsRestorableConfiguration|type-list": {
		Class: classReverseAssertion,
		Why:   "列举 legacy 声明类型，钉住「导出的规则必须装载得上」。",
	},
	"domains/outputcompliance/r1040_address_family_canonical_parity_test.go||type-list": {
		Class: classReverseAssertion,
		Why: "★ 对拍门的**声明类型电池**：必须**独立复述**一遍名单，" +
			"否则 SSOT 少一项时两边一起绿。",
	},
	"domains/outputcompliance/r1040_address_family_canonical_parity_test.go|TestR1040DeclaredAddressFamilyIsNeverMasked|type-list": {
		Class: classReverseAssertion,
		Why:   "口径门的地址族断言清单（独立复述）。",
	},
	"domains/sanitizerules/legacy_baseline_test.go|TestR1038LegacyPIIRuleMarksAddressFamilyAsLog|type-list": {
		Class: classReverseAssertion,
		Why:   "r1038 的断言清单（独立复述）。",
	},
	"domains/sanitizerules/legacy_baseline_test.go|TestR1038bLegacyDeclaredAddressTypesAreNotDowngradedToCustom|type-list": {
		Class: classReverseAssertion,
		Why:   "r1038b 的断言清单（独立复述）。",
	},
	"domains/sanitizerules/legacy_baseline_test.go|TestR1038bLegacyProducedRulesMustCompile|type-list": {
		Class: classReverseAssertion,
		Why:   "r1038b 的断言清单（独立复述）。",
	},
	"domains/sanitizerules/legacy_baseline_test.go|TestR1038bAddressFamilyListIsSharedByBothFaces|type-list": {
		Class: classReverseAssertion,
		Why:   "r1038b 的名单边界断言（独立复述）。",
	},
	"security/sanitize/r1040_builtin_pattern_address_family_test.go||type-list": {
		Class: classReverseAssertion,
		Why:   "内置 YAML 的地址族名字登记簿（地址族名字 + 必须落成的类型）。",
	},

	// ── 误报：同名不同义 ────────────────────────────────────────────────
	"admin/ops_overview.go|queryOpsDeploymentNodes|type-list": {
		Class: classNotADecision,
		Why: "误报：这是运维面板响应的 **map 键**（hostname / ip_address 两个字段名），" +
			"不是规则类型。",
	},
	"internal/collector/allowlist.go||type-list": {
		Class: classNotADecision,
		Why: "误报：这是**禁收的遥测 JSON 键片段**（hostname / ip_address 等敏感字段名），" +
			"不是规则类型。",
	},
	"internal/opsreporter/reporter.go|registerFresh|type-list": {
		Class: classNotADecision,
		Why:   "误报：上报字段名清单（hostname / ip_address），不是规则类型。",
	},
}

// siteKey 是清单的键。
func siteKey(s Site) string { return s.File + "|" + s.Func + "|" + string(s.Kind) }

// measureSites 跑一次全仓普查，返回「键 → 实测面」。
func measureSites(t *testing.T, root string) map[string]Site {
	t.Helper()
	sites, err := AuditRepo(root)
	if err != nil {
		t.Fatalf("AuditRepo(%s): %v", root, err)
	}
	out := map[string]Site{}
	for _, s := range sites {
		out[siteKey(s)] = s
	}
	return out
}

// TestAddressFamilyDecisionSitesAreAllRegistered 是**双向对拍**：
//
//	① 实测有、清单无 ⇒ 红（新面无人判定）
//	② 清单有、实测无 ⇒ 红（判定过期 / 面被删 / 函数改名）
//	③ 分类不得为空、不得是未知分类；理由不得为空
func TestAddressFamilyDecisionSitesAreAllRegistered(t *testing.T) {
	root := repoRoot(t)
	measured := measureSites(t, root)

	var errs []string
	for key, site := range measured {
		a, ok := addressFamilySiteAssessments[key]
		if !ok {
			errs = append(errs, "★ 未登记的地址族判定面："+site.String()+
				"\n    ⇒ 新增的判定面必须先裁决：它调 SSOT 了吗？若没有，它凭什么可以自己写？")
			continue
		}
		if a.Class == "" {
			errs = append(errs, "登记条目 "+key+" 的分类为空 ⇒ 空分类等于没有判定")
		}
		if strings.TrimSpace(a.Why) == "" {
			errs = append(errs, "登记条目 "+key+" 的理由为空 ⇒ 空理由的登记等于没有登记")
		}
		if a.Class == classSSOTBound && site.Kind.Dangerous() {
			errs = append(errs, "登记条目 "+key+" 登记成 "+classSSOTBound+
				"，但实测形态是 "+string(site.Kind)+"（自己写了一份名单/分支）⇒ 分类与实测矛盾")
		}
	}
	for key := range addressFamilySiteAssessments {
		if _, ok := measured[key]; !ok {
			errs = append(errs, "★ 清单登记了但实测没有："+key+
				"\n    ⇒ 判定过期（面被删 / 改名 / 挪位）——请更新清单，不要留着一条查无此面的登记")
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		for _, e := range errs {
			t.Error(e)
		}
	}
}

// TestNoProductionFileMayCarryItsOwnAddressFamilyList 是这道门的**核心断言**：
// 生产代码（**非测试**、**非量具**）里不许再出现第二份地址族名单/分支。
//
// ⇒ 生产代码里允许出现的只有两类，且都要**逐个登记 + 写明理由**：
//
//	not-a-decision      经人读源码确认是**同名不同义**（DB 列名 / 响应字段名）
//	reviewed-exception  已审阅的例外（必须写明为什么 + 由哪道行为门把住）
//
// ⇒ 将来谁在业务代码里写一份「ip / domain / hostname …」的名单 ⇒ 这里立刻红。
//
// ★ **诚实边界**：`not-a-decision` 这一类是**人的判断**（人读源码确认它不是判定面）。
// 工具分不出「DB 列名 hostname」与「规则类型 hostname」——两者在 AST 里都是字符串字面量。
// ⇒ 这一类登记的价值是**留痕**（谁确认的、为什么），不是自动判定。
func TestNoProductionFileMayCarryItsOwnAddressFamilyList(t *testing.T) {
	root := repoRoot(t)
	measured := measureSites(t, root)
	for key, site := range measured {
		if !site.Kind.Dangerous() {
			continue
		}
		if strings.HasSuffix(site.File, "_test.go") {
			continue // 反向断言清单：独立复述是**要的**
		}
		if strings.HasPrefix(site.File, ssotPackage) {
			continue // SSOT 包：定义点
		}
		if strings.HasPrefix(site.File, "cmd/tools/address_family_decision_audit/") {
			continue // 量具自己
		}
		a, ok := addressFamilySiteAssessments[key]
		if !ok {
			t.Errorf("★ 生产代码里出现了一份自己的地址族名单/分支：%s\n"+
				"    ⇒ 「所有地址族判定点必须调 SSOT」：要么调 domains/sanitizerules 的 SSOT，"+
				"要么在清单里登记成 %s 并写明理由 + 由哪道行为门把住。", site.String(), classReviewedException)
			continue
		}
		switch a.Class {
		case classNotADecision, classReviewedException:
		default:
			t.Errorf("生产代码里的地址族名单/分支 %s 被登记成 %s ⇒ 生产代码里只允许 %s 与 %s 两种登记",
				site.String(), a.Class, classNotADecision, classReviewedException)
		}
	}
}

// TestAddressFamilyListHasExactlyOneDefinitionSite 钉住「仓内只有一份名单」。
func TestAddressFamilyListHasExactlyOneDefinitionSite(t *testing.T) {
	root := repoRoot(t)
	measured := measureSites(t, root)
	var lists []string
	for key, site := range measured {
		if site.Kind != KindTypeList {
			continue
		}
		if a, ok := addressFamilySiteAssessments[key]; ok && a.Class == classDefinitionSite {
			lists = append(lists, site.String())
		}
	}
	sort.Strings(lists)
	if len(lists) != 1 {
		t.Errorf("★ 仓内的地址族名单应当**只有一份**（SSOT），实测 %d 份：%v\n"+
			"    ⇒ 第二份名单就是下一次漂移的起点（r1038b 的 D1、r1040 的第三面都是这么来的）",
			len(lists), lists)
	}
}

// TestAddressFamilyTypeNameCopyMatchesTheSSOT 把住量具自己的副本。
//
// ★ 量具里的 9 个名字是**故意的副本**（否则分不出抄件），代价是它自己会漂
// ⇒ 这道门让副本与 SSOT **对拍**：SSOT 加减一项，量具必须同步。
func TestAddressFamilyTypeNameCopyMatchesTheSSOT(t *testing.T) {
	want := sanitizerules.AddressFamilyTypes()
	got := append([]string(nil), ssotTypeNames...)
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("量具里的地址族名单副本是 %v，SSOT 是 %v ⇒ 副本已漂移", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("量具里的地址族名单副本是 %v，SSOT 是 %v ⇒ 副本已漂移", got, want)
		}
	}
}

// TestAddressFamilyAuditIsNotSilentlyVacuous 是**防空过假绿**的下限。
//
// ★ 「0 个面」有两种读法：仓里真的没有判定面，与扫描器坏了。
// ⇒ 判据钉的是**后者**：报出的面必须**非空**，且必须含至少一个
// membership-call（脱敏/合规面的正牌判定）与一个 canonical-call（规范化入口）。
func TestAddressFamilyAuditIsNotSilentlyVacuous(t *testing.T) {
	root := repoRoot(t)
	sites, err := AuditRepo(root)
	if err != nil {
		t.Fatalf("AuditRepo(%s): %v", root, err)
	}
	if len(sites) == 0 {
		t.Fatal("全仓普查报出 0 个地址族判定面 —— 这不是「仓里没有判定面」，是审计坏了" +
			"（扫描范围被改小 / 解析失败被吞 / AST 形态变了）。")
	}
	var membership, canonical int
	for _, s := range sites {
		switch s.Kind {
		case KindMembershipCall:
			membership++
		case KindCanonicalCall:
			canonical++
		}
	}
	if membership == 0 {
		t.Error("普查报出 0 个 membership-call ⇒ SSOT 的调用点全没被看见（扫描器失效）")
	}
	if canonical == 0 {
		t.Error("普查报出 0 个 canonical-call ⇒ 规范化入口没被看见（扫描器失效）")
	}
}
