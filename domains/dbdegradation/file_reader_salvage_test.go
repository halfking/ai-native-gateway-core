package dbdegradation

// 2026-09-30 O5 收口：fallback 文件打捞读取测试。
// 损坏形态取自 2026-09-30 local PG 停机演练实测文件
// （多进程 O_APPEND 追加 + 容器硬杀跳过 gzip.Close）。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gzipMember 用 records（JSONL 行）构造一个完整 gzip member。
func gzipMember(t *testing.T, records ...map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, rec := range records {
		line, err := json.Marshal(rec)
		require.NoError(t, err)
		_, err = zw.Write(append(line, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func requestLogRecord(requestID string) map[string]any {
	return map[string]any{
		"type":       "request_log",
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
		"record_key": requestID + ":insert",
		"payload":    json.RawMessage(fmt.Sprintf(`{"request_id":%q,"op":"insert"}`, requestID)),
	}
}

// writeBackupFile 在 tmpDir/backups 下落一个手工构造的备份文件。
func writeBackupFile(t *testing.T, tmpDir, filename string, data []byte) {
	t.Helper()
	backupDir := filepath.Join(tmpDir, "backups")
	require.NoError(t, os.MkdirAll(backupDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, filename), data, 0o600))
}

// collectRequests 经 FileReader 读取并返回全部 request_log 的 request_id 与统计。
func collectRequests(t *testing.T, fr *FileReader, filename string) ([]string, SalvageStats, error) {
	t.Helper()
	var ids []string
	stats, err := fr.ReadRecordsWithStats(context.Background(), filename, func(record BackupRecord) error {
		if record.Type == "request_log" {
			var payload struct {
				RequestID string `json:"request_id"`
			}
			require.NoError(t, json.Unmarshal(record.Payload, &payload))
			ids = append(ids, payload.RequestID)
		}
		return nil
	})
	return ids, stats, err
}

func TestSalvage_CleanFileSingleMember(t *testing.T) {
	tmpDir := t.TempDir()
	member := gzipMember(t, requestLogRecord("req-1"), requestLogRecord("req-2"))
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", member)

	ids, stats, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.NoError(t, err)
	assert.Equal(t, []string{"req-1", "req-2"}, ids)
	assert.Equal(t, 1, stats.Members)
	assert.False(t, stats.HasLoss(), "干净文件无损失")
}

func TestSalvage_ConcatenatedCleanMembers(t *testing.T) {
	// 多进程各自完整 Close 后追加：合法多 member 串联，全部可读。
	tmpDir := t.TempDir()
	var raw bytes.Buffer
	raw.Write(gzipMember(t, requestLogRecord("req-a")))
	raw.Write(gzipMember(t, requestLogRecord("req-b"), requestLogRecord("req-c")))
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", raw.Bytes())

	ids, stats, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.NoError(t, err)
	assert.Equal(t, []string{"req-a", "req-b", "req-c"}, ids)
	assert.Equal(t, 2, stats.Members)
	assert.False(t, stats.HasLoss())
}

func TestSalvage_OrphanHeaderPrefixThenValidMember(t *testing.T) {
	// 现场形态：文件头连排孤儿 gzip header（进程写完 header 即被顶掉），
	// 跟一段垃圾，然后一个完整 member。
	tmpDir := t.TempDir()
	orphan := gzipMember(t) // 取其 10 字节 header 当孤儿头
	garbage := bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 8)
	valid := gzipMember(t, requestLogRecord("req-live"))
	var raw bytes.Buffer
	raw.Write(orphan[:10])
	raw.Write(orphan[:10])
	raw.Write(garbage)
	raw.Write(valid)
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", raw.Bytes())

	ids, stats, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.NoError(t, err)
	assert.Equal(t, []string{"req-live"}, ids, "应打捞出完整 member 中的记录")
	assert.Equal(t, 1, stats.Members)
	assert.Equal(t, int64(10+10+len(garbage)), stats.LostBytes, "孤儿 header 与垃圾字节计入丢失")
}

func TestSalvage_TruncatedMemberThenValidMember(t *testing.T) {
	// 现场形态：硬杀导致前一 member 截断（无 trailer），后续进程追加完整
	// member。截断部分按丢失计，完整 member 打捞；截断 member 中已刷出
	// 的行也尽力回收（宽容解码），故只断言必得 req-saved。
	tmpDir := t.TempDir()
	full := gzipMember(t, requestLogRecord("req-lost"), requestLogRecord("req-lost-2"))
	truncated := full[:len(full)-14] // 砍掉 trailer 与部分 deflate
	valid := gzipMember(t, requestLogRecord("req-saved"))
	var raw bytes.Buffer
	raw.Write(truncated)
	raw.Write(valid)
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", raw.Bytes())

	ids, stats, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.NoError(t, err)
	assert.Contains(t, ids, "req-saved")
	assert.Equal(t, int64(len(truncated)), stats.LostBytes)
	assert.True(t, stats.HasLoss())
}

func TestSalvage_TruncatedMemberOnly_LiveShape(t *testing.T) {
	// 2026-09-30 local 演练实测形态：孤儿 header 连排 + 截断 member（无
	// trailer，尾部被追加 header 覆盖）+ 尾部孤儿 header——文件中不存在
	// 任何完整 member，但截断 member 内已刷出的记录必须打捞回来。
	tmpDir := t.TempDir()
	orphan := gzipMember(t)[:10]

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, id := range []string{"req-live-1", "req-live-2", "req-live-3"} {
		line, err := json.Marshal(requestLogRecord(id))
		require.NoError(t, err)
		_, err = zw.Write(append(line, '\n'))
		require.NoError(t, err)
	}
	// Flush 强制 deflate 刷出（对应现场大文件自然跨块刷出）；不 Close：
	// 模拟进程被硬杀，deflate 尾块与 trailer 缺失。
	require.NoError(t, zw.Flush())
	truncatedMember := buf.Bytes()

	var raw bytes.Buffer
	for i := 0; i < 5; i++ {
		raw.Write(orphan)
	}
	raw.Write(truncatedMember)
	for i := 0; i < 3; i++ {
		raw.Write(orphan)
	}
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", raw.Bytes())

	ids, stats, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"req-live-1", "req-live-2", "req-live-3"}, ids,
		"截断 member 中已刷出的记录应全部打捞")
	assert.Equal(t, 1, stats.Members)
	assert.Equal(t, 1, stats.PartialMembers)
	assert.True(t, stats.HasLoss(), "截断 member 区域完整性不可验证，必须计损")
}

