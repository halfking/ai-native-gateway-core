//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// sessionid_raw_header_ratchet_test.go —— 会话身份头直读棘轮（2026-10-31 实施包审计 P1-4 跟进）。
//
// 背景：sessionid_single_source_test.go 只认字面量头表形态，前人审计指出它漏掉
// 「分两次 Header.Get」的写法；实测生产代码仍有 13 个文件直接 Header.Get("X-Gw-Session-Id"
// / "X-Session-Id")，绕过 internal/sessionid.Resolve。它们读的是 handler 已 stamp 的头，
// 功能上一致，但属于"第二真值"的温床。
//
// 判据（棘轮，只降不升）：
//   R1 生产代码（非 _test.go，排除 vendor/.worktrees/.build-local）中，直接读会话头的
//      调用次数，按文件统计，必须 ≤ 基线。新增文件或超出基线 ⇒ 失败。
//   R2 基线中列出的文件若实际次数 < 基线 ⇒ 也失败（要求同步下调基线，棘轮只降不升）。
//   R3 豁免文件（task_id.go：TaskID 而非会话身份）不计入。
//
// 修复方向：新代码一律经 sessionid.FromHeaders / Resolve；存量收拢时同步下调本基线。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rawSessionHeaderBaseline 是当前已知的直读基线（2026-10-31 实测）。
var rawSessionHeaderBaseline = map[string]int{
	"domains/streaming/stream.go":                       4,
	"domains/streaming/request_context.go":              4,
	"domains/streaming/handler.go":                      4,
	"domains/streaming/auto_route_nonchat.go":           4,
	"domains/streaming/session_assignment.go":           3,
	"domains/streaming/auto_route.go":                   2,
	"domains/streaming/executors/executor_ollama.go":    2,
	"domains/streaming/executors/executor_gemini.go":    2,
	"domains/streaming/executors/executor_chat.go":      2,
	"domains/streaming/executors/executor_anthropic.go": 2,
	"domains/streaming/executors/audit_context.go":      2,
	"domains/streaming/executors/context_summarize.go":  1,
}

// rawSessionHeaderExempt 不计入棘轮的文件，每条须有理由。
var rawSessionHeaderExempt = map[string]string{
	"domains/memory/task_id.go": "TaskID 派生，不是会话身份（执行日志 01 · A7 禁改）",
}

var rawSessionHeaderRe = regexp.MustCompile(`Header\.Get\(\s*"X-(Gw-)?Session-Id"\s*\)|Header\["X-(Gw-)?Session-Id"\]`)

func TestSessionIDRawHeaderRatchet(t *testing.T) {
	root := sessionidRepoRoot(t)
	actual := map[string]int{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch filepath.Base(path) {
			case "vendor", ".git", "node_modules", ".worktrees", ".build-local":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/sessionid/") {
			return nil
		}
		if _, ok := rawSessionHeaderExempt[rel]; ok {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", rel, rerr)
		}
		if n := len(rawSessionHeaderRe.FindAllIndex(b, -1)); n > 0 {
			actual[rel] = n
		}
		return nil
	})

	for file, n := range actual {
		base, ok := rawSessionHeaderBaseline[file]
		if !ok {
			t.Errorf("R1 新增会话头直读：%s（%d 处）。请改经 internal/sessionid，或登记为豁免并写明理由", file, n)
			continue
		}
		if n > base {
			t.Errorf("R1 会话头直读超出基线：%s 实际 %d > 基线 %d", file, n, base)
		}
		if n < base {
			t.Errorf("R2 基线可下调：%s 实际 %d < 基线 %d —— 请把 rawSessionHeaderBaseline 同步降低（棘轮只降不升）", file, n, base)
		}
	}
	for file, base := range rawSessionHeaderBaseline {
		if _, ok := actual[file]; !ok {
			t.Errorf("R2 基线文件已无直读：%s（基线 %d）—— 请从 rawSessionHeaderBaseline 移除", file, base)
		}
	}
}

// 自检：正则必须同时命中两种写法，否则门失明。
func TestSessionIDRawHeaderRegexCoversBothForms(t *testing.T) {
	for _, s := range []string{
		`x := r.Header.Get("X-Gw-Session-Id")`,
		`if v := r.Header.Get( "X-Session-Id" ); v != ""`,
		`h := r.Header["X-Session-Id"]`,
	} {
		if !rawSessionHeaderRe.MatchString(s) {
			t.Fatalf("正则未命中：%s", s)
		}
	}
	if rawSessionHeaderRe.MatchString(`r.Header.Get("X-Request-Id")`) {
		t.Fatal("正则误命中无关头")
	}
}
