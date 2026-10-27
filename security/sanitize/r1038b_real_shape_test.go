package sanitize

// r1038b_real_shape_test.go — 2026-10-27 对 r1038 的**批判式审计**：
// 把判据从「合成规则」升级到「**生产真实规则集**」。
//
// ## 为什么要有这个文件（r1038 的判据盲区）
//
// r1038（2026-10-20）的 13 条判据钉的是**手工构造的规则**：
// `r1038LegacyIPRule()` / `r1038InputScopedIPRule()` —— 一条
// `type=server_ip / scopes=[output] / action=mask` 的合成规则。
//
// 于是判据与生产之间隔着一层：
//
//   - 生产规则集是 **built-in 基线 + legacy `pii_patterns` 行 + 少数内置补充**
//     （cmd/gateway 的 loadSanitizeRulesBaseline），**不是**一条合成规则；
//   - legacy 行的类型由 `domains/sanitizerules.LegacyPIIRule` 决定，
//     而 r1038 时它住在 package main 里 ⇒ **测试无法直接调用** ⇒ 想验证
//     「生产真实规则集长什么样」的判据只能**手抄一份映射**，抄件会漂移。
//
// 结果是：**生产真实规则集从未被任何判据跑过**。r1038 的修复当时是对的
// （本文件的判据 2 会用真实规则集复现它），但**没有任何机制保证它继续对**。
//
// 本文件补上这一层：
//
//  1. **事实源**：`316_output_compliance_monitoring.sql`（生产播种了
//     `pii_patterns` 12 行的那个迁移）+ `PatternDetector.RuleConfig()`
//     （built-in 基线）+ `outputcompliance.NewBuiltinChecker().RuleConfig()`
//     —— 三样都是**生产自己用的东西**，判据从它们拼出规则集。
//  2. **映射**：`sanitizerules.LegacyPIIRule`（r1038b 已从 package main
//     下沉到可被测试直接调用的位置）—— 生产与判据调**同一个函数**。
//
// 判据纪律：
//   - 「没被遮蔽」必须配「前提仍在场」：判据 1 要求规则集里**确实存在**会
//     命中定位符的规则，否则「没被遮蔽」只是「压根没匹配到」的假绿；
//   - 每条「放行」都要配一条「仍然遮蔽」的反向对照（判据 3）与一条
//     「外传守卫仍然看得见」的安全侧对照（判据 4）——
//     豁免不得越界成「什么都不遮」或「外传也不拦」。

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1038bSeedRow 是 `pii_patterns` 的一行（生产播种的那种）。
type r1038bSeedRow struct {
	name    string
	typ     string
	pattern string
}

// r1038bParseProductionSeed 从 **316 迁移 SQL** 里解析出生产播种的
// `pii_patterns` 行。
//
// 为什么解析 SQL 而不是抄一份表：
//   - 316 迁移**就是**生产事实源（245 上 `pii_patterns` 实测 12 行，与它
//     一一对应）；
//   - 有人往迁移里加一行 / 改一行正则 ⇒ 判据**立刻**跟着变 ⇒ 门禁不会
//     守着过期的规则集转绿。
//
// 解析失败 ⇒ 直接失败（不跳过）：解析器坏了就是「判据已失去意义」。
func r1038bParseProductionSeed(t *testing.T) []r1038bSeedRow {
	t.Helper()
	const path = "../../sql/migrations/startup/316_output_compliance_monitoring.sql"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读生产种子失败（%s）: %v", path, err)
	}
	src := string(b)
	i := strings.Index(src, "INSERT INTO pii_patterns")
	if i < 0 {
		t.Fatalf("316 迁移里没有 pii_patterns 的 INSERT —— 生产种子事实源变了，判据必须同步")
	}
	seg := src[i:]
	if j := strings.Index(seg, "ON CONFLICT"); j > 0 {
		seg = seg[:j]
	}
	re := regexp.MustCompile(`\('([^']*)',\s*'([^']*)',\s*'((?:[^'\\]|\\.)*)'`)
	var rows []r1038bSeedRow
	for _, m := range re.FindAllStringSubmatch(seg, -1) {
		rows = append(rows, r1038bSeedRow{name: m[1], typ: m[2], pattern: m[3]})
	}
	if len(rows) == 0 {
		t.Fatal("没解析出任何生产种子行 —— 判据已失去意义")
	}
	return rows
}

