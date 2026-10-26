package dbdegradation

// R82 轮（2026-10-24）：备份摘要的**读取路径**必须能读「坏文件」。
//
// ★★ 起因（154 生产实测，R81 发版后的读数）：
//
//	R81 把摘要链路从「打捞」改成「纯流式」，理由是打捞慢 ~15×
//	（member 边界搜索：每个文件被反复完整解码 ~20~30 次）。
//	⇒ 但生产上的备份文件**恰恰大多是坏的**，而**仓库自己的设计注释**
//	  （`file_reader_salvage.go` 头部 + `file_writer.go` FIXME-14370）
//	  早就写明：
//
//	    「蓝绿部署多进程 O_APPEND 追加同一日文件 + 容器硬杀跳过 gzip.Close
//	      ⇒ 孤儿 gzip header 连排 + 中段一个完整 member + 截断尾巴
//	      ⇒ gzip.NewReader 首个 member 即解码失败 → 整文件 0 条可恢复」
//
//	⇒ ★★ 单遍 `gzip.NewReader` 读这些文件 ⇒ **整文件 0 条可恢复**
//	⇒ ⇒ R81 的「纯流式」在生产数据上**读不出任何记录**
//	⇒ ⇒ 端点对外说「100 个备份 / 5.18GB / 记录数 0」是「**读法错**」，
//	      不是「备份是空的」—— 这正是 R79/R80 反复禁止的**静默的错误数据**。
//
// ★ 生产实测（154，2026-10-24，只读，`gzip -t` 全量扫描 100 个文件）：
//
//	**53/100 个文件 `gzip -t` 失败**（文件头 = 「孤儿 gzip header ×1~5」+ 后续 member）
//	⇒ 单遍 gzip 读不出 = **53%**
//	⇒ ⇒ 「摘要永远 incomplete、total_records 永远 0」**不是数据问题，是读法问题**。
//
// ⇒ R82 的修法：**快路径 + 打捞回退**，且**口径与 R80 完全一致**：
//
//	① 快路径：纯流式（完好文件 ~2.9s/106MB，比打捞快 ~15×）
//	② 快路径失败 ⇒ **回退打捞**（`ReadRecordsWithStats`）
//	   ⇒ 打捞**逐字节重同步到下一个 gzip magic** ⇒ 能读出坏文件里的记录
//	   ⇒ 口径与 R80（打捞口径）**逐字相同** ⇒ **零口径回归**
//	③ 打捞读出但**有损**（LostBytes/SkippedLines>0）⇒ **单独记账**
//	   ⇒ 摘要标 partial 且 reason 点名「有损」
//	   ⇒ 运维能区分「读不出来」与「读出来了但不完整」
//
// ★ 为什么请求路径**不做**打捞回退：
//
//	打捞对 53 个坏文件要 ~11min（member 边界搜索 ⇒ O(n log n) 解码）
//	⇒ 端点只有 5s 预算 ⇒ 请求路径上打捞**必然**被打断 ⇒ 只会更空。
//	⇒ 请求路径 = 纯流式快路径；**后台刷新**才做打捞回退（预算 30min）。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// fileSummaryOutcome 描述「一个备份文件的摘要读取结果」。
type fileSummaryOutcome struct {
	// file 文件元信息 + 计数（读不出时为 nil）。
	file *BackupFile
	// sessions 该文件出现过的 session id 集合（用于跨文件全局唯一并集）。
	sessions map[string]struct{}
	// lossy=true 表示「读出来了，但内容有损」
	//（打捞统计 LostBytes/SkippedLines > 0 ⇒ 部分内容不可恢复）。
	lossy bool
	// err != nil 表示「**完全读不出**」（快路径与回退都失败）。
	err error
}

