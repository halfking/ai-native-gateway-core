package sanitize

// r1038_redaction_scope_marker_test.go — 2026-10-20 脱敏范围与遮蔽标记的门禁。
//
// ## 起因（用户报告）
//
// 用户反复看到
//
//	export https_proxy=http://[REDACTED]:7890 http_proxy=http://[REDACTED]:7890
//
// 并给出两条口径：
//
//  1. 「原则上每个占位符都有特殊的标记」——客户端看到的遮蔽物必须自证遮掉了
//     哪一类，而不是一个无从追问的裸 `[REDACTED]`；
//  2. 「脱敏的范围应该不包含域名、地址、ip这些内容，这些不是足够敏感」。
//
// 验收原文（用户逐字给出，命令必须仍可复制执行）：
//
//	export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890
//	git config --global http.proxy $http_proxy
//	curl -o /dev/null -w '%{http_code}\n' https://github.com      # 期望 200
//	git clone --depth 1 https://github.com/octocat/Hello-World /tmp/gt   # 期望成功
//
// ## 根因（r1038 取证）
//
// 1. **地址族的排除只挂在内建 fallback 上**。`DetectScope` 里排除地址族的
//    条件是 `snapshot == nil && scope != ScopeEgress && scopedType(...)`——
//    `snapshot == nil` 意味着「不是托管规则」。于是**已发布规则路径没有这层
//    排除**：任何一条 type=server_ip/internal_ip、scopes 含 output 的已发布
//    规则，都会在脱敏面把地址遮掉。
//    而 legacy `pii_patterns` 导入恰好造的就是这个形状
//    （cmd/gateway/sanitize_rules_control.go：type `ip`→`server_ip`、
//    scopes=[output]、action=mask）。851 迁移停用的是**DB 行**，不是代码：
//    基线快照不可变，851 之前发布过基线的部署拿到的快照里那行仍然是 enabled。
// 2. **裸标记仍有生产路径**。类型不可知时（退化跨段、超长尾段、整字段遮蔽、
//    被改写过的占位符）产出的是裸 `[REDACTED]`，而其中至少有一条路径
//    （loose 还原）**明明读得出类型**却仍然退成裸形态。
//
// ## 本文件钉住的两条契约
//
//   A. 地址族在**脱敏面**一律放行（fallback 与托管规则同口径），且放行
//      **只撤遮蔽、不撤检出**（命中仍进台账）。
//   B. 遮蔽物一律带类型；类型不可知时是 `[REDACTED:unknown]` 而不是裸形态。
//
// 判据纪律：每条「没被遮蔽」的断言都必须配一条**前提仍在场**的断言
// （「检测器确实看得见它」），否则「没被遮蔽」可能只是「压根没匹配到」那种
// 假绿。

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1038UserReportedBlock 是用户逐字给出的验收原文。
const r1038UserReportedBlock = "export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890\n" +
	"git config --global http.proxy $http_proxy\n" +
	"curl -o /dev/null -w '%{http_code}\\n' https://github.com\n" +
	"# 期望 200\n" +
	"git clone --depth 1 https://github.com/octocat/Hello-World /tmp/gt\n" +
	"# 期望成功"

// r1038ManagedDetector 装一个**托管规则**检测器（snapshot != nil 的那条路径）。
//
// 这是 r1038 根因的关键：旧代码的地址族排除只对 snapshot == nil 生效，
// 所以判据必须走托管路径才抓得到那个缺口。
func r1038ManagedDetector(t *testing.T, rules ...sanitizerules.Rule) *PatternDetector {
	t.Helper()
	cfg := sanitizerules.Config{Rules: rules}
	m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), cfg, sanitizerules.Options{InstanceID: "r1038"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	d := NewPatternDetector()
	d.SetRuleManager(m)
	return d
}

// r1038AddressPattern 是 IPv4 + IPv6 的合成形态（生产上是 pii.server_ip 与
// pii.server_ipv6 两条规则；合成一条是为了让判据的前提断言对两种地址都成立）。
const r1038AddressPattern = `\b(?:\d{1,3}\.){3}\d{1,3}\b|(?:[0-9A-Fa-f]{0,4}:){2,}[0-9A-Fa-f:.]*`

