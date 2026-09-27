// internal_error_guard_test.go — R72 审计轮 L4 守卫。
//
// R71 收口 session detail/list v2 两个端点时发现 admin 包 500 响应直接
// 回显 err.Error()（pgx/驱动内部错误串原样到达客户端）是包级存量债；
// 本轮以 writeInternalErr / writeInternalErrStr / writeInternalTextErr
// 批量收口（internal_error.go）。本守卫静态扫描包内源码，禁止任何 500
// 响应行重新引入 *.Error() 回显 —— R71 实证这类债可以静默红两轮，钉住
// 才不会回潮。
//
// 机制与 internal/sqlreadguard 的 LEGIT 白名单同款：确需在 500 行内使用
// 错误文本的位点，在 legitimateExceptions 里登记 file 名 + 理由；当前
// 刻意留空 —— 收口后应为零。
package admin

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// legitimateExceptions maps file names (no path) to a reason. Entries here
// are reviewed debt, not license: each needs a concrete justification for
// shipping error text on a 500.
var legitimateExceptions = map[string]string{}

func TestNoInternalErrorEchoIn500Responses(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	echoRe := regexp.MustCompile(`\.Error\(\)`)
	var leaks []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, ok := legitimateExceptions[name]; ok {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "StatusInternalServerError") && echoRe.MatchString(line) {
				leaks = append(leaks, name+":"+strconv.Itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
	}
	if len(leaks) > 0 {
		t.Errorf("admin package must not echo err.Error() into 500 responses — use writeInternalErr/writeInternalErrStr/writeInternalTextErr (internal_error.go); %d leak(s):\n%s",
			len(leaks), strings.Join(leaks, "\n"))
	}
}
