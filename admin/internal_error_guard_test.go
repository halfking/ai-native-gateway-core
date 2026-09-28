// internal_error_guard_test.go — R72 审计轮 L4 守卫，R73 审计轮 v2 升级。
//
// R71 收口 session detail/list v2 两个端点时发现 admin 包 500 响应直接
// 回显 err.Error()（pgx/驱动内部错误串原样到达客户端）是包级存量债；
// 本轮以 writeInternalErr / writeInternalErrStr / writeInternalTextErr
// 批量收口（internal_error.go）。本守卫静态扫描包内源码，禁止任何 500
// 响应行重新引入 *.Error() 回显 —— R71 实证这类债可以静默红两轮，钉住
// 才不会回潮。
//
// v2（R73）：v1 守卫有三个结构性漏网，均已实证过在逃位点——
//   1. 只认 `.Error()` 形态：`fmt.Sprintf("...%v", err)` 拼进 500 响应体
//      的写法不含 `.Error()` 调用（annotation/session_management 等
//      22 处在逃）；
//   2. 要求状态位与回显同行：多行 `writeJSON(w, 500, map{..., "error":
//      err.Error()})` 每行只命中一半（tool_registry/tool_policy 等
//      10 处在逃）；
//   3. os.ReadDir(".") 不递归：admin 子目录整体漏扫（logsearch 1 处
//      在逃；dashboardapi 靠 writer 层兜底，见白名单）。
// 现改为 filepath.WalkDir 递归 + 状态行 ±3 行窗口 + `%v`+err 启发式。
//
// 机制与 internal/sqlreadguard 的 LEGIT 白名单同款：确需在 500 邻域内
// 使用错误文本的位点，在 legitimateExceptions 里登记 file 名 + 理由。
package admin

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// legitimateExceptions maps file names (no path) to a reason. Entries here
// are reviewed debt, not license: each needs a concrete justification for
// shipping error text on a 500.
// key 必须是相对 admin/ 的斜杠路径（WalkDir 的 path 形态），不能是裸文件名：
// 十七轮审计发现 basename 匹配会让 admin 顶层的同名文件（如既存的
// admin/session_health.go）被 dashboardapi 条目连带豁免，静默制造扫描盲区。
var legitimateExceptions = map[string]string{
	// dashboardapi 的 writeErrorJSON（types.go）对 status>=500 一律剥离
	// details、真实错误落服务端 slog——调用点传 err.Error() 是给服务端
	// 日志用的，wire 上已被 writer 兜底。这是既定架构而非漏网。
	"dashboardapi/session_overview.go": "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
	"dashboardapi/session_health.go":   "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
	"dashboardapi/session_trend.go":    "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
	"dashboardapi/module_stats.go":     "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
	"dashboardapi/session_active.go":   "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
	"dashboardapi/performance.go":      "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
	"dashboardapi/errors.go":           "dashboardapi writeErrorJSON strips details on 5xx (types.go backstop)",
}

func TestNoInternalErrorEchoIn500Responses(t *testing.T) {
	echoRe := regexp.MustCompile(`\.Error\(\)`)
	// `%v`/`%s` 与 err 变量共现（v1 只认 .Error()，fmt 形态全部漏网）。
	fmtErrRe := regexp.MustCompile(`%[vs]`)
	errVarRe := regexp.MustCompile(`\berr(or)?\b`)
	// 自身状态是 4xx 的行：哨兵/校验文案按设计给用户看（M-7 政策），
	// 只是恰好落在别处 500 状态行的窗口内才被扫到。
	non5xxStatusRe := regexp.MustCompile(`Status(BadRequest|Forbidden|NotFound|Conflict|Unauthorized|MethodNotAllowed|TooManyRequests|Teapot)\b`)
	// strings.Contains(err.Error(), …) 是错误分类逻辑，不是 wire 写出。
	logicRe := regexp.MustCompile(`strings\.Contains\(`)
	// 多行 slog 调用的续行：`"error", err.Error())`（逗号键值，响应 map
	// 用冒号）。
	slogContRe := regexp.MustCompile(`"(err|error)",\s*[A-Za-z_]+\.Error\(\)`)
	// 十六轮审计 E5：R72 守卫只匹配 StatusInternalServerError，literal
	// `500` 全部漏网。状态位识别同时覆盖两种写法。
	statusRe := regexp.MustCompile(`StatusInternalServerError|\b500\b`)
	// 十七轮审计：`\b500\b` 会把 `500*time.Millisecond` 这类时长字面量
	// 当成状态行锚点（live_stream_sse.go 实锤——200 诊断载荷被迫脱敏）。
	// 锚点行命中 duration 形态（`500 * time.X` / `time.X * 500`）时整行
	// 不作为状态行参与窗口扫描。
	durationAnchorRe := regexp.MustCompile(`\b500\s*\*\s*time\.|time\.\w+\s*\*\s*500\b`)

	var leaks []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if _, ok := legitimateExceptions[path]; ok {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		isComment := func(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "//") }
		for i, line := range lines {
			if !statusRe.MatchString(line) || durationAnchorRe.MatchString(line) {
				continue
			}
			lo, hi := max(0, i-3), min(len(lines)-1, i+3)
			for j := lo; j <= hi; j++ {
				l := lines[j]
				if isComment(l) || strings.Contains(l, "slog.") || strings.Contains(l, "writeInternalErr") ||
					non5xxStatusRe.MatchString(l) || logicRe.MatchString(l) || slogContRe.MatchString(l) {
					// 日志行不是 wire；writeInternalErr* 三态 helper
					// 本身按构造安全（真实错误只进 slog）；其余三类
					// 见各正则处注释。
					continue
				}
				if echoRe.MatchString(l) || (fmtErrRe.MatchString(l) && errVarRe.MatchString(l)) {
					leaks = append(leaks, path+":"+strconv.Itoa(j+1)+" "+strings.TrimSpace(l))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leaks) > 0 {
		t.Errorf("admin package must not echo err.Error() into 500 responses — use writeInternalErr/writeInternalErrStr/writeInternalTextErr (internal_error.go); %d leak(s):\n%s",
			len(leaks), strings.Join(leaks, "\n"))
	}
}
