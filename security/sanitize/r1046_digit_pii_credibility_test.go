package sanitize

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1046（2026-11-13，211 rv-1735）：数字族 PII（credit_card / id_card / phone）
// 的可信度守卫。
//
// 生产事故（磁盘清理会话 gw_64107a4c…，minimax-m3.1-flash）完整链：
//
//	① 输入面：ps/du 输出里的 Chrome 命令行数字 ID（16-19 位）被快照 v2 的
//	  financial.bank_card（r1039 修复前的旧正则 `\b\d{4}[\s-]?\d{4}[\s-]?\d{4}
//	  [\s-]?\d{4,7}\b`＝任意 16-19 位数字串）与 legacy compliance.pii.5/6
//	  （无边界 `\d{17}[\dXx]` / `\d{15}`）铸成 550 个占位符 ⇒ 模型看不见
//	  自己上下文里的进程 ID 与字节数；
//	② 输出面：模型在 thinking 尾部复述其中一个数字 ⇒ 检测器想遮蔽 ⇒
//	  thinking 是签名 lane 不可改写 ⇒ rewrite_refused 杀整回合（rv-1735，
//	  用户报障）。
//
// 根因＝**规则语义有三层载体，修复只落在其中一层**：r1039 修了 YAML，但
// 生产生效的 DB 快照血统（v1=10-16 → v2=10-29）冻结了旧正则。本守卫是
// 引擎级判据（IIN 前缀族 + Luhn / 省级前缀 + 真实出生日期 / 数字串嵌入
// 豁免），对所有规则来源同口径生效，对已发布快照即刻生效，不动 DB。
//
// 本文件四层门：
//
//	1. 快照 v2 数字族规则**逐字复刻**下的事故文本回放（FP 全灭）；
//	2. 内置 fallback 面同口径；
//	3. 输入面防致盲（SanitizeInput 不得把数字 ID 铸成占位符）；
//	4. 真阳性对照（「收窄不得拿召回换精度」——r1039 判据纪律）。

// r1046SnapshotV2DigitRules 逐字复刻 211 生产快照 v2（sanitize_rule_versions
// id=2，2026-10-29 发布）里**数字族全部规则**的正则与 scope——旧正则原样
// 保留（含无边界形态），证明守卫对「快照血统冻结的宽松规则」同样生效。
// 复刻来源：sanitize_rule_head.version_id=2 的 config（2026-11-13 04:0x 导出）。
func r1046SnapshotV2DigitRules() []sanitizerules.Rule {
	return []sanitizerules.Rule{
		{ID: "pii.id_card.egress", Kind: "pattern", Name: "pii.id_card.egress", Type: "id_card", Action: "block", Scopes: []string{"egress"}, Enabled: true,
			Pattern: `\b[1-9]\d{5}(18|19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`},
		{ID: "pii.id_card", Kind: "pattern", Name: "pii.id_card", Type: "id_card", Action: "mask", Scopes: []string{"input", "output"}, Enabled: true,
			Pattern: `\b[1-9]\d{5}(18|19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`},
		{ID: "pii.phone.egress", Kind: "pattern", Name: "pii.phone.egress", Type: "phone", Action: "block", Scopes: []string{"egress"}, Enabled: true,
			Pattern: `\b1[3-9]\d{9}\b`},
		{ID: "pii.phone", Kind: "pattern", Name: "pii.phone", Type: "phone", Action: "mask", Scopes: []string{"input", "output"}, Enabled: true,
			Pattern: `\b1[3-9]\d{9}\b`},
		{ID: "financial.bank_card.egress", Kind: "pattern", Name: "financial.bank_card.egress", Type: "credit_card", Action: "block", Scopes: []string{"egress"}, Enabled: true,
			Pattern: `\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4,7}\b`},
		{ID: "financial.bank_card", Kind: "pattern", Name: "financial.bank_card", Type: "credit_card", Action: "mask", Scopes: []string{"input", "output"}, Enabled: true,
			Pattern: `\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4,7}\b`},
		// legacy compliance.pii.*（pii_patterns 表导入）：全部无词边界。
		{ID: "compliance.pii.2", Kind: "pattern", Name: "compliance.pii.2", Type: "phone", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `1[3-9]\d{9}`},
		{ID: "compliance.pii.3", Kind: "pattern", Name: "compliance.pii.3", Type: "phone", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `1[3-9]\d-\d{4}-\d{4}`},
		{ID: "compliance.pii.4", Kind: "pattern", Name: "compliance.pii.4", Type: "phone", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `\+\d{1,3}[-\s]?\d{7,14}`},
		{ID: "compliance.pii.5", Kind: "pattern", Name: "compliance.pii.5", Type: "id_card", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `\d{17}[\dXx]`},
		{ID: "compliance.pii.6", Kind: "pattern", Name: "compliance.pii.6", Type: "id_card", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `\d{15}`},
		{ID: "compliance.pii.7", Kind: "pattern", Name: "compliance.pii.7", Type: "credit_card", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `4\d{15}`},
		{ID: "compliance.pii.8", Kind: "pattern", Name: "compliance.pii.8", Type: "credit_card", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `5[1-5]\d{14}`},
		{ID: "compliance.pii.9", Kind: "pattern", Name: "compliance.pii.9", Type: "credit_card", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `3[47]\d{13}`},
		{ID: "compliance.pii.12", Kind: "pattern", Name: "compliance.pii.12", Type: "credit_card", Action: "mask", Scopes: []string{"output"}, Enabled: true,
			Pattern: `\d{16,19}`},
	}
}

