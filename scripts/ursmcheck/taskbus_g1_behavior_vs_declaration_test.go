package ursm_test

// taskbus_g1_behavior_vs_declaration_test.go — G1 行为对拍声明（V3/V4）。
//
// 判据：只扫 func (…) Execute( 方法体（深度计数到匹配闭合）。
//   V3: 声明 DispatchTaskBus 的文件，Execute 内禁止 go func
//   V4: 默认/Inline 的 Execute 内禁止 go func
// 豁免簿（本波）：A4 memora 未挂载不迁；其余为切 TaskBus 前的存量，附理由。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func taskbusG1Root(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root missing go.mod: %s", root)
	}
	return root
}

// path suffix → reason（相对 domains/hooks/）
var taskbusG1V4Exempt = map[string]string{
	"memoraauto/hook.go": "A4: production-unmounted; migrate only after wire-up",
	"sessionaudit/hook.go": "batch2 candidate; still Inline ad-hoc async",
	"security/hook.go": "pre-existing Inline async; not this wave",
	"compression/strategy/runner_parallel.go": "strategy helper, not a pipeline Hook Execute contract site",
}

var (
	reDispatchTaskBus = regexp.MustCompile(`DispatchTaskBus`)
	reGoFunc          = regexp.MustCompile(`\bgo\s+func\b`)
	reExecuteSig      = regexp.MustCompile(`func\s+\([^)]*\)\s+Execute\s*\(`)
)

func executeBodies(src string) []string {
	var out []string
	for _, loc := range reExecuteSig.FindAllStringIndex(src, -1) {
		i := loc[1]
		// find opening brace
		for i < len(src) && src[i] != '{' {
			i++
		}
		if i >= len(src) {
			continue
		}
		depth := 0
		start := i
		for j := i; j < len(src); j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					out = append(out, src[start:j+1])
					i = j
					goto next
				}
			}
		}
	next:
	}
	return out
}

func TestTaskBusG1_BehaviorMatchesDeclaration(t *testing.T) {
	root := taskbusG1Root(t)
	hooksDir := filepath.Join(root, "domains", "hooks")
	var violations []string
	_ = filepath.Walk(hooksDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		src := string(b)
		bodies := executeBodies(src)
		if len(bodies) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(root, "domains", "hooks"), path)
		rel = filepath.ToSlash(rel)
		isTaskBus := reDispatchTaskBus.MatchString(src)
		for _, body := range bodies {
			if !reGoFunc.MatchString(body) {
				continue
			}
			if isTaskBus {
				violations = append(violations, "V3: TaskBus Execute has go func: "+rel)
				continue
			}
			if reason, ok := taskbusG1V4Exempt[rel]; ok {
				t.Logf("V4 exempt %s: %s", rel, reason)
				continue
			}
			violations = append(violations, "V4: Inline Execute has go func: "+rel)
		}
		return nil
	})
	if len(violations) > 0 {
		t.Fatalf("G1 violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestTaskBusG1_Selftest_CacheSaveTaskBusClean(t *testing.T) {
	root := taskbusG1Root(t)
	path := filepath.Join(root, "domains", "hooks", "cache", "taskbus_save.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reDispatchTaskBus.Match(b) {
		t.Fatal("demo migration must declare DispatchTaskBus")
	}
	for _, body := range executeBodies(string(b)) {
		if reGoFunc.MatchString(body) {
			t.Fatal("V3 selftest: cache taskbus Execute must not go func")
		}
	}
}
