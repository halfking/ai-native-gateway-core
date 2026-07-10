package dbdegradation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileWriter_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	fw := NewFileWriter(tmpDir)
	defer fw.Close()

	ctx := context.Background()
	sess := &session.Session{
		SessionID: "test-session-001",
		TenantID:  "default",
		APIKeyID:  1,
		CreatedAt: time.Now(),
	}
	stats := &session.SessionStats{
		TotalTurns:        5,
		TotalPromptTokens: 1000,
		TotalCostUSDCents: 100,
	}
	args := session.SnapshotArgs{
		StoppedAt:  time.Now(),
		StopReason: "manual",
	}

	// 写入快照
	err := fw.WriteSnapshot(ctx, sess, stats, args)
	require.NoError(t, err)

	// 验证文件存在
	backupDir := filepath.Join(tmpDir, "backups")
	files, err := os.ReadDir(backupDir)
	require.NoError(t, err)
	assert.Len(t, files, 1)

	// 验证文件名格式
	filename := files[0].Name()
	assert.Regexp(t, `^sessions-\d{4}-\d{2}-\d{2}\.jsonl\.gz$`, filename)

	// 验证文件权限
	info, err := files[0].Info()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestFileWriter_Rotation(t *testing.T) {
	tmpDir := t.TempDir()
	fw := NewFileWriter(tmpDir)
	fw.maxFileSize = 500 // 设置为 500 字节方便测试
	fw.maxDailyFiles = 20 // 增加限制避免超出
	defer fw.Close()

	ctx := context.Background()

	// 写入大量数据直到触发轮转
	for i := 0; i < 30; i++ {
		sess := &session.Session{
			SessionID: "test-session-" + string(rune(i)),
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
			// 添加大量数据使记录变大
			Annotation: string(make([]byte, 500)),
		}
		stats := &session.SessionStats{
			TotalTurns: 1,
		}
		args := session.SnapshotArgs{}

		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
		
		// 强制刷新以更新文件大小
		fw.Flush()
	}

	// 验证生成了多个文件
	backupDir := filepath.Join(tmpDir, "backups")
	files, err := os.ReadDir(backupDir)
	require.NoError(t, err)

	// 应该有多个文件（因为超过了 500 字节限制）
	assert.GreaterOrEqual(t, len(files), 2, "应该生成多个轮转文件")

	// 验证文件名包含序号
	hasSequence := false
	for _, f := range files {
		if match, _ := filepath.Match("sessions-*-*.jsonl.gz", f.Name()); match {
			hasSequence = true
			break
		}
	}
	assert.True(t, hasSequence, "应该有带序号的文件")
}

func TestFileWriter_MaxDailyFiles(t *testing.T) {
	tmpDir := t.TempDir()
	fw := NewFileWriter(tmpDir)
	fw.maxFileSize = 10    // 很小的文件大小
	fw.maxDailyFiles = 3   // 最多 3 个文件
	defer fw.Close()

	ctx := context.Background()

	// 尝试写入大量数据
	var lastErr error
	for i := 0; i < 100; i++ {
		sess := &session.Session{
			SessionID: "test-session-" + string(rune(i)),
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
			Annotation: string(make([]byte, 50)),
		}
		stats := &session.SessionStats{TotalTurns: 1}
		args := session.SnapshotArgs{}

		err := fw.WriteSnapshot(ctx, sess, stats, args)
		if err != nil {
			lastErr = err
			break
		}
	}

	// 应该触发日文件数限制错误
	assert.Error(t, lastErr)
	assert.Contains(t, lastErr.Error(), "daily file limit reached")
}

func TestFileWriter_Compression(t *testing.T) {
	tmpDir := t.TempDir()
	fw := NewFileWriter(tmpDir)
	defer fw.Close()

	ctx := context.Background()

	// 写入大量重复数据（压缩效果好）
	for i := 0; i < 10; i++ {
		sess := &session.Session{
			SessionID: "test-session-" + string(rune(i)),
			TenantID:  "default",
			APIKeyID:  1,
			CreatedAt: time.Now(),
			Annotation: string(make([]byte, 1000)), // 1KB 空字符（压缩效果好）
		}
		stats := &session.SessionStats{TotalTurns: 1}
		args := session.SnapshotArgs{}

		err := fw.WriteSnapshot(ctx, sess, stats, args)
		require.NoError(t, err)
	}

	// 强制刷新
	err := fw.Flush()
	require.NoError(t, err)

	// 获取统计信息
	stats := fw.GetStats()

	// 验证压缩率（应该小于 0.5，即压缩了 50% 以上）
	assert.Greater(t, stats.TotalBytes, int64(0), "未压缩字节数应该大于 0")
	assert.Greater(t, stats.CompressedBytes, int64(0), "压缩后字节数应该大于 0")
	assert.Less(t, stats.CompressionRatio, 0.5, "压缩率应该小于 0.5")
}

func TestFileWriter_Retry(t *testing.T) {
	tmpDir := t.TempDir()
	fw := NewFileWriter(tmpDir)
	fw.retryMax = 2
	defer fw.Close()

	ctx := context.Background()
	sess := &session.Session{
		SessionID: "test-session-retry",
		TenantID:  "default",
		APIKeyID:  1,
		CreatedAt: time.Now(),
	}
	stats := &session.SessionStats{TotalTurns: 1}
	args := session.SnapshotArgs{}

	// 正常写入应该成功
	err := fw.WriteSnapshot(ctx, sess, stats, args)
	require.NoError(t, err)

	// 关闭文件后再写入，会触发重试
	fw.Close()
	
	// 创建只读目录模拟失败
	backupDir := filepath.Join(tmpDir, "backups")
	os.Chmod(backupDir, 0400)
	defer os.Chmod(backupDir, 0700)

	err = fw.WriteSnapshot(ctx, sess, stats, args)
	assert.Error(t, err, "只读目录应该导致写入失败")
}

func TestFileWriter_DirectoryPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	fw := NewFileWriter(tmpDir)
	defer fw.Close()

	ctx := context.Background()
	sess := &session.Session{
		SessionID: "test-perm",
		TenantID:  "default",
		APIKeyID:  1,
		CreatedAt: time.Now(),
	}
	stats := &session.SessionStats{TotalTurns: 1}
	args := session.SnapshotArgs{}

	err := fw.WriteSnapshot(ctx, sess, stats, args)
	require.NoError(t, err)

	// 验证目录权限
	backupDir := filepath.Join(tmpDir, "backups")
	info, err := os.Stat(backupDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm(), "备份目录权限应为 0700")
}
