package dbdegradation

// R80 轮（2026-10-24）：备份摘要的**「部分结果」语义**三条不变量。
//
// ★ 为什么有这一轮：R79 给端点加了 5s ctx 预算 ⇒ 备份摘要的计算**会被截断**
//
//	（生产实测：5.18GB / 100 个文件，冷路径 ~48s，远超 5s 预算）
//	⇒ 而 R79 的实现把**截断结果当完整结果缓存** ⇒
//	⇒ 端点对外说「100 个备份 / 5.18GB / 记录数 0 / 会话数 0」——
//	★★ **静默的错误数据**（比慢更危险）。
//
// ★ 三条不变量：
//
//	① **计算被截断 ⇒ 必须报错**（不能把部分结果当完整结果交出去）；
//	② **部分结果可以被缓存，但必须带 partial 标记**（否则每次请求重解压 5GB）；
//	③ **后台刷新**能把完整结果补上（请求路径不再付解压代价）。

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// r80WriteBackup 写一个可指定大小的 gzip 备份文件。
func r80WriteBackup(t *testing.T, dir, name string, sessions []string) string {
	t.Helper()
	path := filepath.Join(dir, "backups", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("★ 建文件失败：%v", err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	for _, s := range sessions {
		if _, err := zw.Write([]byte(`{"session_id":"` + s + `","record_type":"x"}` + "\n")); err != nil {
			t.Fatalf("★ 写记录失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("★ 关 gzip 失败：%v", err)
	}
	return path
}

// TestR80TruncatedSummaryMustReportPartial（★ 不变量①）
//
// ★ 计算被 ctx 截断 ⇒ **必须报错**。
// ⇒ 否则调用方（端点）会把「记录数 0 / 会话数 0」当完整结果对外说。
func TestR80TruncatedSummaryMustReportPartial(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})

	fr := NewFileReader(dir)

	// ★ ctx 已取消 ⇒ 计算必然被截断
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	summary, err := fr.GetBackupSummary(ctx)
	if err == nil {
		t.Fatalf("★ 计算被截断 ⇒ GetBackupSummary 必须报错（否则部分结果会被当成完整结果），实测 summary=%+v", summary)
	}
	if summary == nil {
		t.Fatal("★ 报错时仍应返回已算出的部分（summary 不能为 nil）")
	}
	// ★ 错误文本必须说明是「不完整」，而不是含糊的失败
	if !strings.Contains(err.Error(), "备份文件读取失败") {
		t.Errorf("★ 错误必须说明是部分结果（期望含「备份文件读取失败」），实测 %q", err.Error())
	}
}

// TestR80PartialResultIsCachedButMarked（★ 不变量② —— ★ **R82b 轮改判据**）
//
// ★ **R80 当时守的是**：「部分结果**可以被缓存**（否则每次请求都要重解压 5GB
//
//	⇒ 共享生产机上不可接受），但缓存里**必须带 partial 标记**」。
//
// ★★ **R82b 轮的订正（生产读数抓到的活锁）**：R80 的这条门在 R82 之后，
//
//	它守的**意图**（避免重复解压）**仍然成立**，但它当时用的**手段**
//	——「把【请求路径的就地计算结果】写进缓存」—— **变成了有害的**：
//
//	★ 生产实测（2830）：后台刷新跑完（写出**真实记录数**的 partial 结果），
//		而当天日文件被改写 ⇒ 指纹变 ⇒ 下一次请求走「就地计算」路径
//		⇒ 就地结果被 5s 预算截断（记录数 **0**）
//		⇒ ★ **覆盖掉后台刷新的好结果** ⇒ 端点继续显示「记录数 0」
//		⇒ ★⇒ 指纹**每天都会变** ⇒ 真实数字每天只存在十几分钟。
//
// ⇒ ⇒ ★★ **判据改为**：
//
//	① **请求路径**的就地结果（尤其 partial）⇒ **不写缓存**（绝不覆盖好结果）
//	② **后台刷新**的结果（完整或 partial）⇒ **写缓存**，且 partial **必须带标记**
//
// ★ 这就是「**门红了先读门在守什么**」：门没坏，**判据的适用边界变了**。
func TestR80PartialResultIsCachedButMarked(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})

	fr := NewFileReader(dir)
	// ★ R82 轮：冷启动路径现在**也会**触发后台刷新（否则真实数字永远出不来）
	//	⇒ 这里断言的是「缓存里的部分结果」⇒ 必须**关掉**后台刷新，否则它会
	//	把缓存改写成完整结果 ⇒ 断言变成竞态。
	fr.summaryRefreshDisabled = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fr.GetBackupSummary(ctx); err == nil {
		t.Fatal("★ 截断的计算必须报错")
	}

	// ★★ R82b 轮：**请求路径**的就地结果**不得**写缓存
	//	（否则它会覆盖后台刷新的好结果 ⇒ 生产读数实证的活锁）
	fr.summaryMu.Lock()
	cached := fr.summaryCache
	fr.summaryMu.Unlock()
	if cached != nil {
		t.Errorf("★ 请求路径的就地计算结果不得写缓存（它会被 5s 预算截断成「记录数 0」"+
			"⇒ 覆盖后台刷新的真实结果）—— 实测缓存里有一份 %+v", cached)
	}
}

