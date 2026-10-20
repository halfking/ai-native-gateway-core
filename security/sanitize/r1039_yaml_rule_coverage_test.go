package sanitize

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// r1039（2026-10-20）—— 通用门禁：configs/sensitive_patterns.yaml 里
// **任何一条规则**都必须被检测器加载。
//
// ★ 缺陷本体（D6）：`parsePatternConfig` 只遍历 pii / secret / financial
// 三族，于是 yaml 里的 `medical:` 整族（medical_record / diagnosis /
// medication）**从未被加载**——病历号、诊断、用药信息以明文出网关，而
// yaml 上写着 `enabled: true`，运维读 yaml 会合理地以为这些规则是活的。
// 仓里**没有任何一条测试**断言过「yaml 里的每条规则都被加载」，所以这族
// 死了多久没人知道。
//
// ⇒ 本门禁把这件事变成**测试义务**：yaml 少一族、少一条、或某条正则编译
// 不过，门禁立刻红。
//
// ★ 为什么这道门不是「多写一个断言」那么简单：yaml 里有一批**不是规则源**
// 的段（global / whitelist / custom_rules / redaction_methods /
// performance / audit）。它们**没有** `patterns:` 映射，检测器不解析它们。
// 如果门禁只按「顶层 key 逐个当规则族」去查，这些段会**假红**。
// ⇒ 下面用一份**显式的、有逐条理由的**白名单登记它们；白名单本身也被
// 判据盯着（段不存在 ⇒ 红；白名单段里长出 `patterns:` ⇒ 红）。
//
// ★ 判据纪律（AGENTS.md）：
//
//	· 判据落在**被测函数同一层**——这里直接检查 `parsePatternConfig` 产出的
//	  规则条目（`d.patterns`），不是间接观察「某个输入有没有被遮蔽」。
//	· **双向**：既查「yaml 有的必须加载」，也查「加载的必须在 yaml 里存在」，
//	  否则门禁可以被「加载一个不存在的规则」骗过。
//	· 每条规则额外断言**正则本身能编译**——medical 族当年就是死在这上面：
//	  `\u4e00` 是 JS 风格转义，Go 的 RE2 直接报 `invalid escape sequence`，
//	  而加载器是「整份文件全编译才替换」的语义 ⇒ 一条坏正则会让**整份
//	  yaml 加载失败**、连 pii/secret/financial 一起失效。
var r1039NonRuleSections = map[string]string{
	"global": "检测器没有置信度/缓存概念：规则条目只读 regex/group/kind/scopes/" +
		"action，`confidence` 与 `min_confidence` 都被忽略。若哪天真按 0.7 实现，" +
		"medical.medication（confidence 0.65）反而会被丢弃——所以本轮不实现，只登记。",
	"whitelist": "检测器没有豁免名单机制；yaml 自己的注释也写了「白名单不作为放行依据」。" +
		"下面的门禁另有一条断言：`exclude_patterns` 必须保持为空，往里加条目不会生效。",
	"custom_rules": "custom_rules 用的是另一套 schema（name/type/confidence/redaction），" +
		"加载器只认「<族名>.patterns.<规则名>」。下面的门禁断言 `rules` 必须保持为空，" +
		"加一条就会红并提示改写成某个 `patterns:` 族。",
	"redaction_methods": "各遮蔽样式的说明文档。检测器实际只用统一占位符" +
		"（{SENSITIVE:<type>:<n>}）与统一还原，不按样式名分派。",
	"performance": "检测是同步顺序执行的；限流/超时由网关其它层负责，与本文件无关。",
	"audit":       "审计台账由 domains/sanitizerules（托管规则）那条链路负责，与本文件无关。",
}

// r1039YAMLDocument 把 yaml 读成通用结构（不用 patternConfig），否则门禁
// 就会和被测代码共享同一套解析盲区——yaml 改字段名时两边一起瞎。
func r1039YAMLDocument(t *testing.T) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("read sensitive_patterns.yaml: %v", err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse sensitive_patterns.yaml: %v", err)
	}
	return doc
}

// r1039LoadedRuleIDs 返回 yaml 加载出来的检测器实际持有的规则 id 集合。
func r1039LoadedRuleIDs(t *testing.T) map[string]string {
	t.Helper()
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	detector.mu.RLock()
	defer detector.mu.RUnlock()
	ids := make(map[string]string, len(detector.patterns))
	for _, p := range detector.patterns {
		if _, dup := ids[p.id]; dup {
			t.Errorf("rule id %q loaded twice", p.id)
		}
		ids[p.id] = p.regex.String()
	}
	return ids
}