// r1046FPShapes 是 rv-1735 会话里实测出现的数字形态（request 体逐字：
// Chrome --variations-seed-version / --field-trial-handle / --launch-time-ticks
// 的参数值）＋同族字节计数与时间戳。
var r1046FPShapes = []string{
	"1335099688260238922", // 19 位 Chrome ID（请求体逐字）
	"1716308874569906581", // 19 位 Chrome ID（请求体逐字）
	"1605140610344942925", // 19 位 Chrome ID（请求体逐字）
	"1605140610344942927", // 同族 19 位、Luhn 合法但 IIN 不在发卡行族（IIN 臂靶点）
	"1184007172550",       // 13 位 --launch-time-ticks（请求体逐字）
	"4567890123456789",    // 16 位字节数形态（IIN 命中、Luhn 不过）
	"347123456789012",     // 15 位 AmEx 形态字节数
	"2026102012345678",    // 16 位纳秒时间戳（r1039 原始误伤形态）
	"138001380001234567",  // 18 位「phone 前缀 + 尾数」结构不合法串
	"110101199003074X999", // 16 位截断 ID 粘连数字（r1038b 原样本）
}

// TestR1046SnapshotV2LooseRulesDoNotKillIncidentShapes：把生产快照 v2 的
// 数字族规则（旧正则原样复刻）装进规则管理器，事故形态必须零命中、零遮蔽
// 意图——rv-1735 的杀回合机制（mask 意图 × thinking 不可改写）由此消灭。
func TestR1046SnapshotV2LooseRulesDoNotKillIncidentShapes(t *testing.T) {
	d := NewPatternDetector()
	m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), d.RuleConfig(), sanitizerules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d.SetRuleManager(m)
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Publish(context.Background(), 1, sanitizerules.Config{Rules: r1046SnapshotV2DigitRules()}, "r1046", "snapshot v2 digit-family replica"); err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	for _, shape := range r1046FPShapes {
		// thinking lane 形态：模型复述 ps 输出里的数字 ID。
		thinking := "the Chrome processes use variations seed " + shape + " which is just a random id from ps output"
		result, err := d.CheckManagedOutput(ctx, "default", thinking)
		if err != nil {
			t.Fatalf("shape %q: err=%v", shape, err)
		}
		if result.Blocked || len(result.Issues) != 0 || result.RedactedOutput != thinking {
			t.Fatalf("snapshot v2 loose rule re-matched incident shape %q: blocked=%v issues=%+v redacted=%q",
				shape, result.Blocked, result.Issues, result.RedactedOutput)
		}
	}
}

// TestR1046FallbackDetectorSameVerdict：内置 fallback 面（YAML/快照都不在的
// 部署形态）同口径。
func TestR1046FallbackDetectorSameVerdict(t *testing.T) {
	d := NewPatternDetector()
	ctx := context.Background()
	for _, shape := range r1046FPShapes {
		fragments, err := d.Detect(ctx, "seed "+shape+" id")
		if err != nil {
			t.Fatal(err)
		}
		if len(fragments) != 0 {
			t.Fatalf("fallback detector matched incident shape %q: %+v", shape, fragments)
		}
	}
}

// TestR1046InputFaceDoesNotPlaceholderizeNumericIDs：rv-1735 的另一半——
// 输入面把 ps/du 输出铸成 550 个 credit_card 占位符，模型整晚看不见自己的
// 上下文。同一段请求体形态在修复后必须原样到达模型。
func TestR1046InputFaceDoesNotPlaceholderizeNumericIDs(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatal(err)
	}
	const psOutput = `/Applications/Chrome --variations-seed-version=1935764596,r,1335099688260238922,1716308874569906581 --launch-time-ticks=1184007172550 --field-trial-handle=1605,140610344942929257,262144 --enable-features=Foo
.build-local/.gocache is 9.8G, ~/Library/Caches/go-build is 91G (4567890123456789 bytes), Chrome-rollback 1.4G`
	res, err := s.SanitizeInput(context.Background(), psOutput)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"credit_card", "id_card"} {
		if strings.Contains(res.SanitizedText, "{SENSITIVE:"+typ+":") {
			t.Fatalf("numeric id placeholderized as %s: %q", typ, res.SanitizedText)
		}
	}
	if res.SanitizedText != psOutput {
		t.Fatalf("ps/du output altered: %q", res.SanitizedText)
	}
}

