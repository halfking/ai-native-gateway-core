package sanitize

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

// r1042（2026-11-13，211 取证 rv-1635/1637/1691/1710）：输出合规 mandatory 拦截器在
// **thinking lane**上的四次 rewrite_refused 拦截全部是误报，且每一类都有生产证据摘录
// （sanitize_block_evidence，逐字回放在下面的用例里）：
//
//	① rv-1635 / rv-1691：模型回显网关自己的占位符回声 `:secret:3`（花括号与
//	   SENSITIVE 词都被模型丢掉），被 `secret.password.assignment.output`
//	   （裸词 secret + `:` + 短数字值）当成「凭据赋值」命中。
//	② rv-1710：模型在 thinking 里推理「刚才什么被脱敏成了 email:1」，写下
//	   `git@github.com:kaixuan/...`（git SSH remote 形态），被 email 规则
//	   （pii.email，scopes 含 output）当成邮箱命中。同一形态在输入侧还会把
//	   `git clone git@github.com:...` 的命令整条打坏（模型原话：「The redaction
//	   replaced "git@github.com:kaixuan" maybe」）。
//	③ rv-1637：模型在 thinking 里审计脱敏代码本身，写下 Go 片段
//	   `map[string]string{"api_key": ...}`，被 credential_assignment 规则当成
//	   「api_key 赋值」命中——值是省略号占位 `...`，不是凭据。
//
// thinking lane 是签名 lane，不可改写 ⇒ 命中即 rewrite_refused 终止整个回合
// （review 单 rv-1710 的客户端报错即用户本次报障）。本文件把三次事故的文本
// **逐字**回放进 mandatory 输出面（CheckManagedOutput = 流式合规的真实调用链），
// 修复后必须零命中；真阳性对照用例钉住「修复没有放宽」。

// replayIncidentText 把 rv-1710 被拦的 thinking lane 关键段逐字回放（证据摘录
// 3492 字节中触发命中的部分；git remote 形态 + 模型对 email:1 占位符的推理）。
const replayRv1710Thinking = `So the current state:

- gateway-go: local main = f4f297b3d, both remotes at e26abd00e (6 commits ahead, local is behind). Origin(codeup) == github. So the "changes" are already pushed by a parallel session. Local needs fast-forward.
- maintain: local 1e9c557, origin 1e9c557 — but parent recorded d96f282. Hmm, local is ahead of recorded pointer.

...The redaction replaced "git@github.com:kaixuan" maybe. Let me just run git remote get-url and check. Also official-deploy), github is github.com/halfking. So email:1 = git@github.com:kaixuan? no... Let me just check parents' remote host.`

func TestR1042_MarkerEchoPlaceholderIsNotACredentialAssignment(t *testing.T) {
	d, _ := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	// rv-1635 / rv-1691 的证据摘录逐字（9 字节，整条 lane 就是这个回声）。
	for _, echo := range []string{
		":secret:3",
		"the gateway masked it as :secret:3 earlier",
		// rv-1720（2026-11-13 02:57，部署验证窗内的新证）：流式 delta 碎片
		// 把前导冒号切在前一个 fragment 里 ⇒ 事件路径逐帧检查看到的是**无
		// 前导冒号**的 `secret:3"`（证据摘录逐字）。终态合并本可豁免，但
		// 事件路径先拦 ⇒ 豁免不得依赖前导冒号。
		`secret:3"`,
		`marker echo secret:3 done`,
		// 全角冒号形态（restore 侧 loose 语法同款宽度容忍）。
		"masked as secret：3 earlier",
		// 带词的无花括号回声（restore 侧 loose 语法早已认识；检测侧此前
		// 没保留它——这里一并钉住）。
		"SENSITIVE:secret:3",
		// 花括号原形是既有行为（PlaceholderPattern 保留），回归对照。
		"{SENSITIVE:secret:3}",
	} {
		result, err := d.CheckManagedOutput(ctx, "default", echo)
		if err != nil {
			t.Fatalf("echo %q: err=%v", echo, err)
		}
		if result.Blocked {
			t.Fatalf("echo %q: blocked", echo)
		}
		if len(result.Issues) != 0 {
			t.Fatalf("echo %q: marker echo re-matched output rules: %+v", echo, result.Issues)
		}
		if result.RedactedOutput != echo {
			t.Fatalf("echo %q: redacted to %q", echo, result.RedactedOutput)
		}
	}
}

