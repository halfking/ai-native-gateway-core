package admin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeSessionTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`"部署故障排查"`, "部署故障排查"},
		{"标题：修复 Casdoor 登录\n多余行", "修复 Casdoor 登录"},
		{"短标题", "短标题"},
	}
	for _, tc := range cases {
		got := normalizeSessionTitle(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeSessionTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := normalizeSessionTitle("这是一个非常非常非常非常长的会话标题需要截断")
	if len([]rune(long)) > sessionTitleMaxRunes+1 {
		t.Fatalf("expected truncation, got %q (%d runes)", long, len([]rune(long)))
	}
}

func TestIsValidSessionTitle(t *testing.T) {
	if isValidSessionTitle("a") {
		t.Fatal("single char title should be invalid")
	}
	if isValidSessionTitle("无法生成标题") {
		t.Fatal("template rejection should be invalid")
	}
	if !isValidSessionTitle("部署鉴权修复") {
		t.Fatal("expected valid title")
	}
	if isValidSessionTitle("<think>") {
		t.Fatal("XML/thinking tag title should be invalid")
	}
	if isValidSessionTitle("redacted_thinking") {
		t.Fatal("thinking marker title should be invalid")
	}
}

func TestNormalizeSessionTitle_StripsThinkingTags(t *testing.T) {
	got := normalizeSessionTitle("<think>部署故障排查</think>")
	if got != "部署故障排查" {
		t.Fatalf("normalizeSessionTitle = %q, want 部署故障排查", got)
	}
}

func TestRequestLogStatusExprRequiresRLAlias(t *testing.T) {
	if !strings.Contains(requestLogStatusExpr, "rl.") {
		t.Fatal("requestLogStatusExpr must reference _request_logs alias rl")
	}
	// loadTaskLogsForTitle / loadSessionLogsForSummary must use FROM _request_logs rl.
	const fromClause = "FROM _request_logs rl"
	q := `
		SELECT rl.ts, ` + requestLogStatusExpr + ` AS request_status
		` + fromClause + `
		WHERE gw_task_id = $1`
	if !strings.Contains(q, fromClause) {
		t.Fatalf("title/summary log queries must include %q", fromClause)
	}
}

func TestSessionTitleMapKey(t *testing.T) {
	k := sessionTitleMapKey("default", "sess-1")
	if k != "default\x00sess-1" {
		t.Fatalf("unexpected key: %q", k)
	}
	if sessionTitleMapKey("default", "") != "default\x00" {
		t.Fatalf("empty scoped session should still key")
	}
}

// TestResolveSessionTitleTaskIDBindsTenantAsDollar2 钉住 2026-10-14 生产
// `unused argument: 1`：SessionFamilyTurnsForSessionSQL 只消费 $1，调用方
// 必须显式挂 `t.tenant_id = $2` 才能传 tenantID，否则标题 scope 同步永久失败。
// 前提在场：源码里 resolve 函数体同时含助手调用与 $2 谓词（count==1）。
func TestResolveSessionTitleTaskIDBindsTenantAsDollar2(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "session_title.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := extractGoFuncBody(string(src), "resolveSessionTitleTaskID")
	if body == "" {
		t.Fatal("未能抽出 resolveSessionTitleTaskID 函数体")
	}
	if !strings.Contains(body, "SessionFamilyTurnsForSessionSQL()") {
		t.Fatal("resolve 必须走 SessionFamilyTurnsForSessionSQL（读源契约）")
	}
	if n := strings.Count(body, "t.tenant_id = $2"); n != 1 {
		t.Fatalf("tenant 谓词 count=%d，期望 1——漏挂 $2 会触发 pgx unused argument: 1", n)
	}
	// 负控制：去掉 $2 谓词后 count 必须变 0，证明判据看见的是真字符串。
	mutated := strings.Replace(body, "t.tenant_id = $2", "/* removed */", 1)
	if n := strings.Count(mutated, "t.tenant_id = $2"); n != 0 {
		t.Fatal("变异负控制失败：替换后仍能匹配到 $2")
	}
	// 参数表仍传 sessionID, tenantID 两个（与 $1/$2 对齐）。
	if !strings.Contains(body, "sessionID, tenantID") {
		t.Fatal("QueryRow 必须同时绑定 sessionID 与 tenantID")
	}
}

// extractGoFuncBody 是本文件私有的轻量抽取：从 `func (recv) name(` 起
// 数花括号到闭合。与 goFuncBodyStrict 同形，避免再引入跨包 helper 重名。
func extractGoFuncBody(src, name string) string {
	needle := "func (h *Handler) " + name + "("
	i := strings.Index(src, needle)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	brace := strings.IndexByte(rest, '{')
	if brace < 0 {
		return ""
	}
	depth := 0
	for j := brace; j < len(rest); j++ {
		switch rest[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[brace : j+1]
			}
		}
	}
	return ""
}
