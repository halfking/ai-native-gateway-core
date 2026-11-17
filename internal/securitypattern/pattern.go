// Package securitypattern 是网关侧的**跨实例威胁模式**面（M2-3）。
//
// # 它补的是哪一段
//
// M2-2 落地了**写面**：网关把统一契约的安全事件写进共享知识项目
// （internal/securityevent 的出网 sink）。规划里 M2-3 要的是另一半：
//
//	Gateway A 检测到威胁 → 模式提取 → 共享域 → Gateway B/C 自动获取新规则
//
// 本包实现 B/C 侧的**读 → 聚合 → 缓存 → 喂给本机**这条链：
//
//  1. 读（reader.go）：从共享项目里检索带 security 标签的安全事件；
//  2. 聚合（pattern.go）：按「事件类型 + 检测码」把它们归并成模式；
//  3. 缓存（cache.go）：本地快照，供请求路径 O(1) 查询；
//  4. 喂（cache.go 的 Annotate）：本机检测到的同签名事件挂上"全网视角"。
//
// # 三个刻意的设计取舍
//
// **① 模式提取放在消费侧，不放在共享域里。**
//
// 规划画的是"A 提取出模式 → 写进 L5 → B/C 取模式"。本包改成"B/C 取**事件**
// 自己在本地聚合成模式"。理由有三：
//
//   - memora 侧零改动（不依赖尚未验证的 playbook 写入契约）；
//   - 模式定义权留在每个实例自己手里 —— 谁有权把一条规则定为"全网模式"
//     是个治理问题，放进共享域就绕不开；
//   - 写面（M2-2）已经在写事件，读事件是同一份契约的另一半，闭环最短。
//
// **② 共享域的内容只做观测增强，不参与拦截判定。**
//
// 共享项目里的条目可能被污染（任何能写这个项目的主体都能写）。让这样的
// 内容决定"要不要拦"，等于把外部系统的可信度接到请求路径上。
// ⇒ 模式只作为提示挂在事件上（Event.FleetHint），由消费侧自行加权。
//
// **③ 检索是语义检索，不是完整镜像。**
//
// memora 的 /api/v2/memories/search 是向量检索（query 必填，top_k ≤ 100）。
// 它给的是"与 query 最像的 N 条"，不是"这个项目里的全部安全事件"。
// ⇒ 本包把它当**模式流**（尽力而为的 top-K），不是审计级同步；
// 需要全量时应走 memora 的 PG 列表面（GET /api/v2/memories），
// 那是另一个身份模型（v1_user_id），本包不用它。
//
// **④ 跨实例共享的前提是"同一个 actor 身份"。**
//
// memora 的 memory 读端点硬过滤 `user_id = $1`（memora 仓
// internal/memory/service.go），而 v2 ingest 也以 actor 作为落库 user_id
// ⇒ 读到的就是**同一个 actor 命名空间**里的东西。
//
// 所以"Gateway A 的模式被 B/C 看到"成立的前提是：**所有网关实例配同一套
// 身份**（同一 LLM_GATEWAY_MEMORA_JWT_SECRET / TENANT_ID / ACTOR_ID /
// PROJECT_ID）。若每个实例有各自的 actor_id，则每个实例只会读到自己写的
// 事件 —— 模式功能看起来"能用"（只统计自己），但**跨实例那部分根本没发生**。
// ⇒ 装配处（cmd/gateway）会把这句写进日志，别让它静默成立。
package securitypattern

import (
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/securityevent"
)

// Signature 是威胁模式的签名。
//
// 签名 = 事件类型 + 检测码：同一个检测器、同一个代码 ⇒ 同一个模式。
// 例："security.threat_detected.v1|pii_leak"。
//
// 为什么不用消息正文做签名：消息里带 session/request id 等每次都变的
// 内容，拿它当签名 ⇒ 每个事件都是"新模式"，聚合就失去意义。
func Signature(eventType, code string) string {
	eventType = strings.TrimSpace(eventType)
	code = strings.TrimSpace(code)
	if eventType == "" && code == "" {
		return ""
	}
	return eventType + "|" + code
}

// Observation 是一条从共享域读回来的安全事件投影。
//
// 它只保留聚合模式需要的字段 —— 读回来的正文（snippet）是给人看的，
// 聚合用 metadata 里的结构化字段。
type Observation struct {
	// EventType 是事件类型（如 security.threat_detected.v1）。
	EventType string
	// Code 是检测码（如 pii_leak）。
	Code string
	// Severity 是归一化后的严重度。
	Severity securityevent.Severity
	// Action 是检测面的处置动作。
	Action securityevent.Action
	// TenantID 是触发方的租户。
	TenantID string
	// InstanceID 是产生这条事件的网关实例 —— **跨实例视角的来源**。
	InstanceID string
	// DetectedAt 是检测发生时间。
	DetectedAt time.Time
	// MemoryID 是共享域里的条目 id（去重与追溯用）。
	MemoryID string
}

