package outputcompliance

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

// r1047 门：拦截证据摘录「以命中点为心」（rv-1735 复盘——头部 4KiB 摘录在
// 命中位于长 lane 尾部时，审核员与影子判官都看不到被拦内容）。
func TestBoundedPolicyEvidenceAroundCentersOnHit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 600; i++ { // 6000 字节 > 4 KiB 上限
		b.WriteString("0123456789")
	}
	text := b.String()
	const headMark, tailMark = "HEADMARK_", "TAILMARK_"
	text = headMark + text[9:]
	text = text[:len(text)-len(tailMark)] + tailMark // 头尾各埋一个标记

	issues := []outputcompliance.ComplianceIssue{{Location: "char:5500-5510"}}
	got := boundedPolicyEvidenceAround(text, issues)
	if len(got) > maxPolicyEvidenceBytes+len(tailMark) {
		t.Fatalf("window exceeds cap: %d", len(got))
	}
	if !strings.Contains(got, tailMark) {
		t.Fatal("尾部命中不在摘录里（盲区未修）")
	}
	if strings.Contains(got, headMark) {
		t.Fatal("窗口没有以命中为心：头部标记不该在场")
	}

	// 无 char 位置（工具参数是 JSON 路径）⇒ 回退头部摘录（既有行为）。
	got = boundedPolicyEvidenceAround(text, []outputcompliance.ComplianceIssue{{Location: "$.command"}})
	if !strings.Contains(got, headMark) {
		t.Fatal("无位置信息时应回退头部摘录")
	}
	// 位置非法 ⇒ 回退。
	got = boundedPolicyEvidenceAround(text, []outputcompliance.ComplianceIssue{{Location: "char:abc"}})
	if !strings.Contains(got, headMark) {
		t.Fatal("非法位置应回退头部摘录")
	}
	// 短文本整段返回。
	got = boundedPolicyEvidenceAround("short", issues)
	if got != "short" {
		t.Fatalf("short text altered: %q", got)
	}
}
