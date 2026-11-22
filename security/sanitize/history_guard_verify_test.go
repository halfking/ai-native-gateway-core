package sanitize

// 历史守卫「以输入面的候选过滤链为准」的钉测（2026-11-23 修复）。
//
// 背景：守卫此前只看正则。仓内规则下实测——「The request used 1734 tokens.」「the
// keyboard shortcut…」「token count exceeded the limit」「run with Bearer
// authentication」全被判不安全、整条历史被丢，而输入面对它们毫无命中。211 真实
// 会话 3953 条里 2269 条因此被丢（agent 会话里 token 一词无处不在）。
//
// 契约：
//  1. 守卫的判据 = 输入声明面同一份检测主体（detectPatterns）对**完整已解码文本**
//     是否有成立的候选；正则命中但被过滤链豁免的，放行；
//  2. 真实凭据（密码、API 密钥、AWS 密钥、JWT、Bearer 令牌、私钥块）仍然拦截；
//  3. 过大的消息（复核不了）保守省略；复核阶段的读错误与取消如实返回；
//  4. 没有复核器的策略（直接构造）保持修复前的行为。

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func guardVerdict(t *testing.T, d *PatternDetector, text string) (bool, error) {
	t.Helper()
	return d.HistoryGuard(context.Background())(context.Background(), func() io.Reader { return strings.NewReader(text) })
}

func TestHistoryGuardAgreesWithInputScope(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// 输入面放行的措辞：修复前守卫全部判不安全（「token」「key」「api」「Bearer」开头的
	// 词匹配密钥形状规则；「token count」「secret sauce」匹配声明规则）。
	benign := []string{
		"The request used 1734 tokens.",
		"token count exceeded the limit",
		"the keyboard shortcut is Ctrl+K",
		"apiVersion: v1",
		"<total_tokens>14995481 tokens left</total_tokens>",
		"run with Bearer authentication",
		"the secret sauce is patience",
		"password policy requires 12 characters",
		"commit 1234567890abcdef",
		"port 6432 on 10.0.0.8",
		"billing database port 6432 in /srv/billing/db.yaml",
	}
	// 真实凭据形态：必须仍然拦截。
	secrets := []string{
		`{"role":"user","content":"password: hunter2"}`,
		`{"role":"user","content":"export OPENAI_API_KEY=sk-proj-9f3aQk8LmZx2Wv7Tb4GyE5u"}`,
		`{"role":"assistant","content":"aws AKIAIOSFODNN7EXAMPLE"}`,
		`{"role":"assistant","content":"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}`,
		`{"role":"user","content":"Authorization: Bearer abc123def456ghi789jkl012mno345pqr678stu901vwx"}`,
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AHd\n-----END RSA PRIVATE KEY-----",
		`{"arguments":"password = 'Xk9$mQ2vLp7#'", "sauce": "hold"}`,
	}
	for _, text := range benign {
		safe, err := guardVerdict(t, d, text)
		if err != nil || !safe {
			t.Fatalf("benign %q: safe=%v err=%v (input face has nothing to mask here)", text, safe, err)
		}
	}
	for _, text := range secrets {
		safe, err := guardVerdict(t, d, text)
		if err != nil || safe {
			t.Fatalf("secret %q: safe=%v err=%v (must stay omitted)", text, safe, err)
		}
	}
	// 守卫判据与输入面逐条一致：输入面有片段 ⇔ 守卫判不安全。
	for _, text := range append(append([]string{}, benign...), secrets...) {
		frags, err := d.DetectScope(context.Background(), text, ScopeInputDeclarations)
		if err != nil {
			t.Fatal(err)
		}
		safe, err := guardVerdict(t, d, text)
		if err != nil {
			t.Fatal(err)
		}
		if safe != (len(frags) == 0) {
			t.Fatalf("guard/dispatch disagree on %q: guard safe=%v, input face fragments=%d", text, safe, len(frags))
		}
	}
}

// TestHistoryGuardPrefilterPropagatesReadErrors —— 预筛要把源读完才能断言「没有可能的
// 词项」，读错误如实返回（fail-closed）：哪怕正则根本不可能命中。
func TestHistoryGuardPrefilterPropagatesReadErrors(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("archive read failed")
	_, err = d.HistoryGuard(context.Background())(context.Background(), func() io.Reader {
		return io.MultiReader(strings.NewReader("nothing to see here, just plain prose about the weather"), historyFailReader{boom})
	})
	if !errors.Is(err, boom) {
		t.Fatalf("预筛的读错误必须如实返回: %v", err)
	}
}

// TestHistoryGuardVerifyPathPropagatesReadErrors —— 慢路径要把源读完整，读错误必须
// 如实返回（regexp.MatchReader 会把读错误当成 EOF 吞掉——这正是慢路径存在的意义之一）。
func TestHistoryGuardVerifyPathPropagatesReadErrors(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("archive read failed")
	_, err = d.HistoryGuard(context.Background())(context.Background(), func() io.Reader {
		// 快路径在 32KB 的 bufio 里只看到前半段（命中「tokens」）；慢路径读完整源时撞上错误。
		return io.MultiReader(strings.NewReader("the request used 1734 tokens and "), historyFailReader{boom})
	})
	if !errors.Is(err, boom) {
		t.Fatalf("慢路径的读错误必须如实返回: %v", err)
	}
}

