package sanitize

import (
	"context"
	"testing"
)

// r1039（D7，2026-10-20）—— **benign corpus ⇒ 零命中** 门禁。
//
// ★ 为什么需要这道门：仓里已有的三道相关门各自都**测不到规则内容**的
// 误伤：
//
//	· r244_bare_marker_false_positive_test.go 审的是**标记形状**
//	  （`{SENSITIVE:...}` 的裸形态），不是规则内容；
//	· r1034_rule_parity_test.go 查的是**内建与 yaml 规则集是否一致**；
//	· deployment_pattern_config_test.go 只查**yaml 能不能加载**。
//
// ⇒ 「规则内容会不会在普通文字上乱咬」这件事此前**没有任何测试在看**，
// D7 的三个误报就是这么活下来的。
//
// ★ 为什么是**常开**（always-on）而不是像 r244 那道一样 opt-in：
//
//	· 语料是**写在测试文件里**的静态清单（不是外部语料文件）⇒ 不依赖任何
//	  环境变量、任何外部文件、任何生产采样 ⇒ 每次 `go test` 都会跑；
//	· 规模是几十行 ⇒ 耗时在毫秒级（见门禁里的耗时日志）。
//	⇒ 判据纪律第 1 条：「检查没跑成」必须与「检查通过」长得不一样——
//	opt-in 门禁在没设环境变量时会 SKIP，而 SKIP 长得像 PASS。
//	（对比：r244_bare_marker_corpus_audit_test.go 依赖 /tmp 下的生产采样，
//	那种才应该 opt-in。）
//
// ★ 语料**故意不含**下列内容，并在下面写明原因（避免有人以为「漏了」）：
//
//	· 「订单号 20241012」「工单号 12345」「北京市朝阳区建国路88号」——
//	  `pii.address` 在这三条上误伤是**owner 明知并冻结**的取舍
//	  （docs/audit/2026-10-20-r1038-脱敏范围与遮蔽标记.md §六，
//	  由 security/sanitize/r1036_label_suffix_address_test.go 的
//	  TestR1036NonAddressLabelWithoutPreciseTypeUnchanged 钉住）⇒ 本轮
//	  不动它，也**不**把它塞进本门禁（否则门禁会红，而修复不在本轮范围）。
//	· 医疗词（诊断 / 病情 / 处方 …）——r1039 刚把 `medical:` 族加载进来，
//	  它的精度由 r1039_medical_family_test.go 单独钉。
func TestR1039BenignCorpusHasNoSensitiveHits(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	sanitizer, err := NewSanitizer(detector)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}

	hits := 0
	for _, line := range r1039BenignCorpus {
		// 输入面（ScopeInputDeclarations）是最宽的一面：它额外包含
		// 「自然语言声明」启发式（r1039 新增的空格分隔候选也在其中）。
		fragments, err := detector.DetectScope(context.Background(), line, ScopeInputDeclarations)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fragments {
			hits++
			t.Errorf("benign 语料被误判：%q -> 规则 %s 类型 %s 值 %q", line, f.RuleID, f.Type, f.Value)
		}
		// 端到端：脱敏结果必须与原文逐字相同（片段为空 ⇒ 原文不变）。
		result, err := sanitizer.SanitizeInput(context.Background(), line)
		if err != nil {
			t.Fatal(err)
		}
		if result.SanitizedText != line {
			hits++
			t.Errorf("benign 语料在端到端脱敏里被改写：%q -> %q", line, result.SanitizedText)
		}
	}
	t.Logf("benign corpus: %d lines, %d hits", len(r1039BenignCorpus), hits)
}

