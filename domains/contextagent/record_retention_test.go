// record_retention_test.go —— 可还原记录的**保留策略**门禁
// （★ 2026-11-11 审计 A-9 的钉测）
//
// ★ A-9 的原形态：★ 每请求写一个 JSON 文件（★ 含三份 base64 正文）
// ★ **无轮转 / 无清理 / 无 TTL** ⇒ ★★ 磁盘占用无上界 ★★。
//
// ★ 本文件钉住修后的**五个可观测行为**：
//
//	① 数量上界：★ 写超过 MaxFiles 个 ⇒ 目录里的文件数被压回上界。
//	② TTL 淘汰：★ 超过 TTL 的记录被删。
//	③ 刚写的文件**绝不被删**（★ 否则「刚存完就查不到」）。
//	④ 清扫**限频**：★ 不是每次 Save 都 O(n) 扫描（★ 摊还成本 O(1)）。
//	⑤ 启动清扫：★ 构造时清掉上一次进程遗留的超量文件。
//
// ★ 另有一条「默认即有界」的契约断言（★ 构造器不得靠零值兜底）。

package contextagent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// retentionTestPolicy造一个「立刻清扫」的测试策略（★ 便于确定性断言）。
func retentionTestPolicy(maxFiles int, ttl time.Duration) FileRetentionPolicy {
	return FileRetentionPolicy{
		MaxFiles:      maxFiles,
		TTL:           ttl,
		SweepInterval: 0, // ★ 关闭时间触发 ★ ⇒ 只靠批量触发（★ 确定性）。
		SweepBatch:    1, // ★ 每次 Save 都触发一次清扫。
	}
}

func jsonFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// TestFileRecorder_DefaultPolicyIsBounded 断言**默认策略就是有界的**。
//
// ★ 为什么重要：★ 保留策略靠零值兜底 ⇒ ★ 「忘了配」就退化成无界 ★★
// ⇒ ★★ 所以默认策略必须**自带**上界（★ 构造器即带界）★★。
func TestFileRecorder_DefaultPolicyIsBounded(t *testing.T) {
	p := DefaultFileRetentionPolicy()
	if p.MaxFiles <= 0 {
		t.Fatalf("默认 MaxFiles=%d ⇒ ★ 构造出来就是无界的", p.MaxFiles)
	}
	if p.TTL <= 0 {
		t.Fatalf("默认 TTL=%v ⇒ ★ 构造出来就是永不过期的", p.TTL)
	}
	if p.SweepInterval <= 0 || p.SweepBatch <= 0 {
		t.Fatalf("默认清扫触发器无效：interval=%v batch=%d", p.SweepInterval, p.SweepBatch)
	}
}

// TestFileRecorder_RetentionBoundsFileCount 钉住**数量上界**。
//
// ★ 变异臂：★ 把 sweepLocked 里的超量淘汰分支去掉 ⇒ ★ 本用例红。
func TestFileRecorder_RetentionBoundsFileCount(t *testing.T) {
	dir := t.TempDir()
	const maxFiles = 8
	rec, err := NewFileRecorderWithPolicy(dir, retentionTestPolicy(maxFiles, 0))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 20; i++ {
		id := "req-" + string(rune('a'+i))
		if err := rec.Save(context.Background(), &Record{RequestID: id}); err != nil {
			t.Fatal(err)
		}
		// ★ 把 mtime 钉成确定值（★ 淘汰顺序才可断言）。
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, id+".json"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	got := jsonFilesIn(t, dir)
	if len(got) > maxFiles {
		t.Fatalf("目录里有 %d 个记录文件，超过上界 %d ⇒ ★ 保留策略没生效", len(got), maxFiles)
	}
	// ★ 被保留的应当是**最近写的**那些（★ 最旧的被淘汰）。
	want := map[string]bool{}
	for i := 20 - maxFiles; i < 20; i++ {
		want["req-"+string(rune('a'+i))+".json"] = true
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("保留了不该保留的 %q（★ 淘汰应当按 mtime 从最旧开始）", name)
		}
	}
}

