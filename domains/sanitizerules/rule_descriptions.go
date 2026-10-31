package sanitizerules

import (
	"strings"
	"sync"
)

// 规则说明目录（2026-10-29 输出合规误报轮 · 「规则说明太简单」）。
//
// ★ 为什么要有这个目录：★ 规则的「说明」（这条规则判什么形状、命中意味着什么、
//   怎么调）写在**代码侧的事实源**里 —— ★ `configs/sensitive_patterns.yaml` 的
//   `description:` 字段，以及内建规则目录（builtinRuleDescriptions）★★
// 而**存库的规则快照不带说明**：★ 历史快照是在说明被采集之前发布的 ⇒ ★★ 于是
//   管理面「读规则」时只能看到一条规则 ID ⇒ ★★ 运维「看不懂这条规则在判什么」
//   ⇒ ★★ 误报就永远只能靠猜（★ 这正是 owner 反馈的「规则说明太简单」）。
//
// ★ 事实源只有一处：★★ YAML 的 description + 内建目录；★ 目录条目由**装载器**
//   在启动/热更时登记（RegisterRuleDescription）⇒ ★★ **不在 SQL / 迁移里抄一份**
//   （抄一份 = 两处事实源 = 必然漂移 = 运维看到的说明与实际生效的规则对不上）。
//
// ★ 读面用法：★ ApplyRuleDescriptions 给配置补说明 —— ★★ 只补**缺省**的
//   （快照里已有的说明优先 ⇒ 人工写过的说明不被覆盖）★★。

// ruleDescMu 保护下面两张表。
var (
	ruleDescMu sync.RWMutex
	// ruleDescExtra 是装载器登记的说明（键 = 规则 ID）。
	ruleDescExtra = map[string]string{}
)

// RegisterRuleDescription 登记一条规则说明。
//
// ★ 由规则装载器调用（★ YAML 装载、热更、内建检查器）⇒ ★★ 同一 ID 重复登记时
// **后者覆盖前者**（★ 热更时 YAML 改了说明要能覆盖旧的）。
func RegisterRuleDescription(ruleID, description string) {
	ruleID = strings.TrimSpace(ruleID)
	description = strings.TrimSpace(description)
	if ruleID == "" || description == "" {
		return
	}
	ruleDescMu.Lock()
	defer ruleDescMu.Unlock()
	ruleDescExtra[ruleID] = description
}

// RuleDescription 取一条规则的说明；没有登记过返回 ok=false。
//
// ★ 查两张表：★ 内建目录（编译期常量）⇒ ★★ 装载器登记表（运行期）★★
// 登记表优先（★ 它更具体：内建目录给的是合成规则的通用说明）。
func RuleDescription(ruleID string) (string, bool) {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return "", false
	}
	ruleDescMu.RLock()
	desc, ok := ruleDescExtra[ruleID]
	ruleDescMu.RUnlock()
	if ok {
		return desc, true
	}
	if info, ok := builtinRuleDescriptions[ruleID]; ok {
		return info.Description, true
	}
	return "", false
}

// RuleDescriptionCatalog 返回全部已知规则说明（键 = 规则 ID）。
//
// ★ 供管理面「规则说明」面板用（★ 规则快照里没有说明时，从这里取）。
func RuleDescriptionCatalog() map[string]string {
	out := make(map[string]string, len(builtinRuleDescriptions)+len(ruleDescExtra))
	for id, info := range builtinRuleDescriptions {
		out[id] = info.Description
	}
	ruleDescMu.RLock()
	for id, desc := range ruleDescExtra {
		out[id] = desc
	}
	ruleDescMu.RUnlock()
	return out
}

// ApplyRuleDescriptions 给配置里的每条规则补上说明。
//
// ★ 只补**缺省**的：★ 快照里已经带说明的（★ 人工发布时写进去的）**保持原样**。
// ★ 返回新配置（★ 不改入参里的切片 ⇒ 调用方的配置不被就地改写）。
func ApplyRuleDescriptions(cfg Config) Config {
	if len(cfg.Rules) == 0 {
		return cfg
	}
	rules := make([]Rule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	for i := range rules {
		if strings.TrimSpace(rules[i].Description) != "" {
			continue
		}
		if desc, ok := RuleDescription(rules[i].ID); ok {
			rules[i].Description = desc
		}
	}
	return Config{Rules: rules}
}
