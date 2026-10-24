package dbdegradation

// R82 轮（2026-10-24）：备份摘要的**读法**必须能读「坏文件」。
//
// ★★ 起因（154 生产实测）：
//
//	R81 把摘要链路从「打捞」改成「纯流式」（快 ~15×），
//	但生产上的备份文件**恰恰大多是坏的**，而仓库自己的设计注释
//	（`file_reader_salvage.go` 头部 + `file_writer.go` FIXME-14370）早就写明：
//
//	    「孤儿 gzip header 连排 + 中段一个完整 member + 截断尾巴
//	      ⇒ gzip.NewReader 首个 member 即解码失败 → 整文件 0 条可恢复」
//
//	⇒ 154 生产实测（`gzip -t` 全量扫描 100 个文件）：**53/100 个文件单遍读不出**
//	⇒ ⇒ 端点对外说「100 个备份 / 5.18GB / 记录数 0」是「**读法错**」，
//	      不是「备份是空的」—— R79/R80 反复禁止的**静默的错误数据**。
//
// ⇒ R82 的修法：**快路径（流式）+ 失败回退打捞**，口径与 R80 **逐字相同**；
//	并把「读不出来」与「读出来了但有损」**分开记账**。
//
// ★ 本文件钉住的**不变量**：
//
//	① 坏文件的记录数必须与**打捞口径**逐字相同（不能少算 ⇒ 不能对外说 0）
//	② 「有损」与「读不出」必须**分别**记账（运维要能区分）
//	③ **请求路径**不得走打捞（打捞昂贵 ⇒ 5s 预算里必然被打断 ⇒ IO 风暴）
//	④ **后台刷新**允许打捞，但**只对快路径失败的文件**（探针数 ≤ 回退数）
//	⑤ 阳性对照：坏文件**必须**真的触发回退（否则 ①~④ 都是空转）

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r82OrphanGzipHeader 是「孤儿 gzip header」：只有 10 字节头、没有任何 deflate 数据。
//
// ★ 生产形态（154 实测的十六进制）：
//
//	损坏: 1f8b0800...ff | 1f8b0800...ff | 1f8b0800...ff | <真实数据>
//	完好: 1f8b0800...ff | <真实数据>
//
//	成因（仓库自有注释）：蓝绿部署多进程 O_APPEND + 容器硬杀跳过 gzip.Close。
var r82OrphanGzipHeader = []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff}

