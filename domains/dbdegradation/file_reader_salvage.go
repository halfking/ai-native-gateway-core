package dbdegradation

// 2026-09-30 O5 收口：fallback 文件打捞读取。
//
// 现场形态（2026-09-30 local PG 停机演练实测，文件 sha256 cc3f33a7…d748c）：
// 蓝绿部署多进程 O_APPEND 追加同一日文件 + 容器硬杀跳过 gzip.Close，
// 产生「孤儿 gzip header 连排 + 中段一个完整 member + 截断尾巴」。
// gzip.NewReader 首个 member 即解码失败 → 整文件 0 条可恢复，PG 恢复后
// 即使运维手动触发恢复也拿不回任何记录。
//
// 打捞语义：逐字节扫描 gzip magic（1f 8b 08），对每个候选起点用「指数扩张
// + 二分」找最小完整 member 端点（tryMemberOK 对端点单调：E ≥ 真实端点才
// 可解码），member 内按行 JSON 解码，坏行跳过计数。回调交付语义与流式路径
// 一致：每条记录恰好一次（打捞路径是唯一读取路径，不存在二次扫描重复交付）。
//
// 归档护栏：LostBytes>0 或 SkippedLines>0 时调用方不得归档/删除原文件
// （文件中仍有不可恢复内容），见 GenericRecovery/Recovery 的守卫。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// gzipMinMemberBytes 是最小合法 gzip member 长度：10 字节 header（FLG=0 的
// 最小头）+ 空 deflate（0x03 0x00）+ 8 字节 trailer。
const gzipMinMemberBytes = 20

// salvageMaxCompressedBytes 是打捞路径载入内存的压缩字节上限。FileWriter
// 单文件上限 maxFileSize=100MB（压缩后），此处留余量；超限文件退回纯流式
// 读取（无打捞，保持既有行为），避免运维路径无界吃内存。
const salvageMaxCompressedBytes = 256 << 20

// salvageMaxLineBytes 是单行上限，与旧 bufio.Scanner 路径的 10MB 上限对齐
// （file_reader.go readGzipStream 的 maxScanTokenSize）。超过只可能是损坏
// 产物（交错写/垃圾字节）：跳过计数并丢弃到行尾。没有这个上限，打捞路径
// 的 pending 会随无换行的垃圾字节无界增长——旧路径在 10MB 报错终止，打捞
// 路径必须自己守住内存边界。
const salvageMaxLineBytes = 10 << 20

// SalvageStats 报告一次备份文件读取的打捞情况。零值表示文件干净走流式或
// 未启用打捞。SkippedLines 与 LostBytes 任一 >0 都意味着文件内容未完全
// 恢复，调用方不得归档/删除原文件。
type SalvageStats struct {
	// Members 成功解码出内容的 gzip member 数（含部分 member）。
	Members int
	// PartialMembers 中被截断/尾部损坏、仅部分内容可解码的 member 数。
	// 现场形态：容器硬杀跳过 gzip.Close，deflate 尾块不完整、trailer 缺失
	// 或被后续追加的 header 覆盖——数据已尽力解码，但无法验证完整性。
	PartialMembers int
	// SkippedLines 解码后无法 JSON 反序列化而跳过的行数。
	SkippedLines int
	// LostBytes 未能完整解码的压缩字节数（孤儿 header、损坏 member、
	// 垃圾间隔、部分 member 所在区域）。
	LostBytes int64
}

// HasLoss 报告本次读取是否丢失了内容（不可归档）。
func (s SalvageStats) HasLoss() bool {
	return s.SkippedLines > 0 || s.LostBytes > 0
}