// r1039BenignCorpus 是「普通业务 / 运维散文」语料（中文 + 英文）。
//
// 选取原则：
//
//	· 覆盖会触发**各类规则**的常见散文形态：英文技术词、中文公文语气、
//	  带数字的运维描述、含「版本 / 灰度 / 回滚」这类运维词的长句；
//	· 覆盖 D7 实测到的三个误伤形态（纳秒时间戳、两位大写字母 + 数字、
//	  单字母 + 8 位数字）；
//	· **不含**任何真实 PII/凭据——本门禁测的是「不该命中时有没有命中」。
var r1039BenignCorpus = []string{
	// —— D7 实测到的三个误伤形态 ——
	"时间戳 2026102012345678",
	"US20240001",
	"DE1234ABCD",
	"配置项 P20240101 生效",

	// —— 中文运维 / 业务散文 ——
	"今天下午的发布窗口已经确认，运维同学会提前十分钟到场。",
	"本次变更只涉及网关的路由配置，不影响上游模型的计费口径。",
	"请在下一个迭代开始前把评审意见同步到设计文档里。",
	"灰度期间先观察错误率，再决定是否扩大流量比例。",
	"值班表已经发到群公告，夜间有同事轮值。",
	"本周的容量评估显示，存储和计算资源都有富余。",
	"配置中心的新版本已灰度，行为保持向后兼容。",
	"部署脚本会在切换前自动做一次健康检查。",
	"回滚方案已准备好，触发条件写在运行手册里。",
	"监控面板增加了两条曲线，分别对应入口和出口的延迟。",
	"客户端升级到最新版本之后，连接复用率明显提升。",
	"这次线上问题的根因是连接池耗尽，不是下游服务变慢。",
	"我们把批处理窗口挪到了凌晨，业务侧已经确认过影响。",
	"请把这次复盘的经验补到排障手册的故障分类里。",
	"灰度批次先放百分之五，观察两天再继续放量。",
	"新同事的权限已经按最小可用原则开通，可以先看只读面板。",
	"本周的周报里要补一条关于缓存命中率下降的说明。",
	"接口的超时时间统一调整为三十秒，避免上游重试放大压力。",
	"该配置项在不同的部署形态下取值不同，文档里已经写清楚。",
	"排查这类问题先看网关侧的日志，再看上游的返回码。",
	"流量侧的压力测试要在业务低峰期做，避免影响线上。",
	"我们保留了旧参数的兼容开关，下个版本再下线。",
	"这个改动只影响读路径，写路径的语义完全不变。",
	"值班交接时请同步一下当前正在灰度的两个批次。",
	"路由策略的变更需要评审通过后才能进发布流程。",
	"缓存预热脚本会在发布完成后自动跑一遍。",
	"压测报告里最关键的是长尾延迟，不是平均耗时。",
	"这一版的错误率比上一版低了十几个百分点，方向是对的。",
	"需要人工确认的变更都收敛到发布前的一个清单里。",
	"对账任务的延迟只影响展示，不影响资金链路。",
	"我们没有改动上游的协议字段，下游不需要配合发版。",
	"限流策略按租户维度生效，跨租户的流量互不影响。",
	"本次升级包含若干依赖的例行更新，没有行为变化。",
	"日志里那条告警是历史遗留，规则早已下线但配置没清。",
	"把这条经验写进发布检查表，下次同类变更可以少走弯路。",

	// —— 英文业务 / 运维散文 ——
	"The release window is confirmed for this afternoon.",
	"Please share the review notes before the next iteration.",
	"We will monitor the error rate during the gradual rollout.",
	"The on-call schedule is posted in the team announcement channel.",
	"No change to the billing semantics for upstream models.",
	"The rollback plan is documented in the operations handbook.",
	"The dashboard now shows two extra curves for ingress and egress latency.",
	"Capacity review shows plenty of headroom for this quarter.",
	"The configuration center has been upgraded and stays backward compatible.",
	"Deployment runs a health check before switching traffic.",
	"The root cause was an exhausted connection pool, not a slow downstream.",
	"We moved the batch window to the early morning after sign-off.",
	"Only the read path changed; write semantics stay exactly the same.",
	"Long tail latency matters more than the average in the load test.",
	"Everything that needs a human check is collected in one pre-release list.",
	"The reconciliation delay only affects the dashboard, not the money path.",
	"We did not touch the upstream protocol fields, so no partner release is needed.",
	"Rate limiting is applied per tenant, so traffic never crosses tenants.",
	"This release only bundles routine dependency updates with no behavior change.",
	"Keep the old flag around for one more release, then remove it.",
}
