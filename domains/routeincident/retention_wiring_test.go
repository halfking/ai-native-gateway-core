package routeincident

// §143.58：route_incident_events 的行级留存清理机制【已完整实现、但从未接线】。
//
// ## 背景：这不是「缺实现」，而是「实现是死代码」
//
// `domains/routeincident/retention.go` 里的 `EventRetentionWorker` 是一套
// **完整且正确** 的行级 TTL 清理：
//
//   - 分批删除（`DELETE ... WHERE id IN (SELECT id ... WHERE created_at < $1 LIMIT $2)`）
//     ⇒ 不会一把锁住整张表；
//   - 前置索引检查（`hasTimeIndex`：必须存在「以 `created_at` 为首列」的索引），
//     没有就 `SkippedNoIndex` 明确跳过而不是硬删
//     ⇒ 设计注释里写着「生产实测 1,699,257 行冷缓存 25.7 s vs statement_timeout 30 s」；
//   - 跨实例互斥（advisory lock，被对端持有时让位而不是双删）；
//   - **无操作也打日志**（`EventRetentionAlwaysLogsIncludingNoop`）⇒ 不沉默。
//
// 且它的**行为面已被 5 条真库集成用例覆盖**
// （`retention_integration_test.go`：`SkipsWithoutTimeIndex` /
// `DeletesOnlyExpiredInBatches` / `YieldsToPeer` / `AlwaysLogsIncludingNoop`，
// 外加 `retention_test.go` 的 3 条单测）。
//
// ★★★ **但 `NewEventRetentionWorker` 在全仓没有任何生产调用方** ——
// 只有它自己的定义处与测试。于是：
//
//   - 252 共享库 `public.route_incident_events` = **1,156 MB / 1,831,995 行**，
//     **最早一行 2026-07-13**（§143.58 实测），>30 天的行 579,639、>90 天 14,945；
//   - 且 `idx_route_incident_events_created_at`（39 MB）**已经在库上**，
//     `hasTimeIndex` 的前提**已满足** ⇒ **唯一的缺口就是「没有调用点」**。
//
// ⇒ 这与 `domains/attachments/cleanup_wiring_test.go` 记录的同款缺陷同型：
//   「**有一个正确的删除实现、它是死代码**」。
//
// ## 定级与处置
//
// P1（存储侧：1,156 MB 单调增长，且**没有任何自动回收路径**）。
// 但「是否接线、留多久」属**数据保留策略**决策
// （事故事件删不删、删多久、要不要连带删 `route_incidents` 父表）
// ⇒ 需产品/owner 裁决，本轮**不擅自接线**。
//
// 按「红门不能进主干」的纪律：**把现状钉住并标注 FIXME** ——
// 将来无论是接线（改断言 + 加 TTL 规格 + 接线）还是彻底废弃这个 worker，
// 都必须**显式发生**。
//
// 同时钉一条**结构不变量**（今天成立、接线后仍应成立）：
// **行为门必须存在** —— 「接线」这个动作的前提是「它删得对」已经有真库证据；
// 若哪天行为门被删掉，接线就变成盲接。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot 向上找 go.mod。不从 "." 起步 —— filepath.Dir(".") == "."，
// 循环会第一步就判定到顶。
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

// goFilesUnder 收集 root 下某子树的 .go 文件（排除 _test.go）。
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

// TestEventRetentionWorkerHasNoProductionCallerYet 钉住「清理机制是死代码」这一现状。
//
// 该断言**看起来是反的**（断言「没有调用方」），它是刻意按「红门不能进主干」
// 的纪律写的：未修的缺陷用「断言当前行为 + FIXME」钉住，使将来修改必须显式发生。
//
// ★ 与 `domains/attachments/cleanup_wiring_test.go` 的同型用例同形。
//
// 若产品裁决为「接线」：本用例应改为断言 `NewEventRetentionWorker`
// **至少有一个生产调用方**，且该调用方必须
//
//	① 传入**非零**的 retention（零值会让 worker 变成 no-op，见 retentionEnabled）；
//	② 来自一个**平台设置**而不是硬编码常量（否则 TTL 漂移无痕）；
//
// 并同步订正 `domains/routeincident/retention.go` 的文件头
// （它现在写着「该 worker 只做行级 TTL」而没有说「尚未接线」）。
func TestEventRetentionWorkerHasNoProductionCallerYet(t *testing.T) {
	root := repoRoot(t)
	const defining = "domains/routeincident/retention.go"
	var callers []string
	for _, sub := range []string{"admin", "bg", "cmd", "domains", "internal"} {
		for _, rel := range goFilesUnder(root, sub) {
			if rel == defining {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			if strings.Contains(string(b), "NewEventRetentionWorker") {
				callers = append(callers, rel)
			}
		}
	}
	if len(callers) != 0 {
		t.Fatalf("FIXME(§143.58-1) 已失效：NewEventRetentionWorker 现在有生产调用方 %v。\n"+
			"  请把本用例改为正向断言（接线是本次裁决要达成的目标），\n"+
			"  并确认该调用方的 retention 来自平台设置、且 retention.go 的文件头已订正。", callers)
	}
}

// TestEventRetentionBehaviorGateExists 钉一条**结构不变量**：
// 接线动作的前提是「它删得对」已经有真库证据 ⇒ 行为门必须存在。
//
// ★ 这条与「有没有调用方」无关 ⇒ **今天成立、接线后仍应成立**。
// 若哪天有人把行为门删了，本用例变红 ⇒ 提醒「你正在把一个没有安全网的清理器接上线」。
func TestEventRetentionBehaviorGateExists(t *testing.T) {
	root := repoRoot(t)
	want := []string{
		"TestEventRetentionSkipsWithoutTimeIndex",
		"TestEventRetentionDeletesOnlyExpiredInBatches",
		"TestEventRetentionYieldsToPeer",
		"TestEventRetentionAlwaysLogsIncludingNoop",
	}
	dir := filepath.Join(root, "domains/routeincident")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	blob := strings.Builder{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		blob.Write(b)
	}
	for _, name := range want {
		if !strings.Contains(blob.String(), "func "+name+"(") {
			t.Errorf("domains/routeincident 缺少行为门 %s。\n"+
				"  这条清理路径是【行级 DELETE】，接线上生产之前必须有真库证据证明它只删过期行、\n"+
				"  分批、不与对端重复删；行为门没了，接线就变成盲接。", name)
		}
	}
}