// Pattern 是一个跨实例的威胁模式。
type Pattern struct {
	// Signature 是模式签名。
	Signature string `json:"signature"`
	// EventType / Code 是签名的两个组成部分。
	EventType string `json:"event_type"`
	Code      string `json:"code"`
	// Severity 是观察到的**最高**严重度（同一模式不同次出现的严重度可能不同）。
	Severity securityevent.Severity `json:"severity"`
	// Observations 是**这份快照里**观察到这个模式的事件条数。
	//
	// ★ 它**不是**累计计数器：每次同步都是**整份替换**快照，计数反映的是
	//   "共享域里现在有多少条这个模式的事件"，不是"历史上累计发生过多少
	//   次"。写成累计会让人以为它单调增长 —— 那是错的。
	Observations int `json:"observations"`
	// Instances 是见过它的实例 id（去重、排序）。
	Instances []string `json:"instances,omitempty"`
	// InstanceCount 是 Instances 的长度。
	InstanceCount int `json:"instance_count"`
	// Tenants 是涉及过的租户（去重、排序）。
	Tenants []string `json:"tenants,omitempty"`
	// FirstSeen / LastSeen 是观察到的最早/最晚时间。
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// BuildPatterns 把一批观察聚合成模式。
//
// 聚合口径：
//   - 按签名归并；
//   - 次数 = 观察条数（**不去重**：同一条事件被读两次算两次 —— 读面是
//     top-K 的语义检索，天然可能重复，所以这里刻意不假装精确）；
//   - 实例与租户去重（它们回答的是"几个实例/几个租户见过"，重复计数没意义）；
//   - 严重度取最高；时间取最早/最晚。
//
// 空签名（事件类型与检测码都缺）的观察被跳过 —— 它没有可归并的形状。
func BuildPatterns(observations []Observation) map[string]Pattern {
	patterns := make(map[string]Pattern)
	instances := map[string]map[string]struct{}{}
	tenants := map[string]map[string]struct{}{}

	for _, obs := range observations {
		signature := Signature(obs.EventType, obs.Code)
		if signature == "" {
			continue
		}
		pattern, ok := patterns[signature]
		if !ok {
			pattern = Pattern{
				Signature: signature,
				EventType: strings.TrimSpace(obs.EventType),
				Code:      strings.TrimSpace(obs.Code),
				Severity:  securityevent.SeverityInfo,
			}
			patterns[signature] = pattern
			instances[signature] = map[string]struct{}{}
			tenants[signature] = map[string]struct{}{}
		}

		pattern.Observations++
		if severityRank(obs.Severity) > severityRank(pattern.Severity) {
			pattern.Severity = obs.Severity
		}
		if !obs.DetectedAt.IsZero() {
			if pattern.FirstSeen.IsZero() || obs.DetectedAt.Before(pattern.FirstSeen) {
				pattern.FirstSeen = obs.DetectedAt.UTC()
			}
			if pattern.LastSeen.IsZero() || obs.DetectedAt.After(pattern.LastSeen) {
				pattern.LastSeen = obs.DetectedAt.UTC()
			}
		}
		if instance := strings.TrimSpace(obs.InstanceID); instance != "" {
			instances[signature][instance] = struct{}{}
		}
		if tenant := strings.TrimSpace(obs.TenantID); tenant != "" {
			tenants[signature][tenant] = struct{}{}
		}
		patterns[signature] = pattern
	}

	for signature, pattern := range patterns {
		pattern.Instances = sortedKeys(instances[signature])
		pattern.InstanceCount = len(pattern.Instances)
		pattern.Tenants = sortedKeys(tenants[signature])
		patterns[signature] = pattern
	}
	return patterns
}

// severityRank 给严重度排序（info < low < medium < high < critical）。
//
// 未知刻度排最低但**不会被当成"没问题"**：它只影响"哪个更严重"的排序，
// 不改变任何判定。
func severityRank(severity securityevent.Severity) int {
	switch severity {
	case securityevent.SeverityCritical:
		return 4
	case securityevent.SeverityHigh:
		return 3
	case securityevent.SeverityMedium:
		return 2
	case securityevent.SeverityLow:
		return 1
	case securityevent.SeverityInfo:
		return 0
	default:
		return -1
	}
}

// sortedKeys 返回排序后的键（确定性输出：同一批观察 ⇒ 同一份快照）。
func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