// TestR82bBackgroundPartialResultMustBeCachedAndMarked（★ 不变量②·后台侧）
//
// ★ 与上一条成对：请求路径**不**写缓存 ⇒ 那么**后台刷新**的结果就是
//
//	**唯一**的缓存来源 ⇒ 它（哪怕是 partial）**必须**被缓存，
//	且 partial **必须带标记**（否则下一次调用会把它当完整结果交出去）。
func TestR82bBackgroundPartialResultMustBeCachedAndMarked(t *testing.T) {
	dir := t.TempDir()
	// 坏文件 ⇒ 统计必然不完整 ⇒ 后台刷新的结果必然是 partial
	r80WriteRawBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []byte("this-is-not-gzip"))

	fr := NewFileReader(dir)
	ctx := context.Background()

	// ★ 触发后台刷新（请求路径 ⇒ 冷启动 ⇒ 触发）
	if _, err := fr.GetBackupSummary(ctx); err == nil {
		t.Log("★ 提示：请求路径返回了完整结果")
	}
	// ★ 等后台刷新跑完
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fr.summaryMu.Lock()
		cached := fr.summaryCache
		fr.summaryMu.Unlock()
		if cached != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	fr.summaryMu.Lock()
	cached := fr.summaryCache
	fr.summaryMu.Unlock()
	if cached == nil {
		t.Fatal("★ 后台刷新的结果必须被缓存（它是唯一的缓存来源）")
	}
	if !cached.partial {
		t.Error("★ 缓存里的部分结果必须带 partial 标记（否则下一次调用会把它当完整结果交出去）")
	}
	if cached.partialError() == nil {
		t.Error("★ 带 partial 标记的缓存必须能翻译出 error（调用方据此标 degraded）")
	}
}

// TestR80BackgroundRefreshFillsCompleteSummary（★ 不变量③）
//
// ★ 过期 ⇒ **后台**刷新（请求路径不等它）⇒ 稍后拿到**完整**结果。
//
// ★ 这是 R80 的核心收益：备份摘要的重算要解压 5.18GB（~48s），
//
//	它**不能**发生在请求路径上（端点预算 5s）。
func TestR80BackgroundRefreshFillsCompleteSummary(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})

	fr := NewFileReader(dir)
	ctx := context.Background()

	// 首次：冷启动 ⇒ 就地算（完整）
	first, err := fr.GetBackupSummary(ctx)
	if err != nil {
		t.Fatalf("★ 首次取摘要失败：%v", err)
	}
	if first.TotalSessions != 2 {
		t.Fatalf("★ 首次摘要不对：sessions=%d（期望 2）", first.TotalSessions)
	}
	computations := fr.SummaryComputations()
	if computations != 1 {
		t.Fatalf("★ 首次应当只算一次，实测 %d 次", computations)
	}

	// ★ 把 TTL 调到 0 ⇒ 下一次调用必然走「过期」路径
	fr.summaryCacheTTL = 0

	start := time.Now()
	second, err := fr.GetBackupSummary(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("★ 过期路径不应报错（返回旧值即可）：%v", err)
	}
	if second.TotalSessions != 2 {
		t.Errorf("★ 过期路径应当返回上一版摘要（sessions=%d，期望 2）", second.TotalSessions)
	}
	// ★ 请求路径**不等**后台重算 ⇒ 应当很快返回
	if elapsed > 2*time.Second {
		t.Errorf("★ 过期路径必须走后台刷新（请求不应等待重算），实测耗时 %v", elapsed)
	}

	// ★ 后台重算最终会跑完 ⇒ 计数增加 ⇒ 缓存变成完整
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fr.SummaryComputations() > computations {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := fr.SummaryComputations(); got <= computations {
		t.Fatalf("★ 后台刷新应当把摘要重算一次（计数应 > %d，实测 %d）", computations, got)
	}

	fr.summaryMu.Lock()
	cached := fr.summaryCache
	fr.summaryMu.Unlock()
	if cached == nil || cached.partial {
		t.Errorf("★ 后台刷新后的缓存必须是完整结果（partial=%v）", cached != nil && cached.partial)
	}
}