// TestR1046GenuinePIIStillDetectedEverywhere：真阳性对照（r1039 判据纪律：
// 收窄不得拿召回换精度）——三面（YAML / 快照 v2 复刻 / fallback）上，真实
// 卡号（IIN 家族 × Luhn 合法）、结构合法身份证、独立手机号必须照常检出。
func TestR1046GenuinePIIStillDetectedEverywhere(t *testing.T) {
	// (值, 期望类型)——值全部 Luhn/结构合法（测试卡号 + r1036/r1039 既有钉值）。
	genuine := []struct {
		value, typ string
	}{
		{"4111111111111111", "credit_card"},    // Visa
		{"4111 1111 1111 1111", "credit_card"}, // Visa 四位分组
		{"4111-1111-1111-1111", "credit_card"}, // Visa 连字符分组
		{"5500005555555559", "credit_card"},    // Mastercard
		{"340000000000009", "credit_card"},     // AmEx 15 位
		{"6011000000000004", "credit_card"},    // Discover
		{"6222021234567890128", "credit_card"}, // UnionPay 19 位（Luhn 合法）
		{"3530111333300000", "credit_card"},    // JCB
		{"110101199003078515", "id_card"},      // 结构合法身份证（r1036 钉值）
		{"11010119900307123X", "id_card"},      // X 校验位形态
		{"13800138000", "phone"},               // 独立手机号
	}

	// 面 1：YAML 生产规则。
	yamlD, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	// 面 2：快照 v2 旧正则复刻。
	snapD := NewPatternDetector()
	m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), snapD.RuleConfig(), sanitizerules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapD.SetRuleManager(m)
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Publish(context.Background(), 1, sanitizerules.Config{Rules: r1046SnapshotV2DigitRules()}, "r1046", "replica"); err != nil {
		t.Fatal(err)
	}
	// 面 3：fallback。★ fallback 的卡族覆盖本就窄于 YAML/快照（无 35/62
	// 前缀族、不认四位分组分隔符——r1039 只在 YAML 面钉过分组卡形态），
	// 这是既有覆盖差、非本轮回归 ⇒ fallback 面只断言它本来就检得出的子集。
	fbD := NewPatternDetector()
	fallbackSkips := map[string]bool{
		"4111 1111 1111 1111": true, "4111-1111-1111-1111": true,
		"6222021234567890128": true, "3530111333300000": true,
	}

	for _, face := range []struct {
		name string
		d    *PatternDetector
		skip map[string]bool
	}{
		{"yaml", yamlD, nil}, {"snapshot-v2-replica", snapD, nil}, {"fallback", fbD, fallbackSkips},
	} {
		if face.skip == nil {
			face.skip = map[string]bool{}
		}
		for _, g := range genuine {
			if face.skip[g.value] {
				continue
			}
			text := "value is " + g.value + " here"
			fragments, err := face.d.DetectScope(context.Background(), text, ScopeRedact)
			if err != nil {
				t.Fatalf("[%s] %q: %v", face.name, g.value, err)
			}
			found := false
			for _, f := range fragments {
				if string(f.Type) == g.typ {
					found = true
				}
			}
			if !found {
				t.Errorf("[%s] 真值漏检（收窄过头）：%q 期望 %s，实得 %+v", face.name, g.value, g.typ, fragments)
			}
		}
	}
}

// TestR1046CredibilityPureFunctions：可信度判据纯函数的边界钉测（变异臂
// 的靶点都在这里——IIN / Luhn / 结构 / 嵌入四条腿各自独立）。
func TestR1046CredibilityPureFunctions(t *testing.T) {
	// 卡：IIN 命中但 Luhn 不过 ⇒ 出局。
	if credibleCardCandidate("4567890123456789") {
		t.Fatal("Luhn-failing Visa-shaped run accepted")
	}
	// 卡：Luhn 过但 IIN 不在家族（1 开头 19 位）⇒ 出局。
	if credibleCardCandidate("1605140610344942927") {
		t.Fatal("Luhn-passing non-IIN run accepted")
	}
	// 卡：13 位（fallback 宽臂）⇒ 长度出局。
	if credibleCardCandidate("4473456789012") {
		t.Fatal("13-digit run accepted as card")
	}
	// 身份证：15 位（compliance.pii.6 目标）⇒ 长度出局。
	if credibleIDCardCandidate("110101990030740") {
		t.Fatal("15-digit run accepted as id card")
	}
	// 身份证：18 位但日期不真实（月 13 / 2 月 30）⇒ 出局。
	if credibleIDCardCandidate("110101199013011234") {
		t.Fatal("month-13 id accepted")
	}
	if credibleIDCardCandidate("110101199002301234") {
		t.Fatal("Feb-30 id accepted")
	}
	// 身份证：省级前缀不在表（00/99）⇒ 出局。
	if credibleIDCardCandidate("990101199001011234") {
		t.Fatal("invalid province prefix accepted")
	}
	// 嵌入：更长数字串的子串 ⇒ 出局（两侧任一是数字）。
	if standaloneDigitRun("x13800138000y", 1, 12) == false {
		t.Fatal("standalone run misjudged as embedded")
	}
	if standaloneDigitRun("113350996882602389220", 1, 20) {
		t.Fatal("embedded substring misjudged as standalone")
	}
}