func r1039Map(t *testing.T, value interface{}, where string) map[string]interface{} {
	t.Helper()
	m, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("%s is %T, want a mapping", where, value)
	}
	return m
}

// TestR1039EveryYAMLRuleIsLoaded 是本轮 D6 的通用门禁。
func TestR1039EveryYAMLRuleIsLoaded(t *testing.T) {
	doc := r1039YAMLDocument(t)
	loaded := r1039LoadedRuleIDs(t)

	// 白名单自身不许腐坏：登记了但 yaml 里没有的段 ⇒ 红（否则有人把段改名
	// 之后，门禁会安静地少管一段）。
	for section := range r1039NonRuleSections {
		if _, ok := doc[section]; !ok {
			t.Errorf("非规则段白名单登记了 %q，但 yaml 里没有这一段 ⇒ "+
				"白名单已腐坏（要么 yaml 改名，要么把该段从白名单里删掉）", section)
		}
	}

	// 反向：加载出来的规则必须在 yaml 里有对应条目。
	yamlRuleIDs := map[string]bool{}
	for section, value := range doc {
		if _, nonRule := r1039NonRuleSections[section]; nonRule {
			// 非规则段不许长出 patterns（否则它其实**是**规则源，登记就错了）。
			if _, hasPatterns := r1039Map(t, value, section)["patterns"]; hasPatterns {
				t.Errorf("非规则段 %q 里出现了 patterns 映射 ⇒ 它其实是规则源，"+
					"请把它移出白名单并让加载器遍历它", section)
			}
			continue
		}
		family := r1039Map(t, value, section)
		enabled, ok := family["enabled"].(bool)
		if !ok {
			t.Errorf("规则族 %q 缺少 enabled 布尔字段", section)
			continue
		}
		patterns := r1039Map(t, family["patterns"], section+".patterns")
		names := make([]string, 0, len(patterns))
		for name := range patterns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			id := section + "." + name
			yamlRuleIDs[id] = true
			definition := r1039Map(t, patterns[name], id)
			source, _ := definition["regex"].(string)
			if source == "" {
				t.Errorf("规则 %s 没有 regex 字段", id)
				continue
			}
			if _, err := regexp.Compile(source); err != nil {
				t.Errorf("规则 %s 的正则编译不过（Go 的 RE2 不支持 \\u 之类的转义）：%v", id, err)
				continue
			}
			got, ok := loaded[id]
			if !ok {
				t.Errorf("规则 %s 在 yaml 里定义了却**没有被加载** ⇒ 这条规则是死的", id)
				continue
			}
			if got != source {
				t.Errorf("规则 %s 加载后的正则与 yaml 不一致：\n  yaml: %s\n  加载: %s", id, source, got)
			}
		}
		if !enabled {
			for _, name := range names {
				if _, ok := loaded[section+"."+name]; ok {
					t.Errorf("规则 %s.%s 的族是 disabled，却仍被加载", section, name)
				}
			}
		}
	}

	for id := range loaded {
		if !yamlRuleIDs[id] {
			t.Errorf("检测器加载了规则 %s，但 yaml 里没有对应条目 ⇒ 规则集与配置漂移", id)
		}
	}
}

// TestR1039NonRuleSectionsStayInert 钉住「非规则段不许悄悄变成规则源」。
//
// 没有这条，上面的门禁在「有人往 whitelist 里塞 patterns」时会红，但
// **原因会指向错的地方**（读者会以为是加载器坏了）。这条把「白名单段里
// 出现了本该留空的清单」直接点出来。
func TestR1039NonRuleSectionsStayInert(t *testing.T) {
	doc := r1039YAMLDocument(t)

	custom := r1039Map(t, doc["custom_rules"], "custom_rules")
	if rules, ok := custom["rules"].([]interface{}); !ok || len(rules) != 0 {
		t.Errorf("custom_rules.rules 必须保持为空：检测器不解析 custom_rules（用的是另一套 "+
			"schema），加进去的规则不会生效却看起来生效了。实际内容：%v", custom["rules"])
	}

	whitelist := r1039Map(t, doc["whitelist"], "whitelist")
	if patterns, ok := whitelist["exclude_patterns"].([]interface{}); !ok || len(patterns) != 0 {
		t.Errorf("whitelist.exclude_patterns 必须保持为空：检测器不把白名单当放行依据，"+
			"加进去的条目不会生效却看起来生效了。实际内容：%v", whitelist["exclude_patterns"])
	}
}
