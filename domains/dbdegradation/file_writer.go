package dbdegradation

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// FileWriter 文件备份写入器（支持 gzip 压缩）
type FileWriter struct {
	baseDir       string
	mu            sync.Mutex
	currentFile   *os.File
	currentGzip   *gzip.Writer
	currentDate   string
	currentSeq    int          // 当日文件序号（用于轮转）
	encoder       *json.Encoder
	stats         atomic.Value // Stats
	retryMax      int
	maxFileSize   int64        // 单文件最大大小（字节），0 表示无限制
	maxDailyFiles int          // 单日最大文件数，0 表示无限制
}

// NewFileWriter 创建文件写入器
func NewFileWriter(baseDir string) *FileWriter {
	fw := &FileWriter{
		baseDir:       baseDir,
		retryMax:      3,
		maxFileSize:   100 * 1024 * 1024, // 100MB（压缩后）
		maxDailyFiles: 10,                 // 单日最多 10 个文件
	}
	fw.stats.Store(Stats{})
	return fw
}

// WriteSnapshot 写入会话快照
func (fw *FileWriter) WriteSnapshot(ctx context.Context, sess *session.Session, stats *session.SessionStats, args session.SnapshotArgs) error {
	record := BackupRecord{
		Type:      "snapshot",
		Timestamp: time.Now(),
		SessionID: sess.SessionID,
		Session:   sess,
		Stats:     stats,
		StopArgs:  &args,
	}
	return fw.writeRecord(record)
}

// WriteRotation 写入凭据轮换记录
func (fw *FileWriter) WriteRotation(ctx context.Context, sessionID string, rotation *session.CredRotationEntry) error {
	record := BackupRecord{
		Type:      "rotation",
		Timestamp: time.Now(),
		SessionID: sessionID,
		Rotation:  rotation,
	}
	return fw.writeRecord(record)
}

// writeRecord 写入记录（带重试）
func (fw *FileWriter) writeRecord(record BackupRecord) error {
	var lastErr error
	for i := 0; i < fw.retryMax; i++ {
		if err := fw.writeRecordOnce(record); err != nil {
			lastErr = err
			slog.Warn("file writer: write failed, retrying",
				"attempt", i+1,
				"max", fw.retryMax,
				"error", err,
			)
			time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
			continue
		}
		return nil
	}
	slog.Error("file writer: write failed after retries",
		"error", lastErr,
		"session_id", record.SessionID,
		"type", record.Type,
	)
	return lastErr
}

// writeRecordOnce 写入记录（单次尝试）
func (fw *FileWriter) writeRecordOnce(record BackupRecord) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	date := time.Now().Format("2006-01-02")

	// 检查是否需要轮转
	if fw.currentFile != nil && fw.currentDate == date && fw.maxFileSize > 0 {
		if fileInfo, err := fw.currentFile.Stat(); err == nil {
			if fileInfo.Size() >= fw.maxFileSize {
				// 文件过大，轮转到新序号
				if err := fw.rotateFile(date); err != nil {
					return fmt.Errorf("rotate file: %w", err)
				}
			}
		}
	}

	// 获取或创建文件
	if err := fw.ensureFile(date); err != nil {
		return fmt.Errorf("ensure file: %w", err)
	}

	// 序列化记录
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	data = append(data, '\n') // JSON Lines 格式

	// 写入 gzip
	n, err := fw.currentGzip.Write(data)
	if err != nil {
		return fmt.Errorf("write to gzip: %w", err)
	}

	// 更新统计
	stats := fw.stats.Load().(Stats)
	stats.TotalRecords++
	stats.TotalBytes += int64(len(data))
	stats.LastWriteTime = time.Now()

	// 获取压缩后的文件大小
	if fileInfo, err := fw.currentFile.Stat(); err == nil {
		stats.CurrentFileSize = fileInfo.Size()
		stats.CompressedBytes = fileInfo.Size()
		if stats.TotalBytes > 0 {
			stats.CompressionRatio = float64(stats.CompressedBytes) / float64(stats.TotalBytes)
		}
	}

	fw.stats.Store(stats)

	slog.Debug("file writer: record written",
		"session_id", record.SessionID,
		"type", record.Type,
		"bytes_uncompressed", len(data),
		"bytes_written", n,
	)

	return nil
}

// rotateFile 轮转到新文件（当前文件过大时）
func (fw *FileWriter) rotateFile(date string) error {
	if fw.currentDate != date {
		fw.currentSeq = 0
	} else {
		fw.currentSeq++
	}

	if fw.maxDailyFiles > 0 && fw.currentSeq >= fw.maxDailyFiles {
		return fmt.Errorf("daily file limit reached (%d files)", fw.maxDailyFiles)
	}

	// 关闭当前文件
	if err := fw.closeCurrentFile(); err != nil {
		slog.Warn("file writer: failed to close old file for rotation", "error", err)
	}

	slog.Info("file writer: rotating to new file",
		"date", date,
		"sequence", fw.currentSeq,
	)

	return nil
}

// ensureFile 确保文件存在并打开
func (fw *FileWriter) ensureFile(date string) error {
	// 如果是同一天且文件已打开，直接返回
	if fw.currentDate == date && fw.currentFile != nil {
		return nil
	}

	// 切换到新日期，重置序号
	if fw.currentDate != date {
		fw.currentSeq = 0
	}

	// 关闭旧文件
	if fw.currentFile != nil {
		if err := fw.closeCurrentFile(); err != nil {
			slog.Warn("file writer: failed to close old file", "error", err)
		}
	}

	// 创建备份目录（修改权限为 0700 - 仅 owner 可访问）
	backupDir := filepath.Join(fw.baseDir, "backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	// 生成文件名（支持序号）
	var filename string
	if fw.currentSeq == 0 {
		filename = fmt.Sprintf("sessions-%s.jsonl.gz", date)
	} else {
		filename = fmt.Sprintf("sessions-%s-%02d.jsonl.gz", date, fw.currentSeq)
	}
	filePath := filepath.Join(backupDir, filename)

	// 打开新文件（修改权限为 0600 - 仅 owner 可读写）
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}

	// 创建 gzip writer
	gzipWriter := gzip.NewWriter(file)

	fw.currentFile = file
	fw.currentGzip = gzipWriter
	fw.currentDate = date

	slog.Info("file writer: opened new backup file",
		"filename", filename,
		"path", filePath,
	)

	return nil
}

// closeCurrentFile 关闭当前文件
func (fw *FileWriter) closeCurrentFile() error {
	if fw.currentGzip != nil {
		if err := fw.currentGzip.Close(); err != nil {
			return fmt.Errorf("close gzip: %w", err)
		}
		fw.currentGzip = nil
	}
	if fw.currentFile != nil {
		if err := fw.currentFile.Close(); err != nil {
			return fmt.Errorf("close file: %w", err)
		}
		fw.currentFile = nil
	}
	return nil
}

// Flush 强制刷新缓冲区
func (fw *FileWriter) Flush() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	if fw.currentGzip != nil {
		if err := fw.currentGzip.Flush(); err != nil {
			return fmt.Errorf("flush gzip: %w", err)
		}
	}
	if fw.currentFile != nil {
		if err := fw.currentFile.Sync(); err != nil {
			return fmt.Errorf("sync file: %w", err)
		}
	}
	return nil
}

// Close 关闭写入器
func (fw *FileWriter) Close() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return fw.closeCurrentFile()
}

// GetStats 获取统计信息
func (fw *FileWriter) GetStats() Stats {
	return fw.stats.Load().(Stats)
}
