package dbdegradation

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileReader_ListBackupFiles(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 先写入一些测试数据
	fw := NewFileWriter(tmpDir)
	ctx := context.Background()
	
	for i := 0; i < 5; i++ {
		sess := &session.Session{
			SessionID: "test-" + string(rune(i)),
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
		}
		stats := &session.SessionStats{TotalTurns: 1}
		args := session.SnapshotArgs{}
		
		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
	}
	fw.Close()
	
	// 测试读取
	fr := NewFileReader(tmpDir)
	files, err := fr.ListBackupFiles(ctx)
	require.NoError(t, err)
	
	assert.Len(t, files, 1, "应该有 1 个备份文件")
	assert.Regexp(t, `^sessions-\d{4}-\d{2}-\d{2}\.jsonl\.gz$`, files[0].Filename)
}

func TestFileReader_GetFileSummary(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 写入测试数据
	fw := NewFileWriter(tmpDir)
	ctx := context.Background()
	
	sessionIDs := []string{"sess-001", "sess-002", "sess-003"}
	for _, sid := range sessionIDs {
		sess := &session.Session{
			SessionID: sid,
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
		}
		stats := &session.SessionStats{TotalTurns: 1}
		args := session.SnapshotArgs{}
		
		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
	}
	fw.Close()
	
	// 获取文件摘要
	fr := NewFileReader(tmpDir)
	files, err := fr.ListBackupFiles(ctx)
	require.NoError(t, err)
	require.Len(t, files, 1)
	
	summary, err := fr.GetFileSummary(ctx, files[0].Filename)
	require.NoError(t, err)
	
	assert.Equal(t, 3, summary.RecordCount, "应该有 3 条记录")
	assert.Equal(t, 3, summary.SessionCount, "应该有 3 个会话")
	assert.Greater(t, summary.Size, int64(0), "文件大小应该大于 0")
}

func TestFileReader_ReadRecords(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 写入测试数据
	fw := NewFileWriter(tmpDir)
	ctx := context.Background()
	
	testSessions := []*session.Session{
		{SessionID: "sess-001", TenantID: "default", APIKeyID: 1, CreatedAt: time.Now()},
		{SessionID: "sess-002", TenantID: "default", APIKeyID: 2, CreatedAt: time.Now()},
	}
	
	for _, sess := range testSessions {
		stats := &session.SessionStats{TotalTurns: 1}
		args := session.SnapshotArgs{}
		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
	}
	fw.Close()
	
	// 读取记录
	fr := NewFileReader(tmpDir)
	files, err := fr.ListBackupFiles(ctx)
	require.NoError(t, err)
	require.Len(t, files, 1)
	
	var records []BackupRecord
	err = fr.ReadRecords(ctx, files[0].Filename, func(record BackupRecord) error {
		records = append(records, record)
		return nil
	})
	require.NoError(t, err)
	
	assert.Len(t, records, 2, "应该读取到 2 条记录")
	
	// 验证记录内容
	for _, record := range records {
		assert.Equal(t, "snapshot", record.Type)
		assert.NotEmpty(t, record.SessionID)
		assert.NotNil(t, record.Session)
		assert.NotNil(t, record.Stats)
	}
}

func TestFileReader_ValidateFile(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 写入有效数据
	fw := NewFileWriter(tmpDir)
	ctx := context.Background()
	
	sess := &session.Session{
		SessionID: "test-valid",
		TenantID:  "default",
		APIKeyID:  1,
		CreatedAt: time.Now(),
	}
	stats := &session.SessionStats{TotalTurns: 1}
	args := session.SnapshotArgs{}
	
	err := fw.WriteSnapshot(ctx, sess, stats, args)
	require.NoError(t, err)
	fw.Close()
	
	// 验证文件
	fr := NewFileReader(tmpDir)
	files, err := fr.ListBackupFiles(ctx)
	require.NoError(t, err)
	require.Len(t, files, 1)
	
	err = fr.ValidateFile(ctx, files[0].Filename)
	assert.NoError(t, err, "有效文件应该通过验证")
}

func TestFileReader_GetBackupSummary(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 写入多个会话
	fw := NewFileWriter(tmpDir)
	ctx := context.Background()
	
	for i := 0; i < 10; i++ {
		sess := &session.Session{
			SessionID: "test-" + string(rune(i)),
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
		}
		stats := &session.SessionStats{TotalTurns: int64(i + 1)}
		args := session.SnapshotArgs{}
		
		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
	}
	fw.Close()
	
	// 获取汇总信息
	fr := NewFileReader(tmpDir)
	summary, err := fr.GetBackupSummary(ctx)
	require.NoError(t, err)
	
	assert.Equal(t, 1, summary.TotalFiles)
	assert.Equal(t, 10, summary.TotalRecords)
	assert.Equal(t, 10, summary.TotalSessions)
	assert.Greater(t, summary.TotalSize, int64(0))
	assert.NotEmpty(t, summary.DateRange)
}

func TestFileReader_CacheInvalidation(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 写入数据
	fw := NewFileWriter(tmpDir)
	ctx := context.Background()
	
	sess := &session.Session{
		SessionID: "test-cache",
		TenantID:  "default",
		APIKeyID:  1,
		CreatedAt: time.Now(),
	}
	stats := &session.SessionStats{TotalTurns: 1}
	args := session.SnapshotArgs{}
	
	err := fw.WriteSnapshot(ctx, sess, stats, args)
	require.NoError(t, err)
	fw.Close()
	
	// 第一次读取（缓存）
	fr := NewFileReader(tmpDir)
	files, err := fr.ListBackupFiles(ctx)
	require.NoError(t, err)
	
	summary1, err := fr.GetFileSummary(ctx, files[0].Filename)
	require.NoError(t, err)
	
	// 清除缓存
	fr.InvalidateCache()
	
	// 第二次读取（重新读取文件）
	summary2, err := fr.GetFileSummary(ctx, files[0].Filename)
	require.NoError(t, err)
	
	// 两次结果应该一致
	assert.Equal(t, summary1.RecordCount, summary2.RecordCount)
	assert.Equal(t, summary1.SessionCount, summary2.SessionCount)
}

func TestFileReader_WithRotatedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 写入数据触发轮转
	fw := NewFileWriter(tmpDir)
	fw.maxFileSize = 500 // 500 字节触发轮转
	ctx := context.Background()
	
	for i := 0; i < 30; i++ {
		sess := &session.Session{
			SessionID: "test-" + string(rune(i)),
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
			Annotation: string(make([]byte, 500)),
		}
		stats := &session.SessionStats{TotalTurns: 1}
		args := session.SnapshotArgs{}
		
		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
		
		// 强制刷新以更新文件大小
		fw.Flush()
	}
	fw.Close()
	
	// 读取所有文件
	fr := NewFileReader(tmpDir)
	files, err := fr.ListBackupFiles(ctx)
	require.NoError(t, err)
	
	assert.GreaterOrEqual(t, len(files), 2, "应该有多个轮转文件")
	
	// 验证每个文件都可以读取
	totalRecords := 0
	for _, file := range files {
		err := fr.ReadRecords(ctx, file.Filename, func(record BackupRecord) error {
			totalRecords++
			return nil
		})
		require.NoError(t, err)
	}
	
	assert.Equal(t, 30, totalRecords, "应该读取到所有记录")
}