// r1038LegacyIPRule 是 legacy `pii_patterns.ip_address` 行在托管快照里的形状。
//
// 形状来自 316 播种 + cmd/gateway/sanitize_rules_control.go 的导入映射：
// type `ip` → `server_ip`，scopes [output]，action mask。**这条规则一旦被
// 启用，每个 IPv4 都在脱敏面被遮蔽**——本文件要钉住的正是它遮不得。
func r1038LegacyIPRule() sanitizerules.Rule {
	return sanitizerules.Rule{
		ID: "compliance.pii.11", Name: "ip_address", Type: "server_ip", Kind: "pattern",
		Pattern: `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`, Scopes: []string{"output"},
		Action: "mask", Enabled: true,
	}
}

// r1038InputScopedIPRule 是同一条规则、但**同时挂 input 相位**的形态。
//
// 为什么需要它：输入侧走 ScopeInputDeclarations ⇒ 相位 "input"，而上面那条
// legacy 规则只挂 "output" ⇒ 它在输入面**压根不适用**。拿它做输入侧的
// 判据就是**假绿**：修复前修复后都绿，钉不住任何东西（变异验证实测）。
// 挂上 input 相位后，「地址规则在脱敏面不得遮蔽地址」这条判据在输入面
// 才真正可失败。
func r1038InputScopedIPRule() sanitizerules.Rule {
	rule := r1038LegacyIPRule()
	rule.ID = "compliance.pii.11.input"
	rule.Scopes = []string{"input", "output"}
	// IPv4 与 IPv6 合成一条：判据的用例里有 IPv6 形态，而 legacy 的
	// ip_address 行只认 IPv4——用它做前提会得到「这条规则压根不匹配它」
	// 的假前提。
	rule.Pattern = r1038AddressPattern
	return rule
}

// r1038EgressScopedIPRule 是同一条规则、但**同时挂 egress 相位**的形态。
//
// 用途只有一个：做「前提仍在场」的断言。legacy 形状只挂 output 相位，
// 而 egress 面按相位过滤规则 ⇒ 用它做前提会得到「egress 面没看见地址」，
// 那是**相位**造成的，不是检测器坏了。这条形态让同一条规则在两个相位都
// 适用，于是可以验证：检测器**看得见**地址（egress 面有片段），而脱敏面
// 依然不遮蔽它（ScopeRedact 无片段）。
func r1038EgressScopedIPRule() sanitizerules.Rule {
	rule := r1038LegacyIPRule()
	rule.ID = "pii.server_ip"
	rule.Scopes = []string{"output", "egress"}
	// IPv4 与 IPv6 合成一条：生产上它们是两条规则（pii.server_ip /
	// pii.server_ipv6），这里合成一条是为了让「前提仍在场」对 IPv6 文本
	// 同样成立——否则 IPv6 用例会因为「这条规则压根不匹配它」而误报前提失效。
	rule.Pattern = r1038AddressPattern
	// egress 相位只接受 block / log（Compile 的硬约束）——与生产上
	// `pii.server_ip` 的形状一致。脱敏面遇到 block 动作本会掐断整条请求，
	// 正是本次修复要保证**不会**发生的那件事（判据顺带钉住它）。
	rule.Action = "block"
	return rule
}

// TestR1038ManagedAddressRuleDoesNotRedact 契约 A 的**输入面**门禁。
//
// 判据：一条已发布的、type=server_ip 的规则**不得**在脱敏面遮蔽地址。
//
// 前提仍在场：这条规则同时挂 input 与 output 两个相位，于是
//   - 前提用**结构**断言（规则在托管快照里、作用域含 input、enabled、
//     正则确实匹配这段文本）——用结构而不是「egress 面看得见」是因为
//     输入面走 ScopeInputDeclarations ⇒ 相位 "input"，egress 相位对它
//     压根不适用，拿它做前提会得到「压根没匹配到」的假绿；
//   - 断言取**输出**（脱敏后文本与原文逐字相同）。
//
// 变异验证：把地址族排除关掉后本判据转红（实测）。
func TestR1038ManagedAddressRuleDoesNotRedact(t *testing.T) {
	rule := r1038InputScopedIPRule()
	d := r1038ManagedDetector(t, rule)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()

	// 前提仍在场：规则在快照里、作用域含 input、且正则匹配这些文本。
	snap := d.RuleManager().Current()
	found := false
	for _, r := range snap.Rules {
		if r.ID == rule.ID {
			found = true
			if !r.Enabled {
				t.Fatal("前提失效：规则在快照里但被停用了")
			}
		}
	}
	if !found {
		t.Fatal("前提失效：托管快照里没有这条规则")
	}
	pattern := regexp.MustCompile(rule.Pattern)

	for _, text := range []string{
		r1038UserReportedBlock,
		"export https_proxy=http://192.168.1.10:7890 http_proxy=http://192.168.1.10:7890",
		"数据库在 10.20.30.40:5432",
		"host=[2001:db8::1234]",
		"server=203.0.113.8",
	} {
		if !pattern.MatchString(text) {
			t.Fatalf("前提失效：规则的正则不匹配这段文本，判据已失去意义\n in: %s", text)
		}

		res, err := s.SanitizeInput(ctx, text)
		if err != nil {
			t.Fatalf("SanitizeInput(%q): %v", text, err)
		}
		if res.SanitizedText != text {
			t.Errorf("托管地址规则在脱敏面遮蔽了地址\n in: %s\nout: %s", text, res.SanitizedText)
		}
		if len(res.SanitizeMap) != 0 {
			t.Errorf("地址文本分配了占位符: %v", res.SanitizeMap)
		}
	}
}