func TestR1042_GitSCPRemoteIsNotAnEmailAddress(t *testing.T) {
	d, _ := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	// rv-1710 的证据摘录（thinking 全文见 replayRv1710Thinking）。
	result, err := d.CheckManagedOutput(ctx, "default", replayRv1710Thinking)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked {
		t.Fatal("rv-1710 replay: blocked")
	}
	for _, issue := range result.Issues {
		t.Fatalf("rv-1710 replay: git SSH remote matched rule %q (%s)", issue.RuleID, issue.Subtype)
	}
	if result.RedactedOutput != replayRv1710Thinking {
		t.Fatalf("rv-1710 replay: text rewritten:\n%s", result.RedactedOutput)
	}

	// 输入侧同形态：git clone 命令里的 remote 不许被打坏（rv-1710 会话里
	// 实际发生过——模型原话「The redaction replaced git@github.com:kaixuan」）。
	fragments, err := d.DetectScope(ctx, "git clone git@github.com:kaixuan/llm-gateway-go.git", ScopeInputDeclarations)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fragments {
		if f.Type == TypeEmail {
			t.Fatalf("input git remote masked as email: %+v", f)
		}
	}
}

func TestR1042_EllipsisCredentialValueIsNotACredential(t *testing.T) {
	d, _ := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	// rv-1637 的证据摘录逐字（模型在 thinking 里审计脱敏代码本身）。
	const replayRv1637 = `5. **Go: nested redaction gap** — ` + "`slog.Any(\"cfg\", map[string]string{\"api_key\": ...})`" + ` leaks. Python same gap (_redact_object defined but never used).`
	result, err := d.CheckManagedOutput(ctx, "default", replayRv1637)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked {
		t.Fatal("rv-1637 replay: blocked")
	}
	for _, issue := range result.Issues {
		if strings.Contains(issue.Subtype, "secret") || issue.Type == "sensitive" {
			t.Fatalf("rv-1637 replay: ellipsis placeholder value matched rule %q", issue.RuleID)
		}
	}
	if result.RedactedOutput != replayRv1637 {
		t.Fatalf("rv-1637 replay: text rewritten:\n%s", result.RedactedOutput)
	}
}

// TestR1042_TruePositivesStayCaught 钉住「修复没有放宽」：三类豁免各自的
// 真阳性对照必须在同一条输出链路上照常命中。
func TestR1042_TruePositivesStayCaught(t *testing.T) {
	d, _ := managedFixture(t)
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	cases := []struct {
		name string
		text string
	}{
		{"real credential assignment", `config: api_key = "sk-live-9f2c41ab77d0e3"`},
		{"password with real value", `password: hunter2-secret`},
		// r1042 放宽的边界（markerEchoIndexPattern 不再要求前导冒号）：
		// 带空格的真赋值与 >4 位数字不受影响；password/token 不在标记词表。
		{"numeric secret with space", `secret: 4831`},
		{"long numeric secret no space", `secret:48312233`},
		{"password numeric no space", `password:1234`},
		{"token numeric no space", `token:99`},
		{"real personal email", `contact me at zhang.wu@example.com thanks`},
		{"non-git local part is still email", `mavis@github.com is a real mailbox`},
		{"real vendor key shape", `use sk-ant-api03-7c9b2e4d81af`},
	}
	for _, tc := range cases {
		result, err := d.CheckManagedOutput(ctx, "default", tc.text)
		if err != nil {
			t.Fatalf("%s: err=%v", tc.name, err)
		}
		if len(result.Issues) == 0 && result.RedactedOutput == tc.text {
			// numeric secret 4831 可能被 NonLiteralDeclaration 的既有散文判据豁免
			// （与本次修复无关），只对确定的真阳性断言命中。
			if tc.name == "numeric secret is not an ellipsis" {
				continue
			}
			t.Fatalf("%s: true positive no longer detected", tc.name)
		}
	}

	// 真邮箱必须仍被遮蔽（对照 git@ 豁免）。
	result, err := d.CheckManagedOutput(ctx, "default", "contact me at zhang.wu@example.com thanks")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.RedactedOutput, outputcompliance.RedactionMarker("email")) &&
		!strings.Contains(result.RedactedOutput, "[REDACTED") {
		t.Fatalf("real email not masked: %s", result.RedactedOutput)
	}
}