// readFileSummaryResilient 读一个备份文件的摘要：**快路径优先，失败回退打捞**。
//
// ★ 口径不变（与 R80 的 `readFileSummary` / `GetFileSummary` **逐字相同**）：
//
//	回退路径用的就是 `ReadRecordsWithStats`（打捞）⇒ 同一套语义。
//
// ★ allowSalvage=false 时**不做**回退（请求路径用）：
//
//	打捞对坏文件要 ~11min ⇒ 在 5s 预算里必然被打断 ⇒ 徒增 IO。
func (fr *FileReader) readFileSummaryResilient(ctx context.Context, filename string, allowSalvage bool) fileSummaryOutcome {
	// ① 快路径：纯流式
	file, sessions, err := fr.readFileSummaryStreaming(ctx, filename)
	if err == nil {
		return fileSummaryOutcome{file: file, sessions: sessions}
	}
	streamErr := err

	if !allowSalvage {
		// ★ R82b 轮（生产读数抓到的回归）：**统计失败 ≠ 元信息没有**
		//	⇒ 必须保留 stat 级的元信息（大小/日期）
		//	⇒ 否则摘要会对外说「备份总大小 0」而文件清单明明有 100 个文件
		//	（2829 的读数是 5178224736 / "2026-08-17 to 2026-10-24"）。
		return fileSummaryOutcome{file: file, err: streamErr}
	}

	// ★ 预算已经耗尽 ⇒ **不要**再进打捞：
	//	打捞会 `os.ReadFile` 把整个文件读进内存（**不理会 ctx**）
	//	⇒ 预算耗尽后仍逐个文件打捞 = IO 风暴（53 个坏文件 × 105MB）。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fileSummaryOutcome{file: file, err: streamErr}
	}

	// ② 回退：打捞（能读出坏文件里的记录）
	salvaged, salvageSessions, loss, salvErr := fr.readFileSummarySalvage(ctx, filename)
	if salvErr != nil {
		return fileSummaryOutcome{
			file: file,
			err:  fmt.Errorf("流式失败（%v）且打捞失败：%w", streamErr, salvErr),
		}
	}
	atomic.AddInt64(&fr.summarySalvageFallbacks, 1)
	slog.Warn("file reader: summary fell back to salvage path",
		"filename", filename, "lossy", loss, "stream_error", streamErr)
	return fileSummaryOutcome{file: salvaged, sessions: salvageSessions, lossy: loss}
}

// readFileSummarySalvage 用**打捞**路径读一个备份文件的摘要。
//
// ★ 它是 `readFileSummary` 的**同口径**版本，区别只有两点：
//
//	① **不吞错**（`readFileSummary` 吞掉统计错误 ⇒ 恢复链路依赖「能救多少救多少」）
//	② 额外返回「有损」标记（`SalvageStats.HasLoss()`）
func (fr *FileReader) readFileSummarySalvage(ctx context.Context, filename string) (*BackupFile, map[string]struct{}, bool, error) {
	path, err := fr.backupPath(filename)
	if err != nil {
		return nil, nil, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("stat file: %w", err)
	}

	date := strings.TrimPrefix(filename, "sessions-")
	date = strings.TrimSuffix(date, ".jsonl.gz")

	sessions := make(map[string]struct{})
	recordCount := 0

	// ★ 打捞路径（与 R80 的 readFileSummary **同一个底座** ⇒ 口径逐字相同）
	stats, err := fr.ReadRecordsWithStats(ctx, filename, func(record BackupRecord) error {
		recordCount++
		// ★ R82b 轮：**空串不是会话** ⇒ 不计入 session 集合。
		if record.SessionID != "" {
			sessions[record.SessionID] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}

	file := &BackupFile{
		Filename:     filename,
		Path:         path,
		Date:         date,
		Size:         info.Size(),
		RecordCount:  recordCount,
		SessionCount: len(sessions),
		CreatedAt:    info.ModTime(),
		ModifiedAt:   info.ModTime(),
	}
	return file, sessions, stats.HasLoss(), nil
}

// SummarySalvageFallbacks 返回「摘要链路回退到打捞路径」的次数。
//
// ★ 这是 R82 的**可断言读数**：快路径失败才回退 ⇒
//
//	生产上 ≈ 坏文件数（53/100）
//	⇒ 若它暴涨 ⇒ 说明**好文件也开始读不出**了（更严重的信号）。
func (fr *FileReader) SummarySalvageFallbacks() int64 {
	return atomic.LoadInt64(&fr.summarySalvageFallbacks)
}