// TestR80OnePassUnionMatchesTwoPassUnion（★ 不变量④：口径不变）
//
// ★ R80 轮把「算跨文件唯一 session 数」从**第二遍解压**改成
// **复用第一遍的 session 集合** ⇒ 口径必须**逐字不变**。
//
// ★ 验证方式 = **用旧实现作对照**（R79/R77 同款纪律）：
//
//	旧实现：对每个文件再 ReadRecords 一遍，取并集；
//	新实现：复用第一遍的每文件 session 集合，取并集。
//
// ★ 对照件特意让两个文件**共享一个 session id**（s2）⇒
//
//	并集 = 3，而「各文件会话数之和」= 4 ⇒ 能区分「并集」与「求和」。
func TestR80OnePassUnionMatchesTwoPassUnion(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})
	r80WriteBackup(t, dir, "sessions-2026-10-21.jsonl.gz", []string{"s2", "s3"})

	fr := NewFileReader(dir)
	ctx := context.Background()

	summary, err := fr.GetBackupSummary(ctx)
	if err != nil {
		t.Fatalf("★ 取摘要失败：%v", err)
	}

	// ★ 旧实现（两趟）作对照
	files, err := fr.ListBackupFiles(ctx)
	if err != nil {
		t.Fatalf("★ 列文件失败：%v", err)
	}
	oldUnion := map[string]struct{}{}
	for _, f := range files {
		if err := fr.ReadRecords(ctx, f.Filename, func(record BackupRecord) error {
			oldUnion[record.SessionID] = struct{}{}
			return nil
		}); err != nil {
			t.Fatalf("★ 对照读取失败：%v", err)
		}
	}

	if summary.TotalSessions != len(oldUnion) {
		t.Errorf("★ 一趟并集与旧实现（两趟）不一致：新=%d 旧=%d", summary.TotalSessions, len(oldUnion))
	}
	if summary.TotalSessions != 3 {
		t.Errorf("★ 并集应为 3（s1,s2,s3），实测 %d —— 若为 4 说明退化成了「各文件求和」", summary.TotalSessions)
	}
	if summary.TotalRecords != 4 {
		t.Errorf("★ 总记录数应为 4（2+2），实测 %d", summary.TotalRecords)
	}
}

// TestR80EachFileIsReadOnlyOnce（★ 不变量⑤：每个文件只解压一遍）
//
// ★ 这是 R79 的根因（「每个备份文件被完整解压两遍」）的**源头级**修复：
//
//	旧实现算一次摘要 ⇒ 每个文件解压 **2** 遍（统计 + 全局 session）；
//	⇒ 现在第一遍就把 session 集合带出来 ⇒ 每个文件只解压 **1** 遍。
func TestR80EachFileIsReadOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})
	r80WriteBackup(t, dir, "sessions-2026-10-21.jsonl.gz", []string{"s3"})
	r80WriteBackup(t, dir, "sessions-2026-10-22.jsonl.gz", []string{"s4", "s5"})

	fr := NewFileReader(dir)
	ctx := context.Background()

	if _, err := fr.GetBackupSummary(ctx); err != nil {
		t.Fatalf("★ 取摘要失败：%v", err)
	}

	if got := fr.FileReads(); got != 3 {
		t.Errorf("★ 3 个文件的摘要应当只解压 3 遍（每个文件一遍），实测 %d 遍 —— "+
			"说明又回到了「每个文件解压两遍」", got)
	}
}
