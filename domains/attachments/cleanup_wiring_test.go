package attachments

// R87-j：附件行删除路径的接线守卫。
//
// ## 背景：59 号 §2.2 的 P1 需要收窄，且收窄后更严重
//
// 59 号写「清理只置 JSONB 为 NULL、**从不删** `request_attachments`」。实测
// （conventions.md §10 三项定点核实）这个说法**过宽**：
//
//   - `domains/attachments/repository.go:294` **确实有** `DELETE FROM public.request_attachments`，
//     且是分块的（2026-09-17 的 ctid+LIMIT chunk，仿 session_summaries_trimmer），
//     **实现本身是正确的**；
//   - **但 `Repository.DeleteOlderThan` 零生产调用方** —— 全仓只有
//     `repository_test.go:103` 调它（且只测 nil repo 的 errNoDB 分支）。
//     而它的文档注释却写着「admin tooling calls this on a schedule to bound table growth」
//     —— **这是一条未兑现的注释承诺**（与 D13 的 `disabled` 注释同型）；
//   - 真正被调用的清理端点是 `POST /api/admin/attachments/cleanup/execute`
//     （`admin/handler.go:1006`，super_admin），它执行的是
//     `UPDATE request_logs_hot`（`admin/data_lifecycle_attachments.go:516`）把 JSONB 置 NULL，
//     **既不调用 `DeleteOlderThan`、也不删附件行**。
//
// ⇒ **真实缺陷比 59 号的表述更精确也更严重：不是「没有删除实现」，而是
// 「有一个正确的删除实现、它是死代码，而唯一在跑的清理路径绕开了它」。**
// 这与 R85 域 A 发现的 `freeresource/pool_dedup.go`（144 行死代码）同型。
//
// ## 定级与处置
//
// P1，但是否接线属**数据保留策略**决策（附件行删不删、删多久、要不要连带删物理文件）
// ⇒ 需产品/owner 裁决，本轮不修。
//
// 按「红门不能进主干」的纪律：**把现状钉住并标注 FIXME** —— 将来无论是接线
// （改断言 + 改注释）还是彻底废弃该方法，都必须**显式发生**。
//
// 同时钉一条**结构不变量**（今天成立、接线后仍应成立）：
// `request_attachments` 的 DELETE 只能出现在 repository.go 一处，不得散落进 admin SQL。
// 这条今天就成立，接线时也不该被破坏。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot 向上找 go.mod。不从 "." 起步 —— filepath.Dir(".") == "."，
// 循环会第一步就判定到顶（R87-g 踩过，已写入 routeguard 的注释）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未找到 go.mod，起点 %s", dir)
		}
		dir = parent
	}
}

// goFilesUnder 收集 root 下某子树的 .go 文件（排除 _test.go 与 vendor）。
func goFilesUnder(root, sub string) []string {
	var out []string
	abs := filepath.Join(root, sub)
	_ = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	return out
}

// TestRequestAttachmentsDeleteIsCentralized 结构不变量：
// `DELETE FROM request_attachments` 只允许出现在 domains/attachments/repository.go。
//
// 语义量是「删除逻辑集中在一处、便于统一分块与保留策略」，不是「文件里出现过这个字符串」。
func TestRequestAttachmentsDeleteIsCentralized(t *testing.T) {
	root := repoRoot(t)
	var hits []string
	for _, sub := range []string{"domains/attachments", "admin", "bg", "cmd/gateway"} {
		for _, rel := range goFilesUnder(root, sub) {
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			lowered := strings.ToLower(string(b))
			if strings.Contains(lowered, "delete from public.request_attachments") ||
				strings.Contains(lowered, "delete from request_attachments") {
				hits = append(hits, rel)
			}
		}
	}
	want := "domains/attachments/repository.go"
	if len(hits) != 1 || hits[0] != want {
		t.Fatalf("request_attachments 的 DELETE 应集中在 %s 一处，实际命中 %v。\n"+
			"  分散的多条删除路径会各自发明分块/保留策略，绕过分块 DELETE 的行锁与 WAL 保护。", want, hits)
	}
}

// FIXME(R87-j-1, 2026-10-01)：钉住「正确的删除实现是死代码」这一现状。
//
// 该断言**看起来是反的**（断言「没有调用方」），它是刻意按「红门不能进主干」
// 的纪律写的：未修的缺陷用「断言当前行为 + FIXME」钉住，使将来修改必须显式发生。
//
// 若产品裁决为「接线」：本用例应改为断言 DeleteOlderThan **至少有一个生产调用方**，
// 且该调用方必须传非零 cutoff；并同步订正 repository.go 的那句注释。
func TestDeleteOlderThanHasNoProductionCallerYet(t *testing.T) {
	root := repoRoot(t)
	var callers []string
	for _, sub := range []string{"admin", "bg", "cmd", "domains", "internal"} {
		for _, rel := range goFilesUnder(root, sub) {
			if rel == "domains/attachments/repository.go" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			if strings.Contains(string(b), "DeleteOlderThan") {
				callers = append(callers, rel)
			}
		}
	}
	if len(callers) != 0 {
		t.Fatalf("FIXME(R87-j-1) 已失效：DeleteOlderThan 现在有生产调用方 %v。\n"+
			"  请把本用例改为正向断言（接线是本次裁决要达成的目标），\n"+
			"  并订正 repository.go 里「admin tooling calls this on a schedule」这句注释。", callers)
	}
}
