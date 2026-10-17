package main

// topology_plugins_wiring.go — 把真实注册中心/数据面治理件适配成 admin 拓扑插件源。
//
// builtin 两种真值形态（按部署形态取其一，都是「真正在拦流量的东西」）：
//   - v2 治理管线开启（LLM_GATEWAY_USE_V2_PIPELINE=1）：domains/security
//     Registry（buildV2DispatchPipeline 内创建并经 v2Deps.SecurityRegistry
//     回传）—— 拓扑读它就是读管线本身；
//   - v2 管线关闭（当前默认）：Registry 不存在，此时**在用的治理件**是 v1
//     数据面的三个守卫：脱敏模式检测器（PatternDetector，进出双向）、
//     敏感词引擎（输入/输出侧，词表为空 = 实际不拦截，不报）、输出合规
//     （响应回客户端前，流式执行器内建）。空清单只留给「什么都没装」。
//
// external = plugin-runtime Registry（外部插件进程状态，health loop 实时改写）。
// 两者都允许为 nil：对应清单为空，端点只在完全没装配时才 degraded。

import (
	"sort"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/security"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

type topologyPluginsAdapter struct {
	builtin  *security.Registry
	external *pluginruntime.Registry
	// v1 数据面治理件（v2 管线关闭时的真值来源；nil = 未装配，不报）。
	swEngine  *sensitive.SensitiveWordEngine
	sanitizer *sanitize.PatternDetector
}

// BuiltinPlugins 投影在用治理插件。
// v2 注册中心在场时以它为准；否则报告 v1 数据面真实装配的守卫集合
// （由非 nil 指针与词表计数推导，不是静态硬编码清单）。
func (a topologyPluginsAdapter) BuiltinPlugins() []admin.TopologyBuiltinPlugin {
	if a.builtin != nil {
		plugins := a.builtin.List()
		out := make([]admin.TopologyBuiltinPlugin, 0, len(plugins))
		for _, p := range plugins {
			if p == nil {
				continue
			}
			out = append(out, admin.TopologyBuiltinPlugin{
				Name:      p.Name(),
				Direction: p.Direction(),
			})
		}
		return out
	}
	out := make([]admin.TopologyBuiltinPlugin, 0, 4)
	if a.sanitizer != nil {
		out = append(out, admin.TopologyBuiltinPlugin{Name: "sanitize_pattern", Direction: security.DirectionBoth})
	}
	if a.swEngine != nil && a.swEngine.LoadedWordCount() > 0 {
		out = append(out,
			admin.TopologyBuiltinPlugin{Name: "sensitive_word_input", Direction: security.DirectionInput},
			admin.TopologyBuiltinPlugin{Name: "sensitive_word_output", Direction: security.DirectionOutput},
		)
	}
	// 输出合规守卫是流式执行器的内建环节（r1016/r1028 族的 output_policy_blocked
	// 都出自它），不存在「装配了但没有」的形态 —— 恒报。
	out = append(out, admin.TopologyBuiltinPlugin{Name: "output_compliance", Direction: security.DirectionOutput})
	return out
}

// ExternalPlugins 投影外部插件进程状态（含 failed/degraded —— 运维
// 恰恰最需要看到的是这些非 ready 状态）。按 PluginID 排序：Snapshot 走
// map 迭代，不排序的话同一状态两次请求顺序都不同，前端 chip 会闪跳。
func (a topologyPluginsAdapter) ExternalPlugins() []admin.TopologyExternalPlugin {
	if a.external == nil {
		return []admin.TopologyExternalPlugin{}
	}
	states := a.external.Snapshot()
	out := make([]admin.TopologyExternalPlugin, 0, len(states))
	for _, st := range states {
		out = append(out, admin.TopologyExternalPlugin{
			PluginID:      st.PluginID,
			PluginVersion: st.PluginVersion,
			Status:        st.Status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out
}