// r1038bProductionRuleSet 拼出**生产真实的规则集**。
//
// 拼装顺序与 cmd/gateway/sanitize_rules_control.go 的
// loadSanitizeRulesBaseline **逐行对应**：
//
//  1. built-in 基线（PatternDetector.RuleConfig()）；
//  2. legacy `pii_patterns` 行（经 sanitizerules.LegacyPIIRule）；
//  3. `compliance.internal_ip`（outputcompliance 内置基线里的那一条）。
//
// legacy 行的 enabled 一律按 **true** 处理（最坏情况）：
// 316 的 INSERT 不写 enabled ⇒ DB 默认 true；851 事后把那行停用了，但那是
// **数据**修复 —— 判据要钉的是「万一它被启用」的那条代码级保证。
func r1038bProductionRuleSet(t *testing.T) []sanitizerules.Rule {
	t.Helper()
	base := NewPatternDetector()
	cfg := base.RuleConfig()
	rules := append([]sanitizerules.Rule(nil), cfg.Rules...)

	for i, row := range r1038bParseProductionSeed(t) {
		rules = append(rules, sanitizerules.LegacyPIIRule(i+1, row.name, row.typ, row.pattern, true))
	}
	for _, r := range outputcompliance.NewBuiltinChecker().RuleConfig().Rules {
		if r.ID == "compliance.internal_ip" {
			rules = append(rules, r)
		}
	}
	return rules
}

// r1038bLocatorSamples 是**定位符**样本 —— 用户口径「脱敏的范围应该不包含
// 域名、地址、ip这些内容」覆盖到的形态。
var r1038bLocatorSamples = []string{
	"export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890",
	"git clone --depth 1 https://github.com/octocat/Hello-World /tmp/gt",
	"curl http://192.168.1.10:8080/api",
	"数据库在 10.20.30.40:5432",
	"host=[2001:db8::1234]",
	"server=203.0.113.8",
	"内网 172.16.0.1",
	"CIDR 10.0.0.0/8",
	"域名 api.github.com",
	"redis.internal:6379",
	"MAC 00:1B:44:11:3A:B7",
	"中文地址 北京市朝阳区建国路88号",
	"https://example.com:8443/path?q=1",
}

// r1038bControlSample 是「前提仍在场」守卫用的**对照串**：
// 它不该被任何**有判别力**的规则命中。
//
// 为什么需要它：规则集里存在 pattern = `(?s).+` 的通配规则
// （builtin.secret_field.* / builtin.account_field.*）。只看「正则匹配不匹配
// 样本」的话，通配规则会匹配**任何**样本 ⇒ 守卫恒成立 ⇒ 把样本换成规则集
// 根本碰不到的东西，门禁照样绿（第一版 M6 臂实测如此）。
//
// 判据：命中样本**且不命中对照串** ⇒ 这条规则真的有判别力。
const r1038bControlSample = "ZZZ_CONTROL_NO_DISCRIMINATING_RULE_MATCHES_THIS_ZZZ"

// r1038bSecretSamples 是**反向对照** —— 豁免不得越界成「什么都不遮」。
var r1038bSecretSamples = []string{
	"13812345678",
	"zhangsan@example.com",
	"110101199003074X",
	"4111111111111111",
	"123-45-6789",
}

