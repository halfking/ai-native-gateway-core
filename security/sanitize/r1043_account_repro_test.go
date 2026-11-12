package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// ──────────────────────────────────────────────────────────────────────────
// r1043 回归钉（2026-11-13，211 request 594aed87…，ZCode/3.18.0 glm-5.3-flash
// 事故轮）：敏感信息脱敏/恢复域的「字典投毒」三缺陷——
//
//	D1 标记投毒：`SSH_USER="[REDACTED:account]"` 被当账号赋值 ⇒ 遮蔽标记本身
//	   铸成占位符值（生产实证 {SENSITIVE:account:6}→[REDACTED:account]）；
//	D2 代码投毒：模型代码片段被当账号值 ⇒ 输入脱敏挖走含引号 span ⇒ 模型
//	   收到的代码语法残缺（生产实证 account:34/22/15/28 四条毒值）；
//	D3 知名账号触发：`SSH_USER="root"` 占位符化 ⇒ 引爆「过滤器捉迷藏」。
//
// 修复面：detector 守卫（D2/D3）+ detectionExemptSpans 标记免疫（D1）+
// smart_sani_guard 字典清洗（D1 存量）。真阳性对照（zhangsan_ops_2024 /
// kaixuan.admin）钉住「修复没有放宽」。
// ──────────────────────────────────────────────────────────────────────────

