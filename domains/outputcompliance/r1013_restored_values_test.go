package outputcompliance

// r1013_restored_values_test.go — 2026-10-10「已还原值不得被输出侧销毁」。
//
// ## 症状
//
// 用户发来 `export https_proxy=http://10.24.7.91:7890`，响应回来是
// `export https_proxy=http://********:7890`，且没有任何映射能把它换回来；
// 工具参数（代码执行路径同走这条链）同样被压成星号，执行必然失败。
//
// ## 根因
//
// 响应拦截链的顺序是
//
//	强制输出守卫 → 占位符还原 → 输出合规（cmd/gateway/goal_control.go）
//
// 输出合规排在还原**之后**，于是它看到的文本里混着两类来源完全不同的值：
// 模型自己写的值，和网关刚把 {SENSITIVE:type:n} 换回来的**调用方原始输入**。
// 合规层分不出这两者，一视同仁地单向遮蔽——用户自己发来的地址刚被还原出来，
// 就被网关亲手压成星号。
//
// ## 契约
//
// 1. 命中区间**整段**等于某个已还原值时，不遮蔽（网关自己注入的值按定义已经
//    回到调用方手里，遮它既不防泄漏又必然毁内容）。
// 2. 遮蔽物是带类型的标记，不是 `*********`：星号串没有编码、不进映射，
//    命中即永久丢失，且长度泄漏原值长度。
// 3. 豁免**只影响客户端可见文本**：issue 照记，合规统计 / 复核队列 / 审计行
//    不变——「响应里出现过内网地址」这件事必须仍然在台账里可见。
//
// 第 3 条必须与 1、2 成组存在：只测「豁免了」，下一次有人把记录一起跳过，
// 测试仍会全绿，而审计面已经空了。

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// ipRuleChecker 复刻「DB 里的 pii_patterns.ip_address 仍然开着」这一形态。
//
// 它不是假设：851 停用迁移此前从未登记进安装器 StartupFiles（见
// installer/internal/dbinit/runner.go 与 db/db.go 的 ensurePIIIPAddressRuleDisabled），
// 因此每台全新安装的机器上这条规则确实还在跑。修好接线之后，本文件仍然
// 保留这个形态：运维随时可能把它改回 enabled，代码必须对此免疫。
func ipRuleChecker() *Checker {
	c := NewBuiltinChecker()
	c.piiPatterns = append(c.piiPatterns, &PIIPattern{
		PatternName:  "ip_address",
		PatternType:  "ip",
		RegexPattern: `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`,
		Enabled:      true,
		Severity:     5,
		regex:        regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`),
	})
	return c
}

// TestRestoredValuesAreNotDestroyedByOutputCompliance 是本文件的主契约。
func TestRestoredValuesAreNotDestroyedByOutputCompliance(t *testing.T) {
	const callerIP = "10.24.7.91"
	const text = "export https_proxy=http://" + callerIP + ":7890 http_proxy=http://" + callerIP + ":7890"

	ctx := WithRestoredValues(context.Background(), NewRestoredValues([]string{callerIP}))
	result, err := ipRuleChecker().Check(ctx, "tenant-a", text)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.RedactedOutput != text {
		t.Fatalf("调用方原值被销毁了\n got: %s\nwant: %s", result.RedactedOutput, text)
	}
	if strings.Contains(result.RedactedOutput, "*") {
		t.Fatalf("响应里出现星号串: %q", result.RedactedOutput)
	}
	// 契约 3：豁免的是客户端可见文本，命中记录必须仍在。
	if len(result.Issues) == 0 {
		t.Fatal("豁免后不再记录 issue：审计面被掏空了")
	}
}

// TestModelAuthoredValueIsStillMasked 是反方向：豁免不能扩大到「本响应里的
// 所有地址」，否则输出合规就等于关掉了。
func TestModelAuthoredValueIsStillMasked(t *testing.T) {
	const restored = "10.24.7.91"
	const modelAuthored = "203.0.113.8"
	text := "connect to " + modelAuthored + " first, gateway at " + restored

	ctx := WithRestoredValues(context.Background(), NewRestoredValues([]string{restored}))
	result, err := ipRuleChecker().Check(ctx, "tenant-a", text)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if strings.Contains(result.RedactedOutput, modelAuthored) {
		t.Fatalf("模型自造的地址未被遮蔽: %q", result.RedactedOutput)
	}
	if !strings.Contains(result.RedactedOutput, restored) {
		t.Fatalf("调用方原值被连带销毁: %q", result.RedactedOutput)
	}
}

// TestExemptionRequiresWholeSpanMatch 钉住「整段相等」而不是子串包含：
// 命中区间只要沾上一个已还原值就整段放行，等于给模型开了绕行口子。
func TestExemptionRequiresWholeSpanMatch(t *testing.T) {
	const restored = "10.24.7.91"
	const smuggled = restored + ":5432/password=hunter2"

	ctx := WithRestoredValues(context.Background(), NewRestoredValues([]string{restored}))
	result, err := ipRuleChecker().Check(ctx, "tenant-a", smuggled)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.RedactedOutput == smuggled {
		t.Fatalf("以已还原值为前缀的拼接串未被处理: %q", result.RedactedOutput)
	}
	if strings.Contains(result.RedactedOutput, restored+":5432/password=") {
		t.Fatalf("拼接串整体逃逸: %q", result.RedactedOutput)
	}
}

// TestNoRestoredValuesMeansOldBehavior 保证未装配豁免来源时行为不变
// （lite 模式、纯数据面部署不装配 sanitize 还原器）。
func TestNoRestoredValuesMeansOldBehavior(t *testing.T) {
	const modelAuthored = "203.0.113.8"
	text := "connect to " + modelAuthored

	result, err := ipRuleChecker().Check(context.Background(), "tenant-a", text)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if strings.Contains(result.RedactedOutput, modelAuthored) {
		t.Fatalf("无豁免集合时遮蔽失效: %q", result.RedactedOutput)
	}
}

// TestRedactionNeverUsesStarRuns 钉住「不能用 ** 代替原值」这条要求本身：
// 任何一类命中的替代物都不得含星号。
func TestRedactionNeverUsesStarRuns(t *testing.T) {
	policy := defaultPolicy("")
	policy.ActionOnInternalIP = "redact"
	policy.ActionOnSecrets = "redact"
	policy.ActionOnToxicity = "redact"
	c := ipRuleChecker()

	for _, text := range []string{
		"connect to 10.24.7.91:5432",
		"key is sk-abcd1234abcd1234",
		"邮箱 test@example.com 手机 13800138000",
	} {
		result, err := c.Check(context.Background(), "tenant-a", text)
		if err != nil {
			t.Fatalf("Check(%q): %v", text, err)
		}
		if strings.Contains(result.RedactedOutput, "*") {
			t.Errorf("遮蔽物含星号 %q -> %q", text, result.RedactedOutput)
		}
		if result.RedactedOutput == text {
			t.Errorf("完全未遮蔽 %q", text)
		}
	}

	// 内网地址走的是内置 detectInternalIP，不经 pii_patterns；两条链路都要钉。
	issues := c.detectInternalIP("connect to 10.24.7.91:5432", policy)
	got := c.redactOutput(context.Background(), "connect to 10.24.7.91:5432", issues, policy)
	if strings.Contains(got, "*") {
		t.Errorf("internal_ip 遮蔽物含星号: %q", got)
	}
}

// TestToxicReplacementIsActuallyUsed 记一笔顺带修好的死配置：ToxicReplacement
// 此前在遮蔽路径上从未被读取（写死的是等长星号串），配置项形同虚设。
//
// 这里直接驱动 toxicReplacement + redactOutput，而不是走 Check：Check 在
// 无 DB 时自行加载 defaultPolicy（ActionOnToxicity="warn"，本就不遮蔽），
// 借它断言配置项生效会得到一个什么都没发生的假绿。
func TestToxicReplacementIsActuallyUsed(t *testing.T) {
	c := NewBuiltinChecker()
	policy := defaultPolicy("")
	policy.ActionOnToxicity = "redact"
	policy.ToxicReplacement = "[内容已过滤]"

	const text = "你说的这句话是 fuck 啊"
	issues := c.detectToxicity(text, policy)
	if len(issues) == 0 {
		t.Fatal("toxic 检出失效")
	}
	got := c.redactOutput(context.Background(), text, issues, policy)
	if !strings.Contains(got, "[内容已过滤]") {
		t.Fatalf("运营配置的 ToxicReplacement 未生效: %q", got)
	}
	if strings.Contains(got, "fuck") {
		t.Fatalf("毒性词未被遮蔽: %q", got)
	}

	// 未配置时退回带类型标记，仍然不用星号串。
	policy.ToxicReplacement = ""
	fallback := c.redactOutput(context.Background(), text, c.detectToxicity(text, policy), policy)
	if strings.Contains(fallback, "*") {
		t.Fatalf("回退遮蔽物含星号: %q", fallback)
	}
	if !strings.Contains(fallback, RedactionMarker("profanity")) {
		t.Fatalf("回退遮蔽物不是带类型标记: %q", fallback)
	}
}

// TestRedactionMarkerSanitizesKind 标记要出现在 JSON 与 SSE 文本里，
// DB 里的 pattern_type 不能带出换行或尖括号。
func TestRedactionMarkerSanitizesKind(t *testing.T) {
	if got := RedactionMarker("Phone"); got != "[REDACTED:phone]" {
		t.Errorf("大小写未归一: %q", got)
	}
	if got := RedactionMarker(""); got != "[REDACTED]" {
		t.Errorf("空类型应退回裸标记: %q", got)
	}
	// 断言整串而不是 ContainsAny：标记本身的首尾就是 [ ]，只有类型段必须净化。
	if got := RedactionMarker("a\nb]<script>"); got != "[REDACTED:a_b__script_]" {
		t.Errorf("标记未净化: %q", got)
	}
}