// TestR1038bProductionRuleSetNeverMasksLocator 是本文件的**主判据**：
// **生产真实规则集**里，任何一条会命中定位符、且作用在输出面、且动作是遮蔽、
// 且类型**不属于地址族**的规则 —— 都不该存在。
//
// 这条性质直接对应用户口径：「脱敏的范围应该不包含域名、地址、ip」。
//
// 它比 r1038 的合成判据强在三点：
//  1. 跑的是**生产真实规则集**（基线 + 316 种子 + 内置补充）；
//  2. 是**属性**判据（遍历规则集找违例），不是「拿一条规则测一次」；
//  3. 生产种子变化（新增一行 / 改宽正则 / 改声明类型）时**自动跟着变**。
//
// ★ 前提仍在场：规则集里必须**确实存在**会命中定位符的规则 —— 否则这条
//
//	性质是「空真」（vacuous），「没被遮蔽」只是「压根没匹配到」。
//
// ★ 变异验证：
//
//	M1 把 legacy 映射的地址族判定改回「只认 server_ip/internal_ip」；
//	   或把某条定位符规则的类型改成非地址族（custom）⇒ 本判据转红。
func TestR1038bProductionRuleSetNeverMasksLocator(t *testing.T) {
	rules := r1038bProductionRuleSet(t)

	// 逐条规则过一遍：它会不会命中定位符？
	type offender struct {
		rule sanitizerules.Rule
		hit  string
	}
	var offenders []offender
	matchedAny := false

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if !containsString(r.Scopes, "output") {
			continue
		}
		if r.Action != "mask" {
			continue
		}
		if sanitizerules.IsAddressFamilyType(r.Type) {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			t.Fatalf("规则 %s 的正则编译失败: %v", r.ID, err)
		}
		for _, sample := range r1038bLocatorSamples {
			if re.MatchString(sample) {
				offenders = append(offenders, offender{rule: r, hit: sample})
				break
			}
		}
	}

	// 前提仍在场：规则集里必须有规则命中定位符（否则上面是空真）。
	//
	// ★ r1038b 第一版的这个守卫**不够严**：它只看「正则匹配不匹配样本」，
	//   而规则集里存在 pattern = `(?s).+` 的通配规则（builtin.secret_field.* 等）
	//   ⇒ 任何样本都被「匹配」⇒ 守卫恒成立 ⇒ 把样本换成规则集根本碰不到的
	//   东西也照样绿（实测：M6 臂没转红）。
	// ⇒ 守卫改成「**有判别力**的命中」：规则必须
	//   ① 适用在输出面（enabled + scopes 含 output）；
	//   ② 正则命中某个定位符样本；
	//   ③ 且**不**命中对照串（否则是通配规则的假命中）。
	for _, r := range rules {
		if !r.Enabled || !containsString(r.Scopes, "output") {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(r1038bControlSample) {
			continue // 通配规则：没有判别力，不算「前提在场」
		}
		for _, sample := range r1038bLocatorSamples {
			if re.MatchString(sample) {
				matchedAny = true
				break
			}
		}
		if matchedAny {
			break
		}
	}
	if !matchedAny {
		t.Fatal("前提失效：生产真实规则集里没有任何**有判别力**的规则命中定位符样本 ⇒ " +
			"「没被遮蔽」只是「压根没匹配到」的假绿（种子或正则变了？）")
	}

	if len(offenders) > 0 {
		var b strings.Builder
		b.WriteString("生产真实规则集里存在「会遮蔽定位符」的非地址族规则：\n")
		for _, o := range offenders {
			b.WriteString("  · " + o.rule.ID + " type=" + o.rule.Type +
				" scopes=" + strings.Join(o.rule.Scopes, ",") +
				" action=" + o.rule.Action + " 命中样本=" + o.hit + "\n")
		}
		b.WriteString("（用户口径：脱敏的范围应该不包含域名、地址、ip）")
		t.Fatal(b.String())
	}
}

// TestR1038bProductionRuleSetKeepsLocatorsIntact 端到端：把**生产真实规则集**
// 装进检测器，走**真实的两个面**（输入面 SanitizeInput / 输出面
// outputSensitiveChecker.Check），定位符必须逐字存活。
//
// 这条是 r1038 判据的**生产版**（r1038 只跑合成规则）。
func TestR1038bProductionRuleSetKeepsLocatorsIntact(t *testing.T) {
	rules := r1038bProductionRuleSet(t)
	d := r1038ManagedDetector(t, rules...)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()
	checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}

	for _, sample := range r1038bLocatorSamples {
		// 输出面（用户症状所在的那条路径）。
		res, err := checker.Check(ctx, "default", sample)
		if err != nil {
			t.Fatalf("输出面 Check(%q): %v", sample, err)
		}
		if res.Blocked {
			t.Errorf("定位符在输出面被掐断了请求: %q", sample)
		}
		if res.RedactedOutput != sample {
			t.Errorf("输出面遮蔽了定位符\n in: %s\nout: %s", sample, res.RedactedOutput)
		}
		// 输入面。
		in, err := s.SanitizeInput(ctx, sample)
		if err != nil {
			t.Fatalf("输入面 SanitizeInput(%q): %v", sample, err)
		}
		if in.SanitizedText != sample {
			t.Errorf("输入面遮蔽了定位符\n in: %s\nout: %s", sample, in.SanitizedText)
		}
	}
}

// TestR1038bProductionRuleSetStillMasksSecrets 是**反向对照**：豁免不得越界。
// 生产真实规则集下，凭据类内容必须**仍然被遮蔽**。
func TestR1038bProductionRuleSetStillMasksSecrets(t *testing.T) {
	rules := r1038bProductionRuleSet(t)
	d := r1038ManagedDetector(t, rules...)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	ctx := context.Background()
	checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}

	for _, secret := range r1038bSecretSamples {
		res, err := checker.Check(ctx, "default", secret)
		if err != nil {
			t.Fatalf("输出面 Check(%q): %v", secret, err)
		}
		if res.RedactedOutput == secret {
			t.Errorf("凭据未被遮蔽（豁免越界了）: %q", secret)
		}
		if !strings.Contains(res.RedactedOutput, "[REDACTED:") {
			t.Errorf("遮蔽物没有带类型标记: %q => %q", secret, res.RedactedOutput)
		}
	}
}