// publishR1043Snapshot 把 211 sanitize_rule_versions id=2 的 account 族规则
// （逐字拷贝 pattern）发布进内存规则管理器，回放生产命中面。
func publishR1043Snapshot(t *testing.T, m *sanitizerules.Manager) {
	t.Helper()
	piiAccount := sanitizerules.Rule{
		ID: "pii.account", Name: "pii.account", Type: "account", Kind: "account_declaration", Group: 1,
		Action: "mask", Scopes: []string{"input"}, Enabled: true,
		Pattern: `(?i)(?:account|user(?:name|_name)?|[a-z0-9_]*_user|login|帐号|账号|账户|用户名)["']?(?:\*{1,2}|` + "`" + `)?\s*(?:[:=：]\s*|(?:改成|改为|设置为|设为|更新为|是|为)\s*|\s+(?:is|(?:set|changed?|updated?)\s+to)\s+)((?:\$\{[^}\r\n]*\}|"(?:\\.|[^"\\])*"|'[^']*'|` + "`" + `[^` + "`" + `]*` + "`" + `|“[^”]*”|‘[^’]*’|(?:\{SENSITIVE:[a-z_]+:[0-9]+\}|[^\s"'` + "`" + `“”‘’,;，；。！？<>{}\]=]*)+))`,
	}
	assignment := piiAccount
	assignment.ID = "pii.account.assignment.output"
	assignment.Name = "pii.account.assignment.output"
	assignment.Kind = "account_assignment"
	assignment.Scopes = []string{"output"}
	base := m.Current().Version
	if _, err := m.Publish(context.Background(), base, sanitizerules.Config{Rules: []sanitizerules.Rule{piiAccount, assignment}}, "r1043", "production snapshot v2 replay"); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestR1043_IncidentPoisonShapesAreNotMinted 逐条回放生产毒值形状：输入面
// （ScopeInputDeclarations = sanitizeInputStable 的检测面）必须零命中——
// 每条都来自事故会话 Redis 字典的实证值或其原始文本。
func TestR1043_IncidentPoisonShapesAreNotMinted(t *testing.T) {
	d, m := managedFixture(t)
	publishR1043Snapshot(t, m)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	for _, text := range []string{
		// D1：标记本身作为赋值值（生产实证 {SENSITIVE:account:6} 的来源形状）。
		`SSH_USER="[REDACTED:account]"`,
		// D2：模型修补代码（生产实证 account:34/22 的来源形状，逐字回放）。
		`new = line[:line.index('{SENSITIVE')+20)+']', 'SSH_USER='`,
		`if 'SSH_USER=' in l or l.startswith('[ -n "$SSH_USER" ]'):`,
		// D2：shell 片段（生产实证 account:15/28）。
		`echo "user=\$(docker inspect ..."`,
		`DB_USER=${DB_USER:-$(whoami)}`,
		// D3：公开默认账号（事故触发器，模型原话「总是将默认的 root 替换」）。
		`SSH_USER="root"`,
		`USER=root ./deploy.sh`,
		`SSH_USER=ubuntu`,
		`db_user: postgres`,
	} {
		fragments, err := d.DetectScope(ctx, text, ScopeInputDeclarations)
		if err != nil {
			t.Fatalf("input %q: err=%v", text, err)
		}
		if len(fragments) != 0 {
			t.Fatalf("input %q: poison shape minted %+v", text, fragments)
		}
		s, _ := NewSanitizer(d)
		res, err := s.SanitizeInput(ctx, text)
		if err != nil {
			t.Fatalf("sanitize %q: err=%v", text, err)
		}
		if res.SanitizedText != text {
			t.Fatalf("sanitize %q: body changed to %q", text, res.SanitizedText)
		}
	}
}

// TestR1043_TruePositiveAccountsStayCaught 钉住修复没有放宽：真实人名/工号
// 账号在输入面照常占位符化、在输出面照常遮蔽。
func TestR1043_TruePositiveAccountsStayCaught(t *testing.T) {
	d, m := managedFixture(t)
	publishR1043Snapshot(t, m)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	for _, text := range []string{
		"数据库账号: zhangsan_ops_2024",
		"username: kaixuan.admin",
		`ssh_user="niuer.prod"`,
	} {
		fragments, err := d.DetectScope(ctx, text, ScopeInputDeclarations)
		if err != nil {
			t.Fatalf("input %q: err=%v", text, err)
		}
		if len(fragments) == 0 {
			t.Fatalf("input %q: true positive lost", text)
		}
	}

	// 输出面（ScopeRedact = mandatory 文本 lane 的检测面）：真阳性账号赋值
	// 照常遮蔽，root 赋值不再遮蔽（D3 对 output 同口径）。
	for _, tc := range []struct {
		text   string
		masked bool
	}{
		{"username: zhangsan_ops_2024", true},
		{`SSH_USER="root"`, false},
		{`USER="admin"`, false},
	} {
		result, err := d.CheckManagedOutput(ctx, "default", tc.text)
		if err != nil {
			t.Fatalf("output %q: err=%v", tc.text, err)
		}
		if result.Blocked {
			t.Fatalf("output %q: blocked", tc.text)
		}
		hasMask := strings.Contains(result.RedactedOutput, "[REDACTED:account]")
		if hasMask != tc.masked {
			t.Fatalf("output %q: masked=%v want %v (redacted=%q)", tc.text, hasMask, tc.masked, result.RedactedOutput)
		}
	}
}

// TestR1043_MaskStringIsImmuneFromAllRules 标记免疫必须对所有规则族生效
// （不只 account）：secret 族命中「值恰好是标记」的形状同样豁免。用基线内置
// 规则（secret 声明族在基线里就有，且基线证明守卫对非托管来源同口径）。
func TestR1043_MaskStringIsImmuneFromAllRules(t *testing.T) {
	d, _ := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	// 对照：基线 secret 规则确实在工作（真凭据照常命中）。
	if frags, err := d.DetectScope(ctx, "password: hunter2secret", ScopeInputDeclarations); err != nil || len(frags) == 0 {
		t.Fatalf("baseline secret rule not active: %v %+v", err, frags)
	}
	for _, text := range []string{
		`password: [REDACTED:secret]`,
		`token="[REDACTED]"`,
		`api_key: [redacted:secret]`,
		`SSH_USER="[REDACTED:account]"`,
	} {
		fragments, err := d.DetectScope(ctx, text, ScopeInputDeclarations)
		if err != nil {
			t.Fatalf("input %q: err=%v", text, err)
		}
		for _, f := range fragments {
			if strings.Contains(f.Value, "REDACTED") || strings.Contains(f.Value, "redacted") {
				t.Fatalf("input %q: mask string minted as %s[%s]", text, f.Type, f.Value)
			}
		}
	}
}

// TestR1043_PoisonedDictionaryIsPurgedOnLoad 字典清洗：存量投毒条目（值恰好
// 是遮蔽标记）在输入侧字典载入与还原侧 loadMap 两处都被丢弃——旧回声按未
// 命中 fail-closed 遮蔽，不再把标记当真值写回。
func TestR1043_PoisonedDictionaryIsPurgedOnLoad(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-r1043", "session-r1043-poison"
	// 模拟事故会话的投毒字典：两条毒值（大小写形态各一）+ 一条真值。
	require := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	require(rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:account:6}", "[REDACTED:account]").Err())
	require(rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:secret:9}", "[redacted:secret]").Err())
	require(rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:account:1}", "zhangsan_ops_2024").Err())

	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := it.loadMap(context.Background(), session, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sm["{SENSITIVE:account:6}"]; ok {
		t.Fatal("poisoned account:6 survived loadMap purge")
	}
	if _, ok := sm["{SENSITIVE:secret:9}"]; ok {
		t.Fatal("poisoned secret:9 survived loadMap purge")
	}
	if v, ok := sm["{SENSITIVE:account:1}"]; !ok || v != "zhangsan_ops_2024" {
		t.Fatalf("clean entry lost: %q %v", v, ok)
	}

	// 还原行为（非流式整链）：真值照常还原；毒值回声按未命中走遮蔽
	// （fail-closed），拒绝把毒值原样写回。
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"user={SENSITIVE:account:1} old={SENSITIVE:secret:9}"}}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.ShouldBlock {
		t.Fatalf("unexpected block: %+v err=%v", result, err)
	}
	out := string(result.ModifiedBody)
	if !strings.Contains(out, "zhangsan_ops_2024") {
		t.Fatalf("clean value not restored: %s", truncStr(out, 200))
	}
	if strings.Contains(out, "[redacted:secret]") {
		t.Fatalf("poison value written back verbatim: %s", truncStr(out, 200))
	}
	if strings.Contains(out, "{SENSITIVE:") {
		t.Fatalf("unresolved marker leaked verbatim: %s", truncStr(out, 200))
	}
}