func TestSalvage_CorruptLineInsideMember(t *testing.T) {
	// member 内坏行（交错写残行）跳过计数，其余记录照常交付。
	tmpDir := t.TempDir()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, line := range []string{
		`{"type":"request_log","record_key":"req-good-1:insert","payload":{"request_id":"req-good-1"}}`,
		"this is not json \xff\xfe",
		`{"type":"request_log","record_key":"req-good-2:insert","payload":{"request_id":"req-good-2"}}`,
	} {
		_, err := zw.Write(append([]byte(line), '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", buf.Bytes())

	ids, stats, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.NoError(t, err)
	assert.Equal(t, []string{"req-good-1", "req-good-2"}, ids)
	assert.Equal(t, 1, stats.SkippedLines)
	assert.True(t, stats.HasLoss())
}

func TestSalvage_PureGarbageErrors(t *testing.T) {
	tmpDir := t.TempDir()
	writeBackupFile(t, tmpDir, "sessions-2026-09-30.jsonl.gz", bytes.Repeat([]byte{0x00}, 128))

	_, _, err := collectRequests(t, NewFileReader(tmpDir), "sessions-2026-09-30.jsonl.gz")
	require.Error(t, err, "无任何可解码 member 应报错")
}

func TestGenericRecovery_SalvageLossBlocksArchive(t *testing.T) {
	// 归档护栏：打捞有损失时恢复完成但文件必须保留。
	tmpDir := t.TempDir()
	orphan := gzipMember(t)[:10]
	valid := gzipMember(t, requestLogRecord("req-keep"))
	var raw bytes.Buffer
	raw.Write(orphan)
	raw.Write(valid)
	filename := "sessions-2026-09-30.jsonl.gz"
	path := filepath.Join(tmpDir, "backups", filename)
	writeBackupFile(t, tmpDir, filename, raw.Bytes())

	var replayed []string
	gr := NewGenericRecovery(NewFileReader(tmpDir), func(ctx context.Context, record BackupRecord) error {
		if record.Type == "request_log" {
			replayed = append(replayed, record.RecordKey)
		}
		return nil
	})

	taskID, err := gr.Recover(context.Background(), filename, true)
	require.NoError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, ok := gr.Status(taskID)
		require.True(t, ok)
		if task.Status == "completed" || task.Status == "completed_with_errors" || task.Status == "failed" {
			assert.Equal(t, "completed_with_errors", task.Status)
			assert.Contains(t, task.Error, "file kept")
			break
		}
		require.Less(t, time.Now(), deadline, "recovery task did not finish")
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, []string{"req-keep:insert"}, replayed)
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "有损失时文件不得归档/删除")
	_, statErr = os.Stat(filepath.Join(tmpDir, "archive", filename))
	require.True(t, os.IsNotExist(statErr), "archive 目录不应出现该文件")
}

func TestGenericRecovery_CleanFileArchives(t *testing.T) {
	// 对照组：干净 request_log 文件恢复后正常归档。
	tmpDir := t.TempDir()
	filename := "sessions-2026-09-30.jsonl.gz"
	path := filepath.Join(tmpDir, "backups", filename)
	writeBackupFile(t, tmpDir, filename, gzipMember(t, requestLogRecord("req-clean")))

	gr := NewGenericRecovery(NewFileReader(tmpDir), func(ctx context.Context, record BackupRecord) error {
		return nil
	})
	taskID, err := gr.Recover(context.Background(), filename, true)
	require.NoError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, ok := gr.Status(taskID)
		require.True(t, ok)
		if task.Status == "completed" || task.Status == "completed_with_errors" || task.Status == "failed" {
			assert.Equal(t, "completed", task.Status)
			break
		}
		require.Less(t, time.Now(), deadline, "recovery task did not finish")
		time.Sleep(10 * time.Millisecond)
	}
	_, statErr := os.Stat(filepath.Join(tmpDir, "archive", filename))
	require.NoError(t, statErr, "干净文件应归档到 archive/")
	_, statErr = os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}
