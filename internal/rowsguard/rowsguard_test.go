package rowsguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exemptions 是显式豁免表：**每一项都必须写明理由**，且 key 精确到
// `file:line`（`for X.Next()` 所在物理行）。豁免表的语义是「这一处
// 不受本守卫检查」，因此它必须可回答「为什么可以不看」。
//
// 注意 key 含行号：改动文件行号漂移会让豁免**失配**（而不是静默放过）
// ——失配时报的是「该行未被豁免且缺守卫」，方向是安全的。真正需要豁免
// 的新站点会因行号变化而重新暴露出来，这是刻意设计。
var exemptions = map[string]string{
	// —— out-of-scope: one-shot developer/ops CLI 的 main 包 ——
	// 这些不是长驻请求/worker 路径，失败即进程退出，截断不会传播给客户端；
	// 其语义需单独裁决，R66 不处理。
	// 4552 -> 4627 是行号漂移，不是新增债：用 git show 8a6142263:cmd/gateway/main.go
	// 核对过，登记那行当时正是 `for rows.Next() {`，同在 func main()，而本文件另一条
	// 循环（当时 6510、现在 6601）从未登记。TestExemptionsStillResolve 会把对不上的
	// 键报成 stale；改键前必须回原提交确认是「同一处漂移」而不是「另找一处顶上」。
	"cmd/gateway/main.go:4627":                                      "one-shot/main package wiring, out of R66 scope (line drifted from 4552)",
	"cmd/gateway/main_helpers.go:369":                               "one-shot/main package wiring, out of R66 scope",
	"cmd/gateway/main_v32_wiring.go:119":                            "one-shot/main package wiring, out of R66 scope",
	"cmd/fetch-standard-iq/main.go:103":                             "one-shot/main package, out of R66 scope",
	"cmd/llm-gw-exporter/main.go:401":                               "one-shot/main package, out of R66 scope",
	"cmd/regen-credentials/main.go:63":                              "one-shot/main package, out of R66 scope",
	"cmd/sessionmeta-bench/sampler.go:192":                          "benchmark main package, out of R66 scope",
	"cmd/tuning-backtest/main.go:205":                               "one-shot/main package, out of R66 scope",
	"cmd/tuning-backtest/main.go:289":                               "one-shot/main package, out of R66 scope",
	"tests/session_audit/cmd/audit-test/main.go:210":                "test harness main package, out of R66 scope",
	"tests/test_popularity_tracker.go:121":                          "test helper (non _test.go), out of R66 scope",
	"scripts/injection-test/test-prompt-injection-detection.go:393": "scripts/ manual injection probe, out of R66 scope",
	"cmd/tools/validate_sessions_v2/loader.go:167":                  "one-shot validator main package, out of R66 scope",
}

// TestEveryRowsLoopIsGuarded 是 R66 的站位级主门。
//
// 判据落在**产物特征**上（每个循环旁是否有 `X.Err()` 终检或跳行留痕），
// 而不是「文件里有没有出现某个 helper 名」——后者在多站点文件里删掉
// 其中一处不会变红，R65 已实证该缺口。
func TestEveryRowsLoopIsGuarded(t *testing.T) {
	root := repoRoot(t)
	checked, violations, err := CheckAll(root, exemptions)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(violations) == 0 {
		t.Logf("rows guard: %d sites checked, 0 violations", checked)
		return
	}
	for _, v := range violations {
		t.Errorf("unguarded rows loop: %s", v)
	}
	t.Errorf("%d/%d rows loops lack a terminal check or skip trace", len(violations), checked)
}

// TestExemptionsStillResolve 防止豁免表腐化：key 里的行号必须仍然指向
// 真实的 `for X.Next()` 循环。行号漂移导致的失配会在这里报出，而不是
// 让一条失效豁免长期假装有效。
func TestExemptionsStillResolve(t *testing.T) {
	root := repoRoot(t)
	sites, err := collectSites(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	live := make(map[string]site, len(sites))
	for _, s := range sites {
		live[fileLine(s.file, s.line)] = s
	}
	for key, reason := range exemptions {
		if reason == "" {
			t.Errorf("exemption %s has no reason; every exemption must be justifiable", key)
			continue
		}
		if _, ok := live[key]; !ok {
			relFile, idx := splitKey(key)
			src, rerr := os.ReadFile(filepath.Join(root, relFile))
			if rerr != nil {
				t.Errorf("exemption %s: file unreadable: %v", key, rerr)
				continue
			}
			lines := strings.Split(string(src), "\n")
			if idx >= 1 && idx <= len(lines) && nextLoopRe.MatchString(lines[idx-1]) {
				t.Errorf("exemption %s: line still holds a Next() loop but is missing from the guard's own key set", key)
				continue
			}
			t.Logf("exemption %s is stale (line drift) — remove it", key)
		}
	}
}

func fileLine(file string, line int) string {
	return file + ":" + itoa(line)
}

// splitKey 把 "path/file.go:123" 拆成 (file, line)。
func splitKey(key string) (string, int) {
	i := strings.LastIndex(key, ":")
	if i < 0 {
		return key, 0
	}
	return key[:i], atoi(key[i+1:])
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