// TestR1043_RestoreRefusesPoisonValueDirect restore 层单点断言：
// 值为标记的映射条目即使直接喂进 SanitizeMap，也必须被视为毒值拒绝还原
// （SessionValues/sanitizeMapValues 同款过滤的判据钉）。
func TestR1043_RestoreRefusesPoisonValueDirect(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sm := SanitizeMap{
		"{SENSITIVE:account:6}": "[REDACTED:account]",
		"{SENSITIVE:account:1}": "zhangsan_ops_2024",
	}
	restored, err := s.RestoreOutputOrMask(ctx, "a={SENSITIVE:account:1} b={SENSITIVE:account:6}", sm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(restored, "zhangsan_ops_2024") {
		t.Fatalf("clean value not restored: %q", restored)
	}
	if strings.Contains(restored, "{SENSITIVE:") {
		t.Fatalf("unresolved marker leaked verbatim: %q", restored)
	}
	// 毒值不得原样写回（遮蔽是唯一出路）。
	if strings.Contains(restored, "= [REDACTED:account] b") {
		t.Fatalf("poison value written back verbatim: %q", restored)
	}
}

// TestR1043_InputMiddlewareKeepsScriptExecutable 全链路（中间件）级：ZCode
// 形态的请求体里，部署脚本正文与模型修补代码必须逐字到达上游——既不占位
// 符化也不挖 span。用基线内置规则（证明守卫对非托管来源同口径）。
func TestR1043_InputMiddlewareKeepsScriptExecutable(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	d := NewPatternDetector()
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	script := "SSH_USER=\"root\"\nDB_USER=${DB_USER:-$(whoami)}\nif 'SSH_USER=' in l or l.startswith('[ -n \"$SSH_USER\" ]'):\n"
	body := []byte(`{"model":"glm-5.3-flash","messages":[` +
		`{"role":"assistant","content":"reading back the deploy script"},` +
		`{"role":"tool","content":` + jsonString(t, script) + `}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	captured := ""
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		captured = string(got)
		w.WriteHeader(http.StatusOK)
	})
	mw.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(captured, `SSH_USER=\"root\"`) && !strings.Contains(captured, `SSH_USER="root"`) {
		t.Fatalf("script ssh user mangled by middleware: %s", truncStr(captured, 200))
	}
	if !strings.Contains(captured, "$(whoami)") {
		t.Fatalf("shell expansion mangled by middleware: %s", truncStr(captured, 200))
	}
	if !strings.Contains(captured, "l.startswith(") {
		t.Fatalf("python code mangled by middleware: %s", truncStr(captured, 200))
	}
	if strings.Contains(captured, "{SENSITIVE:account:") {
		t.Fatalf("account placeholder minted for script content: %s", truncStr(captured, 200))
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