// r82WriteDamagedBackup 写一个「生产同形」的坏备份文件：
//
//	[孤儿 header ×N] + [完整 member] + [可选：截断的尾巴]
func r82WriteDamagedBackup(t *testing.T, dir, name string, sessions []string, orphanHeaders int, truncatedTail bool) string {
	t.Helper()

	// 完整 member
	var member bytes.Buffer
	zw := gzip.NewWriter(&member)
	for _, s := range sessions {
		if _, err := zw.Write([]byte(`{"session_id":"` + s + `","record_type":"x"}` + "\n")); err != nil {
			t.Fatalf("★ 写 member 失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("★ 关 gzip 失败：%v", err)
	}

	var content []byte
	for i := 0; i < orphanHeaders; i++ {
		content = append(content, r82OrphanGzipHeader...)
	}
	content = append(content, member.Bytes()...)

	if truncatedTail {
		// 截断的另一个 member 的前半段（容器硬杀 ⇒ gzip.Close 没跑）
		var tail bytes.Buffer
		tw := gzip.NewWriter(&tail)
		if _, err := tw.Write([]byte(`{"session_id":"tail-sess","record_type":"x"}` + "\n")); err != nil {
			t.Fatalf("★ 写 tail 失败：%v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("★ 关 tail gzip 失败：%v", err)
		}
		tailBytes := tail.Bytes()
		content = append(content, tailBytes[:len(tailBytes)/2]...)
	}

	path := filepath.Join(dir, "backups", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("★ 写文件失败：%v", err)
	}
	return path
}

// TestR82SummaryMustRecoverDamagedFileViaSalvage（★ 不变量①：口径逐字相同）
//
// ★ 生产症状的直接对照：154 上端点对外说「记录数 0 / 会话数 0」，
//
//	而实际坏文件里**有可救回的记录** ⇒ 「0」是**读法错**，不是事实。
//
// ⇒ 这里断言：坏文件经摘要链路读出的记录数/会话数，
//
//	必须与**打捞口径**（`GetFileSummary`，R80 的底座）**逐字相同**。
func TestR82SummaryMustRecoverDamagedFileViaSalvage(t *testing.T) {
	dir := t.TempDir()
	const name = "sessions-2026-10-20.jsonl.gz"
	r82WriteDamagedBackup(t, dir, name, []string{"s1", "s2", "s1", "s3"}, 2, true)

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	// ★ 打捞口径（R80 的底座）作对照
	salvaged, err := fr.GetFileSummary(ctx, name)
	if err != nil {
		t.Fatalf("★ 打捞读失败：%v", err)
	}
	if salvaged.RecordCount == 0 {
		t.Fatal("★ 前置条件失效：打捞都读不出记录 ⇒ 这条门测不到东西")
	}

	summary, partial, perr := fr.computeSummary(ctx, mustListBackupFiles(t, fr))
	if summary == nil {
		t.Fatalf("★ 摘要为 nil：err=%v", perr)
	}
	if summary.TotalRecords != salvaged.RecordCount {
		t.Errorf("★ 坏文件的记录数必须与打捞口径逐字相同：摘要=%d 打捞=%d "+
			"（少算 ⇒ 端点会对外说「记录数 0」—— 生产实测的静默错误数据）",
			summary.TotalRecords, salvaged.RecordCount)
	}
	if summary.TotalSessions != salvaged.SessionCount {
		t.Errorf("★ 坏文件的会话数必须与打捞口径逐字相同：摘要=%d 打捞=%d",
			summary.TotalSessions, salvaged.SessionCount)
	}
	// ★ 有损 ⇒ 必须标 partial（数据不完整 ⇒ 不能当完整结果对外说）
	if !partial {
		t.Errorf("★ 坏文件读出来但有损 ⇒ 摘要必须标 partial（实测 summary=%+v err=%v）", summary, perr)
	}
	if perr == nil {
		t.Error("★ 有损的摘要必须带错误（部分结果 ≠ 完整结果）")
	}
}

// TestR82SummaryMustDistinguishLossyFromUnreadable（★ 不变量②：分开记账）
//
// ★ 「读不出来」与「读出来了但不完整」是**两件事**：
//
//	运维看到前者 ⇒ 文件坏了，要去查备份链路；
//	看到后者 ⇒ 文件部分可恢复，档案还能救。
//
// ⇒ 错误文本必须**分别**点名，不能混成一句。
func TestR82SummaryMustDistinguishLossyFromUnreadable(t *testing.T) {
	dir := t.TempDir()
	// 有损（可救回）
	r82WriteDamagedBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1"}, 2, true)
	// 完全读不出（不是 gzip）
	r80WriteRawBackup(t, dir, "sessions-2026-10-21.jsonl.gz", []byte("this-is-not-gzip"))

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	_, partial, perr := fr.computeSummary(ctx, mustListBackupFiles(t, fr))
	if !partial || perr == nil {
		t.Fatalf("★ 混合场景必须 partial 且带错误（实测 partial=%v err=%v）", partial, perr)
	}
	msg := perr.Error()
	if !strings.Contains(msg, "有损") {
		t.Errorf("★ 错误必须点名「有损」（实测 %q）", msg)
	}
	if !strings.Contains(msg, "读取失败") {
		t.Errorf("★ 错误必须点名「读取失败」（实测 %q）", msg)
	}
}

// TestR82RequestPathMustNotUseSalvage（★ 不变量③：请求路径不打捞）
//
// ★ 打捞对坏文件要 `os.ReadFile` 整个文件（**不理会 ctx**）
//
//	⇒ 5s 预算里逐个打捞 = IO 风暴。
//
// ⇒ 请求路径（`GetBackupSummary` 的就地计算）必须**只**走快路径。
func TestR82RequestPathMustNotUseSalvage(t *testing.T) {
	dir := t.TempDir()
	r82WriteDamagedBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1"}, 2, true)

	fr := NewFileReader(dir)
	// ★ 关掉后台刷新 ⇒ 只测**请求路径**
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	before := SalvageMemberProbes()
	if _, err := fr.GetBackupSummary(ctx); err == nil {
		t.Log("★ 提示：请求路径返回了完整结果（若该文件被读成完整，说明快路径变了）")
	}
	after := SalvageMemberProbes()
	if after != before {
		t.Errorf("★ 请求路径不得走打捞（探针数 %d → %d）—— 打捞会无视 ctx 整文件读入，"+
			"在 5s 预算里 = IO 风暴", before, after)
	}
	if fr.SummarySalvageFallbacks() != 0 {
		t.Errorf("★ 请求路径的回退次数必须为 0（实测 %d）", fr.SummarySalvageFallbacks())
	}
}

// TestR82BackgroundRefreshMaySalvageOnlyFailedFiles（★ 不变量④）
//
// ★ 后台刷新（预算 30min）**允许**打捞 —— 这是坏文件能被计数的唯一途径。
//
// ⇒ 但**只允许**对快路径失败的文件打捞：
//
//	好文件走快路径（~15× 快），坏文件才回退。
//
// ⇒ 断言：回退次数 == 坏文件数（1），且探针数 > 0（阳性对照）。
func TestR82BackgroundRefreshMaySalvageOnlyFailedFiles(t *testing.T) {
	dir := t.TempDir()
	// 好文件
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"g1", "g2"})
	// 坏文件
	r82WriteDamagedBackup(t, dir, "sessions-2026-10-21.jsonl.gz", []string{"b1"}, 2, true)

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	files := mustListBackupFiles(t, fr)
	probeBefore := SalvageMemberProbes()
	summary, partial, perr := fr.computeSummary(ctx, files)
	probeAfter := SalvageMemberProbes()

	if summary == nil {
		t.Fatalf("★ 摘要为 nil：err=%v", perr)
	}
	if got := fr.SummarySalvageFallbacks(); got != 1 {
		t.Errorf("★ 回退次数必须恰好等于坏文件数 1（实测 %d）", got)
	}
	if probeAfter <= probeBefore {
		t.Errorf("★ 阳性对照失效：坏文件必须真的走打捞（探针数 %d → %d）", probeBefore, probeAfter)
	}
	if summary.TotalRecords != 3 {
		t.Errorf("★ 摘要必须把坏文件的记录也算进来（好文件 2 条 + 坏文件 1 条），实测 %d", summary.TotalRecords)
	}
	if !partial {
		t.Errorf("★ 坏文件有损 ⇒ 必须标 partial（实测 summary=%+v err=%v）", summary, perr)
	}
}

// TestR82SummaryMustNotReportZeroForDamagedFile（★ 生产症状的直接回归门）
//
// ★ 生产症状：端点对外说「100 个备份 / 5.18GB / **记录数 0** / 会话数 0」。
//
// ⇒ 这里断言：**只要文件里有可救回的记录，摘要就不得报 0**。
func TestR82SummaryMustNotReportZeroForDamagedFile(t *testing.T) {
	dir := t.TempDir()
	r82WriteDamagedBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2", "s3"}, 3, false)

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	summary, _, _ := fr.computeSummary(ctx, mustListBackupFiles(t, fr))
	if summary == nil {
		t.Fatal("★ 摘要为 nil")
	}
	if summary.TotalRecords == 0 {
		t.Errorf("★ 坏文件里有记录时摘要不得报 0（实测 TotalRecords=%d）—— "+
			"这正是生产上「记录数 0」的来源", summary.TotalRecords)
	}
	if summary.TotalSessions == 0 {
		t.Errorf("★ 坏文件里有会话时摘要不得报 0（实测 TotalSessions=%d）", summary.TotalSessions)
	}
}

// r82WriteBackupWithoutSessionID 写一个「记录**不带** session_id」的备份文件。
//
// ★ 生产同形（154 实测）：备份记录几乎全是 `request_wal` / `request_log`，
//
//	顶层字段只有 `type` / `timestamp` / `record_key` / `payload`
//	⇒ **没有 session_id** ⇒ `BackupRecord.SessionID` 恒为空
//	⇒ 「跨文件唯一 session 数」在旧实现里恒为 **1**（唯一集合里只有空串）。
func r82WriteBackupWithoutSessionID(t *testing.T, dir, name string, records int) string {
	t.Helper()
	path := filepath.Join(dir, "backups", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for i := 0; i < records; i++ {
		line := `{"type":"request_wal","timestamp":"2026-10-23T16:12:10Z","record_key":"req-` +
			string(rune('a'+i%26)) + `:update","payload":{"RequestID":"req-` +
			string(rune('a'+i%26)) + `"}}` + "\n"
		if _, err := zw.Write([]byte(line)); err != nil {
			t.Fatalf("★ 写记录失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("★ 关 gzip 失败：%v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("★ 写文件失败：%v", err)
	}
	return path
}

// TestR82SessionCountMustNotCountEmptyStringAsSession（★ R82b：口径修正）
//
// ★ 生产实测（154）：端点对外说「会话数 = 1」，而备份里 57644 条记录
//
//	**一条都没有 session_id** ⇒ 「1」是**空串**被当成了会话 ⇒ **读数失真**。
//
// ⇒ 空串**不是**会话 ⇒ 会话数必须是 0，且必须**显式**说「该口径不可用」。
func TestR82SessionCountMustNotCountEmptyStringAsSession(t *testing.T) {
	dir := t.TempDir()
	r82WriteBackupWithoutSessionID(t, dir, "sessions-2026-10-20.jsonl.gz", 5)

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	summary, _, _ := fr.computeSummary(ctx, mustListBackupFiles(t, fr))
	if summary == nil {
		t.Fatal("★ 摘要为 nil")
	}
	if summary.TotalRecords != 5 {
		t.Fatalf("★ 记录数应为 5，实测 %d", summary.TotalRecords)
	}
	if summary.TotalSessions != 0 {
		t.Errorf("★ 空串不是会话 ⇒ 会话数必须是 0（实测 %d）—— "+
			"旧实现把空串算成一个会话 ⇒ 生产上对外说「会话数 1」", summary.TotalSessions)
	}
	if summary.SessionCountAvailable {
		t.Error("★ 有记录却一个真实 session id 都没有 ⇒ 会话数口径**不可用**，必须显式标记")
	}
}

// TestR82SessionCountAvailableWhenRecordsCarrySessionID（★ R82b：阳性对照）
//
// ★ 「不可用」不能变成**恒假的门** ⇒ 有 session_id 时必须可用且计数正确。
func TestR82SessionCountAvailableWhenRecordsCarrySessionID(t *testing.T) {
	dir := t.TempDir()
	r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2", "s1"})

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	summary, _, _ := fr.computeSummary(ctx, mustListBackupFiles(t, fr))
	if summary == nil {
		t.Fatal("★ 摘要为 nil")
	}
	if !summary.SessionCountAvailable {
		t.Error("★ 记录带 session_id ⇒ 会话数口径必须可用")
	}
	if summary.TotalSessions != 2 {
		t.Errorf("★ 会话数必须是去重后的真实值 2，实测 %d", summary.TotalSessions)
	}
}

// TestR82SummaryMustKeepStatMetadataWhenCountsFail（★ R82b：元信息 ≠ 统计）
//
// ★ 生产读数抓到的回归（R82 第一版）：统计被 ctx 截断时，
//
//	摘要对外说「备份总大小 **0**」而文件清单里明明有 100 个文件
//	（2829 的读数是 5178224736 / "2026-08-17 to 2026-10-24"）。
//
// ⇒ ★ **「统计失败」不等于「元信息没有」** ——
//
//	大小/日期来自 **stat**，与解压无关 ⇒ **必须**照报。
func TestR82SummaryMustKeepStatMetadataWhenCountsFail(t *testing.T) {
	dir := t.TempDir()
	path := r80WriteBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []string{"s1", "s2"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("★ stat 失败：%v", err)
	}

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	// ★ ctx 已取消 ⇒ 统计必然失败
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// ★★ 两条路径**都要**覆盖（★ M15 第一版只变异了其中一条 ⇒ 门恒绿 ⇒ 没牙）：
	//	① 请求路径（allowSalvage=false）—— ★ 生产回归就发生在这里；
	//	② 后台刷新路径（allowSalvage=true）。
	for _, allowSalvage := range []bool{false, true} {
		summary, _, _ := fr.computeSummaryWith(ctx, mustListBackupFiles(t, fr), allowSalvage)
		if summary == nil {
			t.Fatalf("★ 摘要为 nil（allowSalvage=%v）", allowSalvage)
		}
		if summary.TotalSize != info.Size() {
			t.Errorf("★ 统计失败时【大小】仍须照报（它来自 stat，与解压无关）：allowSalvage=%v 期望 %d 实测 %d",
				allowSalvage, info.Size(), summary.TotalSize)
		}
		if summary.DateRange == "" {
			t.Errorf("★ 统计失败时【日期范围】仍须照报（它来自文件名，与解压无关）：allowSalvage=%v", allowSalvage)
		}
	}
}

// TestR82SummaryMustKeepStatMetadataWhenBothPathsFail（★ R82b：两条路都失败）
//
// ★ 上一条门只覆盖了「ctx 提前退出」那一条分支（★ M15 第一版 ⇒ 门恒绿 ⇒ 没牙）。
//
// ⇒ 这里覆盖**真正会失败的那条**：【流式失败 + 打捞也失败】
//
//	（生产形态：不是 gzip 的文件）⇒ 摘要仍必须报 stat 级的大小/日期。
func TestR82SummaryMustKeepStatMetadataWhenBothPathsFail(t *testing.T) {
	dir := t.TempDir()
	path := r80WriteRawBackup(t, dir, "sessions-2026-10-20.jsonl.gz", []byte("this-is-not-gzip"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("★ stat 失败：%v", err)
	}

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	ctx := context.Background()

	for _, allowSalvage := range []bool{false, true} {
		summary, _, _ := fr.computeSummaryWith(ctx, mustListBackupFiles(t, fr), allowSalvage)
		if summary == nil {
			t.Fatalf("★ 摘要为 nil（allowSalvage=%v）", allowSalvage)
		}
		if summary.TotalSize != info.Size() {
			t.Errorf("★ 两条路都失败时【大小】仍须照报：allowSalvage=%v 期望 %d 实测 %d",
				allowSalvage, info.Size(), summary.TotalSize)
		}
		if summary.DateRange == "" {
			t.Errorf("★ 两条路都失败时【日期范围】仍须照报：allowSalvage=%v", allowSalvage)
		}
	}
}

// TestR82bSessionCountUnavailableMustNotDependOnRecordCount（★ R82b：判定不依赖「记录数」）
//
// ★ 生产冷读（2831）抓到：冷启动窗口里 `total_records = 0`
//
//	（意思是「**还没统计完**」，不是「真的 0 条记录」）
//	⇒ 用它判定「会话数口径是否可用」会**误报可用**。
//
// ⇒ 判据必须是「**有备份文件、却一个真实 session id 都没有**」。
func TestR82bSessionCountUnavailableMustNotDependOnRecordCount(t *testing.T) {
	dir := t.TempDir()
	r82WriteBackupWithoutSessionID(t, dir, "sessions-2026-10-20.jsonl.gz", 5)

	fr := NewFileReader(dir)
	fr.summaryRefreshDisabled = true
	// ★ ctx 已取消 ⇒ 统计被截断 ⇒ 记录数是 0（**未统计完**）
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	summary, _, _ := fr.computeSummaryWith(ctx, mustListBackupFiles(t, fr), false)
	if summary == nil {
		t.Fatal("★ 摘要为 nil")
	}
	if summary.TotalRecords != 0 {
		t.Fatalf("★ 前置条件：统计应被截断（TotalRecords=%d）", summary.TotalRecords)
	}
	if summary.SessionCountAvailable {
		t.Error("★ 冷启动窗口（记录数 = 0 = 未统计完）不得报「会话数口径可用」—— " +
			"判据不能依赖记录数（生产实测的误报）")
	}
}