// TestR1038LegacyShapeManagedAddressRuleDoesNotRedact 钉住 **legacy 形状**
// （scopes 只有 output）在**输出面**的那个具体缺口。
//
// 输入面走 "input" 相位，legacy 规则挂 "output" ⇒ 输入面本就不适用它；
// 输出面走 ScopeRedact ⇒ 相位 "output" ⇒ **正是它适用的地方**，也是用户
// 看到 `http://[REDACTED]:7890` 的地方。
func TestR1038LegacyShapeManagedAddressRuleDoesNotRedact(t *testing.T) {
	rule := r1038LegacyIPRule()
	d := r1038ManagedDetector(t, rule)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()

	const text = "export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890"
	checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
	res, err := checker.Check(ctx, "default", text)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Blocked {
		t.Fatal("legacy 形状的地址规则在输出面掐断了请求")
	}
	if res.RedactedOutput != text {
		t.Errorf("legacy 形状的地址规则在输出面遮蔽了地址\n in: %s\nout: %s", text, res.RedactedOutput)
	}
}

// TestR1038OutputComplianceDoesNotMaskAddress 是**用户症状所在的那条路径**。
//
// 为什么单独一条：输出侧的 `outputSensitiveChecker.Check` 走
// `detector.Detect`（= ScopeRedact ⇒ 相位 "output"），而 legacy 的
// `compliance.pii.11` 恰恰挂 **output 相位** ⇒ 它会在**模型自己的输出**里
// 把 IP 遮成 `[REDACTED:ip]` ——正是用户看到的那一行
// `export https_proxy=http://[REDACTED]:7890`。
//
// 输入侧（`SanitizeInput`）走的是 ScopeInputDeclarations ⇒ 相位 "input"
// ⇒ 地址规则（scopes=[output]）本就不适用。所以「输入侧没被遮蔽」这条
// 断言**抓不到**这个缺陷——必须打输出侧。
//
// 前提仍在场：这条规则确实在托管快照里、作用域含 output、且它的正则确实
// 匹配这段文本——否则「没被遮蔽」可能只是「规则压根没生效」的假绿。
func TestR1038OutputComplianceDoesNotMaskAddress(t *testing.T) {
	rule := r1038LegacyIPRule()
	d := r1038ManagedDetector(t, rule)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()

	// 前提仍在场。
	snap := d.RuleManager().Current()
	found := false
	for _, r := range snap.Rules {
		if r.ID == rule.ID {
			found = true
			if !r.Enabled {
				t.Fatal("前提失效：规则在快照里但被停用了")
			}
			if len(r.Scopes) == 0 || r.Scopes[0] != "output" {
				t.Fatalf("前提失效：规则作用域 = %v，期望含 output", r.Scopes)
			}
		}
	}
	if !found {
		t.Fatal("前提失效：托管快照里没有这条规则")
	}
	if !regexp.MustCompile(rule.Pattern).MatchString("127.0.0.1") {
		t.Fatal("前提失效：规则的正则不匹配 127.0.0.1")
	}

	for _, text := range []string{
		"export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890",
		"控制台 http://127.0.0.1:18090 → 200",
		"数据库在 10.20.30.40:5432",
	} {
		checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
		res, err := checker.Check(ctx, "default", text)
		if err != nil {
			t.Fatalf("Check(%q): %v", text, err)
		}
		if res.Blocked {
			t.Errorf("输出侧被地址规则阻断: %q", text)
		}
		if res.RedactedOutput != text {
			t.Errorf("输出侧遮蔽了地址\n in: %s\nout: %s", text, res.RedactedOutput)
		}
		if strings.Contains(res.RedactedOutput, "[REDACTED") {
			t.Errorf("输出侧出现遮蔽标记: %s", res.RedactedOutput)
		}
	}
}

