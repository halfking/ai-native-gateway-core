package dbdegradation

// R81 轮（2026-10-24）：备份摘要的**读取路径**守卫。
//
// ★ 背景（154 生产实测）：
//
//	同一个 106MB 备份文件：
//	  - 纯 gzip 解压（`gzip -dc | wc -l`）= **2.92s**
//	  - 网关摘要路径（旧，打捞路径）        = **~45s**
//	⇒ 差 ~15×，差在**打捞路径的 member 边界搜索**
//	（`gzipMemberEnd` 用「指数查找 + 二分查找」逐个**试解**前缀
//	  ⇒ 同一个文件被**反复完整解码 ~20~30 次**）。
//
// ⇒ R81：摘要链路改走**纯流式**（打捞是给「恢复」链路用的，摘要不需要）。
//
// ★ 不变量：**口径不变** —— 完好文件上，流式与打捞读出的
// 「记录数 / 会话数 / 会话集合」**逐字相同**。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestR81StreamingSummaryMatchesSalvageCounts（★ 口径不变）
func TestR81StreamingSummaryMatchesSalvageCounts(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2", "s1"})

	fr := NewFileReader(dir)
	ctx := context.Background()
	const name = "sessions-2026-10-20.jsonl.gz"

	// ★ 新路径（流式）
	streamed, sessions, err := fr.readFileSummaryStreaming(ctx, name)
	if err != nil {
		t.Fatalf("★ 流式读失败：%v", err)
	}

	// ★ 旧路径（打捞）作对照
	salvaged, err := fr.GetFileSummary(ctx, name)
	if err != nil {
		t.Fatalf("★ 打捞读失败：%v", err)
	}

	if streamed.RecordCount != salvaged.RecordCount {
		t.Errorf("★ 记录数必须逐字相同：流式=%d 打捞=%d", streamed.RecordCount, salvaged.RecordCount)
	}
	if streamed.SessionCount != salvaged.SessionCount {
		t.Errorf("★ 会话数必须逐字相同：流式=%d 打捞=%d", streamed.SessionCount, salvaged.SessionCount)
	}
	if len(sessions) != salvaged.SessionCount {
		t.Errorf("★ 流式返回的会话集合大小与计数不一致：集合=%d 计数=%d", len(sessions), salvaged.SessionCount)
	}
	if streamed.Size != salvaged.Size || streamed.Date != salvaged.Date {
		t.Errorf("★ 元信息必须相同：流式=(%d,%s) 打捞=(%d,%s)",
			streamed.Size, streamed.Date, salvaged.Size, salvaged.Date)
	}
}

// TestR81StreamingSummaryMustReportDamagedFile（★ 损坏文件必须被记账）
//
// ★ 流式读遇到损坏文件会**报错** ⇒ 该文件被计入「失败」⇒ 摘要标 **partial**
//
//	⇒ 「统计不完整」会被显式记账，而不是伪装成「这个文件有 0 条记录」。
func TestR81StreamingSummaryMustReportDamagedFile(t *testing.T) {
	dir := t.TempDir()
	// ★ 不是 gzip 的内容
	r80WriteRawBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []byte("this-is-not-gzip"))

	fr := NewFileReader(dir)
	ctx := context.Background()

	_, _, err := fr.readFileSummaryStreaming(ctx, "sessions-2026-10-20.jsonl.gz")
	if err == nil {
		t.Fatal("★ 损坏文件必须报错（否则会被当成「0 条记录」的完好文件）")
	}

	// ★ 摘要链路必须把它算成「失败」⇒ 整体标 partial
	summary, partial, perr := fr.computeSummary(ctx, mustListBackupFiles(t, fr))
	if !partial {
		t.Errorf("★ 有文件读失败 ⇒ 摘要必须标 partial（实测 summary=%+v err=%v）", summary, perr)
	}
	if perr == nil {
		t.Error("★ 摘要必须带错误（部分结果 ≠ 完整结果）")
	}
}

// r80WriteRawBackup 写一个**原始字节**的备份文件（用于构造损坏件）。
func r80WriteRawBackup(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "backups", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("★ 写文件失败：%v", err)
	}
	return path
}

// mustListBackupFiles 列出备份文件（测试辅助）。
func mustListBackupFiles(t *testing.T, fr *FileReader) []BackupFile {
	t.Helper()
	files, err := fr.ListBackupFiles(context.Background())
	if err != nil {
		t.Fatalf("★ 列备份文件失败：%v", err)
	}
	return files
}

// TestR81SummaryMustNotUseSalvageMemberSearch（★ 成本不变量）
//
// ★ 打捞路径的 member 边界搜索（`gzipMemberEnd`）每做一次「前缀试解」
//
//	就**完整解码**那个前缀 ⇒ 一个文件被反复解码 ~20~30 次
//	⇒ 实测同���个 106MB 文件：纯解压 2.92s vs 打捞路径 ~45s（**~15×**）。
//
// ⇒ 摘要链路**只**要计数 ⇒ **绝不能**走打捞路径
//
//	⇒ 判据用**探针计数**（确定性），**不用计时**（计时断言天然 flaky）。
//
// ★ 阳性对照：打捞路径本身**必须**产生探针
//
//	⇒ 否则这条门可能因为「计数器坏了」而恒绿（量具失效的经典形态）。
func TestR81SummaryMustNotUseSalvageMemberSearch(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})

	fr := NewFileReader(dir)
	// ★ R82 轮：冷启动路径现在**也会**触发后台刷新（这是修「真实数字出不来」的
	//	关键一环）⇒ 而后台刷新**允许**打捞回退 ⇒ 这里要断言的是
	//	「**请求路径**不得走打捞」⇒ 必须**关掉**后台刷新，否则测量是竞态的。
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	// ★ 摘要链路 ⇒ 探针数**不得增长**
	before := SalvageMemberProbes()
	if _, err := fr.GetBackupSummary(ctx); err != nil {
		t.Fatalf("★ 取摘要失败：%v", err)
	}
	after := SalvageMemberProbes()
	if after != before {
		t.Errorf("★ 请求路径不得走打捞的 member 边界搜索（探针数 %d → %d）—— "+
			"那是 ~15× 的成本放大器（实测同文件 2.92s vs ~45s）", before, after)
	}

	// ★ 阳性对照：打捞路径本身必须产生探针（否则上面的断言是空转）
	salvageBefore := SalvageMemberProbes()
	if _, err := fr.GetFileSummary(ctx, "sessions-2026-10-20.jsonl.gz"); err != nil {
		t.Fatalf("★ 打捞读失败：%v", err)
	}
	if SalvageMemberProbes() == salvageBefore {
		t.Error("★ 阳性对照失效：打捞路径应当产生 member 边界搜索探针（否则计数没生效，门是空转）")
	}
}