// ReadRecordsWithStats 同 ReadRecords，并返回打捞统计供调用方决定归档。
// 回调可安全持有记录：BackupRecord 的 Payload 是 json.RawMessage，
// UnmarshalJSON 为拷贝语义（2026-09-30 复审勘误：此前注释声称「别名内部
// 缓冲、回调必须同步消费」是错的，旧 scanner 路径同样如此），并有
// TestSalvage_RetainedRecordPayloadIntact 钉死。
func (fr *FileReader) ReadRecordsWithStats(ctx context.Context, filename string, callback func(BackupRecord) error) (SalvageStats, error) {
	path, err := fr.backupPath(filename)
	if err != nil {
		return SalvageStats{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return SalvageStats{}, fmt.Errorf("stat file: %w", err)
	}

	// 常规文件（≤上限）：整文件载入压缩字节走打捞路径——干净文件等价
	// 流式读取，损坏文件自动打捞，回调始终单次交付。
	if info.Size() <= salvageMaxCompressedBytes {
		raw, err := os.ReadFile(path)
		if err != nil {
			return SalvageStats{}, fmt.Errorf("read file: %w", err)
		}
		return salvageGzipMembers(ctx, raw, callback)
	}

	// 超限：纯流式（原行为）。大文件（245 日 WAL 量级）本就按流处理，
	// 打捞收益不抵内存。
	slog.Warn("file reader: file exceeds salvage cap; streaming without salvage",
		"filename", filename, "size", info.Size(), "cap", salvageMaxCompressedBytes)
	file, err := os.Open(path)
	if err != nil {
		return SalvageStats{}, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()
	return SalvageStats{}, readGzipStream(ctx, file, callback)
}

// salvageGzipMembers 从（可能损坏的）gzip 字节流中逐 member 打捞记录。
// 完整 member 精确定位端点后整段解码；截断 member（硬杀跳过 gzip.Close）
// 宽容解码出已刷出的部分后重同步。全文件无任何可解码内容时返回错误；
// 部分可解码时返回 nil error，损失通过 SalvageStats 体现。
func salvageGzipMembers(ctx context.Context, raw []byte, callback func(BackupRecord) error) (SalvageStats, error) {
	var stats SalvageStats
	var covered int64
	salvager := newLineSalvager(callback, &stats)

	nextMagic := func(from int) int {
		next := from
		for next < len(raw) && !isGzipMagicAt(raw, next) {
			next++
		}
		return next
	}

	pos := 0
	for pos < len(raw) {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if !isGzipMagicAt(raw, pos) {
			pos = nextMagic(pos + 1)
			continue
		}
		// 先按完整 member 精确定位：端点偏大会跳过后续 member，偏小会
		// 重复扫描，都不能容忍；定位成功即整段可信。
		if end, ok := gzipMemberEnd(ctx, raw, pos); ok {
			stats.Members++
			covered += int64(end - pos)
			if err := streamMemberLines(ctx, raw[pos:end], salvager); err != nil {
				return stats, err
			}
			pos = end
			if err := salvager.flushTail(); err != nil {
				return stats, err
			}
			continue
		}
		// 截断/尾部损坏 member：宽容解码已刷出的部分（现场实测可回收
		// 停机窗口内的全部记录行），该区域整体计入丢失（完整性不可验证，
		// 归档护栏据此保留原文件），再重同步到下一个 magic。
		decodedAny, err := lenientMemberDecode(ctx, raw[pos:], salvager)
		if err != nil {
			return stats, err
		}
		next := nextMagic(pos + 1)
		if decodedAny {
			stats.Members++
			stats.PartialMembers++
			slog.Warn("file reader: salvage recovered partial gzip member",
				"offset", pos, "region_bytes", next-pos)
		}
		pos = next
		if err := salvager.flushTail(); err != nil {
			return stats, err
		}
	}
	stats.LostBytes = int64(len(raw)) - covered

	if stats.Members == 0 {
		return stats, fmt.Errorf("salvage: no decodable gzip members in %d bytes", len(raw))
	}
	if err := salvager.flushTail(); err != nil {
		return stats, err
	}
	return stats, nil
}

// isGzipMagicAt 报告 raw[pos:] 是否以 gzip magic（1f 8b 08）开头。
func isGzipMagicAt(raw []byte, pos int) bool {
	return pos+3 <= len(raw) && raw[pos] == 0x1f && raw[pos+1] == 0x8b && raw[pos+2] == 0x08
}

// gzipMemberEnd 返回以 raw[pos] 为起点的 gzip member 的真实端点（压缩流
// 含 trailer 的排他端点）。tryMemberOK 对端点单调（端点过小 unexpected
// EOF，端点 ≥ 真实值时 Multistream(false) 在 member 末尾返回 EOF 忽略
// 尾部），因此「指数扩张找上界 + 二分夹逼」能精确定位端点——定位偏大
// 会跳过后续 member，偏小会重复扫描，都不能容忍。
func gzipMemberEnd(ctx context.Context, raw []byte, pos int) (int, bool) {
	lo := pos + gzipMinMemberBytes
	if lo > len(raw) {
		return 0, false
	}
	hi := lo
	for {
		if err := ctx.Err(); err != nil {
			return 0, false
		}
		if tryMemberOK(raw[pos:hi]) {
			break
		}
		lo = hi + 1
		if hi >= len(raw) {
			return 0, false
		}
		hi *= 2
		if hi > len(raw) {
			hi = len(raw)
		}
	}
	for lo < hi {
		if err := ctx.Err(); err != nil {
			return 0, false
		}
		mid := (lo + hi) / 2
		if tryMemberOK(raw[pos:mid]) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return hi, true
}

// tryMemberOK 报告 b 是否（从 header 起步）恰好容纳一个可完整解码的
// gzip member。尾部多余字节被 Multistream(false) 忽略。
func tryMemberOK(b []byte) bool {
	if len(b) < gzipMinMemberBytes {
		return false
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return false
	}
	defer zr.Close()
	zr.Multistream(false)
	_, err = io.Copy(io.Discard, zr)
	return err == nil
}

// streamMemberLines 流式解码单个 member 并按行投递。
func streamMemberLines(ctx context.Context, member []byte, salvager *lineSalvager) error {
	zr, err := gzip.NewReader(bytes.NewReader(member))
	if err != nil {
		return fmt.Errorf("salvage: member header: %w", err)
	}
	defer zr.Close()
	zr.Multistream(false)

	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := zr.Read(buf)
		if n > 0 {
			if err := salvager.feed(buf[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// gzipMemberEnd 已验证 member 完整，正常不会走到；
			// 防御性兜底：报错让上层按失败处理，不静默吞。
			return fmt.Errorf("salvage: member decode: %w", err)
		}
	}
}

// lenientMemberDecode 宽容解码一个从 header 起步的 member：输出已刷入的
// 部分，遇截断/损坏即停（区别于 streamMemberLines 的严格模式）。返回是
// 否产出过任何 deflate 输出（用于区分「孤儿 header+垃圾」与「截断但
// 有数据」的 member）。
func lenientMemberDecode(ctx context.Context, member []byte, salvager *lineSalvager) (bool, error) {
	zr, err := gzip.NewReader(bytes.NewReader(member))
	if err != nil {
		return false, nil
	}
	defer zr.Close()
	zr.Multistream(false)

	decodedAny := false
	buf := make([]byte, 64*1024)
	var total int
	for {
		if err := ctx.Err(); err != nil {
			return decodedAny, err
		}
		n, rerr := zr.Read(buf)
		if n > 0 {
			decodedAny = true
			total += n
			if ferr := salvager.feed(buf[:n]); ferr != nil {
				return decodedAny, ferr
			}
		}
		if rerr == io.EOF {
			return decodedAny, nil
		}
		if rerr != nil {
			slog.Warn("file reader: salvage partial member decode stopped",
				"decoded_bytes", total, "error", rerr)
			return decodedAny, nil
		}
	}
}

// lineSalvager 跨 member 携带半行缓冲并按行 JSON 解码。
// 回调可安全持有记录（RawMessage 拷贝语义，见 ReadRecordsWithStats 勘误）。
type lineSalvager struct {
	callback func(BackupRecord) error
	stats    *SalvageStats
	pending  []byte
	// discarding 标记超长行丢弃模式：行已计损、pending 已清，剩余无换行
	// 字节整体丢弃直到下一个 \n（否则同一逻辑行的残余会被当新行反复累积）。
	discarding bool
}

func newLineSalvager(callback func(BackupRecord) error, stats *SalvageStats) *lineSalvager {
	return &lineSalvager{callback: callback, stats: stats}
}

func (s *lineSalvager) feed(chunk []byte) error {
	for len(chunk) > 0 {
		if s.discarding {
			idx := bytes.IndexByte(chunk, '\n')
			if idx < 0 {
				return nil
			}
			chunk = chunk[idx+1:]
			s.discarding = false
			continue
		}
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			s.pending = append(s.pending, chunk...)
			if len(s.pending) > salvageMaxLineBytes {
				s.stats.SkippedLines++
				slog.Warn("file reader: salvage dropped overlong line", "bytes", len(s.pending))
				s.pending = s.pending[:0]
				s.discarding = true
			}
			return nil
		}
		line := append(s.pending, chunk[:idx]...)
		s.pending = s.pending[:0]
		chunk = chunk[idx+1:]
		if len(line) > salvageMaxLineBytes {
			s.stats.SkippedLines++
			slog.Warn("file reader: salvage dropped overlong line", "bytes", len(line))
			continue
		}
		if err := s.dispatch(line); err != nil {
			s.pending = nil
			s.discarding = false
			return err
		}
	}
	return nil
}

// flushTail 在 member/文件边界结算残行并返回结算结果。残行丢弃语义：
// 下一个 member 是另一进程的独立流，残行与之无关，携带过去会把对方的
// 行粘成坏行（2026-09-30 实测截断 member 的残行吞掉下一 member 首条
// 记录）。回调错误必须上抛，不得静默吞。
func (s *lineSalvager) flushTail() error {
	if s.discarding {
		s.discarding = false
		s.pending = s.pending[:0]
		return nil
	}
	if len(s.pending) == 0 {
		return nil
	}
	line := s.pending
	s.pending = nil
	return s.dispatch(line)
}

func (s *lineSalvager) dispatch(line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var record BackupRecord
	if err := json.Unmarshal(line, &record); err != nil {
		s.stats.SkippedLines++
		slog.Warn("file reader: salvage skipped undecodable line",
			"bytes", len(line), "error", err)
		return nil
	}
	return s.callback(record)
}