// TestHistoryGuardOversizedMatchIsOmittedConservatively —— 复核有内存上限；超过它
// 的消息一旦快路径命中就保守省略（与修复前一致），不报错。
func TestHistoryGuardOversizedMatchIsOmittedConservatively(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	big := "the request used 1734 tokens " + strings.Repeat("and more prose after that ", (historyVerifyMaxBytes/26)+10)
	safe, err := guardVerdict(t, d, big)
	if err != nil || safe {
		t.Fatalf("超过复核上限的消息应保守省略: safe=%v err=%v", safe, err)
	}
	// 对照：同样的内容只要没超上限就放行（证明上面那次省略是因为超限，不是别的）。
	small := "the request used 1734 tokens " + strings.Repeat("and more prose after that ", 400)
	if safe, err := guardVerdict(t, d, small); err != nil || !safe {
		t.Fatalf("未超限时应放行: safe=%v err=%v", safe, err)
	}
}

type countingReader struct {
	r io.Reader
	n *int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += n
	return n, err
}

// TestHistoryGuardWithoutVerifierKeepsRegexOnlyBehavior —— 直接用 compileHistoryPolicy
// 构造的策略没有复核器：行为与修复前相同（正则命中即省略 / block 组命中即拒绝）。
func TestHistoryGuardWithoutVerifierKeepsRegexOnlyBehavior(t *testing.T) {
	re, _ := historyPatternSuperset(`\btokens?\b`)
	compiled := compileHistoryPolicy([]historyPolicyRule{{Pattern: re, Block: false}})
	if safe, err := compiled.guard(context.Background(), func() io.Reader { return strings.NewReader("1734 tokens") }); safe || err != nil {
		t.Fatalf("无复核器时应保持「命中即省略」: safe=%v err=%v", safe, err)
	}
	re2, _ := historyPatternSuperset(`SYNTHETIC[0-9]+`)
	compiled2 := compileHistoryPolicy([]historyPolicyRule{{Pattern: re2, Block: true}})
	if safe, err := compiled2.guard(context.Background(), func() io.Reader { return strings.NewReader("SYNTHETIC7") }); safe || !errors.Is(err, ErrRuleBlocked) {
		t.Fatalf("无复核器时 block 组命中应拒绝: safe=%v err=%v", safe, err)
	}
	if safe, err := compiled2.guard(context.Background(), func() io.Reader { return strings.NewReader("nothing") }); !safe || err != nil {
		t.Fatalf("无复核器时未命中应放行: safe=%v err=%v", safe, err)
	}
}

// TestHistoryGuardFieldlessBlockRuleStillBlocks —— 不带字段选择器的 block 规则走的是
// 复核这条路：复核确认成立后必须仍然以 ErrRuleBlocked 拒绝（而不是降级成省略）；
// 快路径被别的规则触发、但复核没有命中这条 block 规则时，不得拒绝。
func TestHistoryGuardFieldlessBlockRuleStillBlocks(t *testing.T) {
	d, m := managedFixture(t)
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{
		{ID: "blocker", Name: "Blocker", Type: "secret", Kind: "pattern", Pattern: `SYNTHETICBLOCK[0-9]+`, Scopes: []string{"input"}, Action: "block", Enabled: true, TenantID: "a"},
		{ID: "masker", Name: "Masker", Type: "secret", Kind: "pattern", Pattern: `SYNTHETICMASK[0-9]+`, Scopes: []string{"input"}, Action: "mask", Enabled: true, TenantID: "a"},
	}}
	if _, err := m.Publish(context.Background(), 1, cfg, "test", "fieldless block"); err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	guard := d.HistoryGuard(ctx)
	open := func(text string) func() io.Reader { return func() io.Reader { return strings.NewReader(text) } }
	if safe, err := guard(ctx, open(`the value is SYNTHETICBLOCK7 here`)); safe || !errors.Is(err, ErrRuleBlocked) {
		t.Fatalf("不带字段的 block 规则经复核成立后必须拒绝: safe=%v err=%v", safe, err)
	}
	if safe, err := guard(ctx, open(`the value is SYNTHETICMASK7 here`)); safe || err != nil {
		t.Fatalf("不带字段的 mask 规则经复核成立后应省略、不报错: safe=%v err=%v", safe, err)
	}
	// 快路径会被基线里的密钥形状规则触发（「tokens」），但复核后 block 规则并不成立。
	if safe, err := guard(ctx, open(`The request used 1734 tokens.`)); !safe || err != nil {
		t.Fatalf("复核不成立时不得拒绝: safe=%v err=%v", safe, err)
	}
}
