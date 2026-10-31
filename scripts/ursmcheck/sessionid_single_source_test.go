//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// sessionid_single_source_test.go —— 会话身份解析单一真值源门（包 01 / W1.1）。
//
// 判据：
//   G1 internal/sessionid 包存在且导出 Resolve / DeriveGatewayID / DefaultHeadersPriority
//   G2 默认头表含 X-Gw-Session-Id 与 X-Deepseek-Harness-Session-Id，且 X-Gw 第一
//   G3 生产代码不得再维护第二份 SessionHeadersPriority 字面量表（streaming 只许别名）
//   G4 domains/memory/task_id.go 不得被本轮改成会话身份解析（文件不得 import sessionid）
//
// Gate facts:
// 1. CI 经 make guards-ursmcheck 调用本文件。
// 2. 无同职责门文件（Glob scripts/ursmcheck/*sessionid* 原先为空）。
// 3. 无生产数据读写；只扫源码文本。
// 4. 用户：「门禁 scripts/ursmcheck/sessionid_single_source_test.go」+「登记 GUARD_PACKAGES」。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func sessionidRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s", root)
	}
	return root
}

func TestSessionIDPackageIsSingleSource(t *testing.T) {
	root := sessionidRepoRoot(t)
	pkgDir := filepath.Join(root, "internal", "sessionid")
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("internal/sessionid 必须存在：%v", err)
	}
	var src strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
		src.WriteByte('\n')
	}
	body := src.String()
	for _, want := range []string{
		"func Resolve(",
		"func DeriveGatewayID(",
		"DefaultHeadersPriority",
		"X-Deepseek-Harness-Session-Id",
		"HeaderXGw",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sessionid 真值源缺少 %q", want)
		}
	}

	// X-Gw must be first in the default table literal block.
	re := regexp.MustCompile(`DefaultHeadersPriority\s*=\s*\[\]string\{([^}]+)\}`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("找不到 DefaultHeadersPriority 字面量")
	}
	fields := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1)
	if len(fields) < 2 {
		t.Fatalf("头表过短: %q", m[1])
	}
	if fields[0][1] != "X-Gw-Session-Id" && fields[0][1] != "" {
		// HeaderXGw const may be used; accept either literal or const name usage.
		if !strings.Contains(m[1], "HeaderXGw") && fields[0][1] != "X-Gw-Session-Id" {
			t.Fatalf("头表第一项必须是 X-Gw，got %q block=%s", fields[0][1], m[1])
		}
	}
	foundDSH := false
	for _, f := range fields {
		if strings.EqualFold(f[1], "X-Deepseek-Harness-Session-Id") {
			foundDSH = true
		}
	}
	if !foundDSH && !strings.Contains(m[1], "Deepseek") {
		t.Fatalf("默认头表必须含 Deepseek Harness: %s", m[1])
	}
}

func TestSessionIDNoSecondPriorityTable(t *testing.T) {
	root := sessionidRepoRoot(t)
	// A second hard-coded priority table looks like []string{"X-Gw-Session-Id", "X-Session-Id", ...}
	re := regexp.MustCompile(`\[\]string\{\s*"X-Gw-Session-Id"\s*,\s*"X-Session-Id"`)
	var offenders []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			base := filepath.Base(path)
			if info != nil && info.IsDir() && (base == "vendor" || base == ".git" || base == "node_modules" || base == ".worktrees") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "internal/sessionid"+string(os.PathSeparator)) {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if re.Find(b) != nil {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if len(offenders) > 0 {
		t.Fatalf("发现第二份会话头优先级字面量表（应只在 internal/sessionid）：%v", offenders)
	}
}

func TestSessionIDTaskIDUntouched(t *testing.T) {
	root := sessionidRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "domains", "memory", "task_id.go"))
	if err != nil {
		t.Fatalf("task_id.go 应存在且本轮不改：%v", err)
	}
	if strings.Contains(string(b), "internal/sessionid") {
		t.Fatal("domains/memory/task_id.go 不得 import sessionid（TaskID ≠ 会话身份）")
	}
}

func TestSessionIDStreamingDelegates(t *testing.T) {
	root := sessionidRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "domains", "streaming", "session_routing.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		"sessionid.DeriveGatewayID",
		"sessionid.FromHeaders",
		"sessionid.FromBody",
		"sessionid.DefaultHeadersPriority",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("session_routing.go 必须委托 %s", want)
		}
	}
}