// TestR1038bEgressGuardStillSeesAddress 是**安全侧对照**：脱敏面放行地址，
// 不得把外传守卫的证据一起放掉。
//
// egress 面与脱敏面是两条不同的判定面。判据用**生产真实规则集**验一次：
// egress 面仍必须看见公网地址（否则外传守卫失去依据）。
func TestR1038bEgressGuardStillSeesAddress(t *testing.T) {
	rules := r1038bProductionRuleSet(t)
	d := r1038ManagedDetector(t, rules...)
	ctx := context.Background()

	const text = "curl http://203.0.113.8/collect"
	frags, err := d.DetectScope(ctx, text, ScopeEgress)
	if err != nil {
		t.Fatalf("DetectScope(ScopeEgress): %v", err)
	}
	if len(frags) == 0 {
		t.Fatal("egress 面不再看见公网地址：外传守卫失去依据")
	}
	redact, err := d.DetectScope(ctx, text, ScopeRedact)
	if err != nil {
		t.Fatalf("DetectScope(ScopeRedact): %v", err)
	}
	if len(redact) != 0 {
		t.Errorf("脱敏面仍在匹配公网地址: %v", redact)
	}
}

// TestR1038bDeclaredAddressTypeIsHonoredByRedactionSurface 钉住
// 「**声明出来的地址族类型**，脱敏面必须认」。
//
// ## 这条判据为什么必须存在（r1038b 审计的一处自伤）
//
// 审计过程中出了一次**自伤**：清理残留时 `git checkout` 把 detector 侧的
// `scopedType` 改动**一起还原了**（只补回了合规面那半）⇒ 两个面又变回
// 两份名单。
//
// 而当时**新判据全绿** —— 因为生产种子（`pii_patterns` 12 行）里**没有
// domain 类型的行** ⇒ 「生产真实规则集」这条路根本走不到域名分支 ⇒
// **判据的适用面有盲区**：它能守住「已存在的规则」，守不住「被声明的类型」。
//
// 这条判据补的正是那个盲区：造一条**声明为 domain** 的规则，断言脱敏面
// **不遮蔽域名**；再用**同一条规则的 custom 形态**做反向对照，断言它
// **照遮** —— 一正一反同时成立，才能证明「豁免是按**声明类型**判定的」，
// 而不是「谁都不遮」。
//
// ★ 变异验证：
//
//	M1 把 scopedType 改回本地名单（丢掉 domain/hostname）⇒ 本判据转红；
//	M7 把豁免改成「一律放行」⇒ 反向对照转红。
func TestR1038bDeclaredAddressTypeIsHonoredByRedactionSurface(t *testing.T) {
	const domainPattern = `[a-z0-9][a-z0-9-]*\.[a-z]{2,}`
	const domainText = "服务地址是 api.github.com 与 redis.internal"

	// 前提仍在场：规则的正则确实匹配这段文本。
	re := regexp.MustCompile(domainPattern)
	if !re.MatchString(domainText) {
		t.Fatalf("前提失效：规则正则不匹配样本 %q", domainText)
	}

	run := func(t *testing.T, ruleType string, wantMasked bool) {
		t.Helper()
		rule := sanitizerules.Rule{
			ID: "r1038b.declared." + ruleType, Name: "declared", Type: ruleType,
			Kind: "pattern", Pattern: domainPattern, Scopes: []string{"output"},
			Action: "mask", Enabled: true,
		}
		d := r1038ManagedDetector(t, rule)
		s, err := NewSanitizer(d)
		if err != nil {
			t.Fatalf("NewSanitizer: %v", err)
		}
		checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
		res, err := checker.Check(context.Background(), "default", domainText)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		masked := res.RedactedOutput != domainText
		if masked != wantMasked {
			t.Errorf("type=%q：遮蔽=%v，期望 %v\n in: %s\nout: %s",
				ruleType, masked, wantMasked, domainText, res.RedactedOutput)
		}
	}

	t.Run("声明为网络定位符（server_ip）⇒ 脱敏面不遮蔽域名", func(t *testing.T) {
		run(t, "server_ip", false)
	})
	t.Run("反向对照：custom 形态照遮（证明豁免按类型判定）", func(t *testing.T) {
		run(t, "custom", true)
	})
	t.Run("反向对照：secret 形态照遮（凭据类不得放行）", func(t *testing.T) {
		run(t, "secret", true)
	})
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