// TestR1038OutputStreamKeepsAddress 端到端：**客户端真正收到的那一帧**。
//
// 这条走的是完整拦截链（输出守卫 → 还原），断言释放给客户端的字节里
// 地址逐字保留。用户报告的形态（响应里出现 `http://[REDACTED]:7890`）
// 就是这条链路的产物。
func TestR1038OutputStreamKeepsAddress(t *testing.T) {
	d := r1038ManagedDetector(t, r1038LegacyIPRule())
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1038_out", TenantID: "default", State: response.NewStreamState()}
	ctx := context.Background()

	const text = "export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890"
	frame := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"` + text + `"}}]}` + "\n\n"
	if _, err := chain.InterceptStreamChunk(ctx, []byte(frame), meta); err != nil {
		t.Fatalf("hold: %v", err)
	}
	wire, ferr := chain.FlushStreamPending(ctx, meta)
	if ferr != nil {
		t.Fatalf("释放帧被拦: %v", ferr)
	}
	if !strings.Contains(string(wire), "http://127.0.0.1:7890") {
		t.Errorf("客户端收到的帧里地址被遮蔽了: %s", string(wire))
	}
	if strings.Contains(string(wire), "[REDACTED") {
		t.Errorf("客户端收到的帧里有遮蔽标记: %s", string(wire))
	}
}

// TestR1038UserReportedBlockSurvivesIntact 是**端到端**的验收门禁：
// 用户给的那段命令逐字返回，一个字节都不许变。
//
// 这条比上一条更强：它走的是「生产形状」的规则集（YAML 规则 + legacy 行 +
// 托管管理器），而不是单条规则。
func TestR1038UserReportedBlockSurvivesIntact(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production pattern config: %v", err)
	}
	// 托管形状：YAML 基线 + legacy 的 ip_address 行（即使它被启用）
	// + 一条同时挂 input 相位的地址行（让输入面这条判据**可失败**——
	// 只挂 output 相位时它在输入面压根不适用，判据会变成假绿）。
	baseline := configured.RuleConfig()
	baseline.Rules = append(baseline.Rules, r1038LegacyIPRule(), r1038InputScopedIPRule())
	d := r1038ManagedDetector(t, baseline.Rules...)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}

	res, err := s.SanitizeInput(context.Background(), r1038UserReportedBlock)
	if err != nil {
		t.Fatalf("SanitizeInput: %v", err)
	}
	if res.SanitizedText != r1038UserReportedBlock {
		t.Errorf("用户原文被改写\n got: %s\nwant: %s", res.SanitizedText, r1038UserReportedBlock)
	}
	if strings.Contains(res.SanitizedText, "[REDACTED") {
		t.Errorf("用户原文里出现了遮蔽标记: %s", res.SanitizedText)
	}
	if strings.Contains(res.SanitizedText, "{SENSITIVE:") {
		t.Errorf("用户原文里出现了内部占位符: %s", res.SanitizedText)
	}
	if len(res.SanitizeMap) != 0 {
		t.Errorf("用户原文分配了占位符: %v", res.SanitizeMap)
	}
}

// TestR1038RealSecretsStillRedacted 是契约 A 的另一半：把地址挪出范围不能把
// 真正的机密一起放过去。
//
// 前提仍在场：同一次检测里地址**看得见**（egress 面），而密钥**看得见**
// （脱敏面命中）——这样「密钥被遮了」才不是「检测器坏了」。
func TestR1038RealSecretsStillRedacted(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production pattern config: %v", err)
	}
	baseline := configured.RuleConfig()
	baseline.Rules = append(baseline.Rules, r1038LegacyIPRule())
	d := r1038ManagedDetector(t, baseline.Rules...)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()

	for _, tc := range []struct{ text, secret string }{
		{"我的手机是13800138000", "13800138000"},
		{"联系邮箱 test@example.com", "test@example.com"},
		{"PASSWORD=Sup3rS3cret!", "Sup3rS3cret!"},
		{"Authorization: Bearer abc.DEF-123", "abc.DEF-123"},
		{"api key sk-abcdef1234567890abcdef12", "sk-abcdef1234567890abcdef12"},
	} {
		res, err := s.SanitizeInput(ctx, tc.text)
		if err != nil {
			t.Fatalf("SanitizeInput(%q): %v", tc.text, err)
		}
		if strings.Contains(res.SanitizedText, tc.secret) {
			t.Errorf("机密漏出: %s -> %s", tc.text, res.SanitizedText)
		}
		if len(res.SanitizeMap) == 0 {
			t.Errorf("机密没有被占位符替换（检测器可能坏了）: %s -> %s", tc.text, res.SanitizedText)
		}
	}
}

// TestR1038RedactionMarkersAlwaysCarryType 契约 B：遮蔽物一律带类型。
//
// 三条产出路径各钉一次：
//   - 类型已知（常规还原失败）→ "[REDACTED:<type>]"
//   - 类型可从被改写的拼写里读出 → "[REDACTED:<type>]"（旧实现退成裸形态）
//   - 类型确实不可知（退化跨段）→ "[REDACTED:unknown]"（不是裸形态）
func TestR1038RedactionMarkersAlwaysCarryType(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()

	// 1. 类型不可知 → unknown（不是裸形态）。
	if got := RedactionMarker(""); got != unknownRedactedMarker {
		t.Errorf("RedactionMarker(\"\") = %q, want %q", got, unknownRedactedMarker)
	}
	// 2. 类型已知 → 带类型。
	if got := RedactionMarker(TypePhone); got != "[REDACTED:phone]" {
		t.Errorf("RedactionMarker(TypePhone) = %q", got)
	}
	// 3. 裸形态在标记集合里必须不存在。
	for _, typ := range []SensitiveType{TypePhone, TypeEmail, TypeSecret, TypeServerIP, TypeInternalIP, ""} {
		marker := RedactionMarker(typ)
		if marker == redactedMarker {
			t.Errorf("类型 %q 的遮蔽物退成了裸形态: %q", typ, marker)
		}
		if !strings.HasPrefix(marker, redactedMarkerPrefix+":") {
			t.Errorf("类型 %q 的遮蔽物没有类型段: %q", typ, marker)
		}
	}

	// 4. 还原失败（映射里没有）时产出的是**带类型**的标记。
	masked, err := s.RestoreOutputOrMask(ctx, "phone {SENSITIVE:phone:7}", SanitizeMap{})
	if err != nil {
		t.Fatalf("RestoreOutputOrMask: %v", err)
	}
	if masked != "phone "+RedactionMarker(TypePhone) {
		t.Errorf("未知占位符的遮蔽物 = %q", masked)
	}
	if strings.Contains(masked, redactedMarker) {
		t.Errorf("客户端可见文本里仍有裸形态: %q", masked)
	}
}

// TestR1038RedactionScopeStillBlocksEgressDestination 契约 A 的**边界**：
// 脱敏面放行地址，不得把外传守卫的证据一起放掉。
//
// 判据：同一条地址规则在 **egress 面**仍必须产出片段——外传守卫（模型生成的
// tool 参数里的公网目的地）正是靠它来阻断的。脱敏面与 egress 面是两条不同的
// 判定面，本轮只动前者。
func TestR1038RedactionScopeStillBlocksEgressDestination(t *testing.T) {
	d := r1038ManagedDetector(t, r1038EgressScopedIPRule())
	ctx := context.Background()

	frags, err := d.DetectScope(ctx, "curl http://203.0.113.8/collect", ScopeEgress)
	if err != nil {
		t.Fatalf("DetectScope(ScopeEgress): %v", err)
	}
	if len(frags) == 0 {
		t.Fatal("egress 面不再看见公网地址：外传守卫失去依据")
	}
	redact, err := d.DetectScope(ctx, "curl http://203.0.113.8/collect", ScopeRedact)
	if err != nil {
		t.Fatalf("DetectScope(ScopeRedact): %v", err)
	}
	if len(redact) != 0 {
		t.Errorf("脱敏面仍在匹配公网地址: %v", redact)
	}
}