// TestFileRecorder_RetentionRemovesExpiredByTTL 钉住 **TTL 淘汰**。
//
// ★ 变异臂：★ 把 TTL淘汰分支去掉 ⇒ ★ 本用例红。
func TestFileRecorder_RetentionRemovesExpiredByTTL(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewFileRecorderWithPolicy(dir, retentionTestPolicy(0, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(context.Background(), &Record{RequestID: "old"}); err != nil {
		t.Fatal(err)
	}
	// ★ 把 old 的 mtime 推到 2 小时前（★ 超过 1h 的 TTL）。
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old.json"), stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(context.Background(), &Record{RequestID: "new"}); err != nil {
		t.Fatal(err)
	}
	got := jsonFilesIn(t, dir)
	if len(got) != 1 || got[0] != "new.json" {
		t.Fatalf("TTL 淘汰后目录 = %v，期望只剩 [new.json]", got)
	}
}

// TestFileRecorder_SweepNeverRemovesJustWrittenRecord 钉住「**刚写的文件
// 绝不被删**」。
//
// ★ 为什么关键：★ 若刚写的文件被淘汰 ⇒ ★ 「刚存完就查不到」⇒ ★★
// ★★ 那是最难查的一类故障（★ 写成功、日志成功、读却404）★★。
//
// ★ 本用例**直接**调 sweepLocked（★ 包内测试）★★ 并把 keepPath 指向
// ★ **mtime 最旧**的那个文件 ⇒ ★ 专门检验 keepPath 的保护是否真的生效。
func TestFileRecorder_SweepNeverRemovesJustWrittenRecord(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewFileRecorderWithPolicy(dir, retentionTestPolicy(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	// ★ 造两个文件：★ keep 是 mtime **最旧**的。
	keep := filepath.Join(dir, "keep.json")
	other := filepath.Join(dir, "other.json")
	for _, p := range []string{keep, other} {
		if err := os.WriteFile(p, []byte("{}"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	newer := time.Now()
	if err := os.Chtimes(keep, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, newer, newer); err != nil {
		t.Fatal(err)
	}

	rec.sweepLocked(keep, time.Now())

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("★ 刚写入的文件被删了（keepPath 保护失效）：%v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatalf("超量的那个（%q）应当被淘汰，却还在 ⇒ ★ 淘汰分支没生效", other)
	}
}

// TestFileRecorder_SweepIsRateLimited 钉住「清扫**限频**」。
//
// ★ 为什么关键：★ Save 在**请求路径**上 ★ ⇒ ★★ 若每次 Save 都 readdir +
// ★★ stat 全部文件 ⇒ ★★ O(n) × 每请求 ⇒ ★★ 高 QPS 下自己变成瓶颈 ★★。
//
// ★ 本用例断言：★ 在「批量阈值未到」且「时间间隔未到」时，★ 清扫次数不增长。
func TestFileRecorder_SweepIsRateLimited(t *testing.T) {
	dir := t.TempDir()
	policy := FileRetentionPolicy{
		MaxFiles:      1000,
		TTL:           0,
		SweepInterval: time.Hour, // ★ 时间触发：1 小时内不触发。
		SweepBatch:    100, // ★ 批量触发：写满 100 个才触发。
	}
	rec, err := NewFileRecorderWithPolicy(dir, policy)
	if err != nil {
		t.Fatal(err)
	}
	before := rec.sweeps
	for i := 0; i < 50; i++ {
		if err := rec.Save(context.Background(), &Record{RequestID: "req-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.sweeps - before; got != 0 {
		t.Fatalf("写了 50 个文件却清扫了 %d 次（★ 阈值是 100 个 / 1 小时）⇒ ★ 清扫没有限频 ⇒ ★★ Save 路径上会退化成 O(n) 每请求 ★★", got)
	}
	// ★ 再写满批量 ⇒ 必须清扫一次（★ 证明限频不是「永远不清扫」）。
	for i := 50; i < 100; i++ {
		if err := rec.Save(context.Background(), &Record{RequestID: "req-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.sweeps - before; got != 1 {
		t.Fatalf("写满 100 个文件后清扫了 %d 次，期望恰好 1 次", got)
	}
}

// TestFileRecorder_StartupSweepCleansLeftovers 钉住「**启动清扫**」。
//
// ★ 为什么关键：★ 上一进程留下的超量文件不会因为本进程启动而消失
// ★ ★⇒ ★★ 「重启后目录仍然是满的」这个形态 ★★ ⇒ ★★ 构造时必须扫一次。
func TestFileRecorder_StartupSweepCleansLeftovers(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(dir, "old-"+string(rune('a'+i))+".json"), []byte("{}"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := NewFileRecorderWithPolicy(dir, retentionTestPolicy(8, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := jsonFilesIn(t, dir)
	if len(got) > 8 {
		t.Fatalf("构造后目录里有 %d 个文件，超过上界 8 ⇒ ★ 启动清扫没生效", len(got))
	}
	// ★ 清扫后仍能正常写入与读回（★ 启动清扫不能把功能弄坏）。
	if err := rec.Save(context.Background(), &Record{RequestID: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Load(context.Background(), "fresh"); err != nil {
		t.Fatal(err)
	}
}
