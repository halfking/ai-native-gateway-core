package dbdegradation

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileReader 文件备份读取器（支持 gzip 解压）
type FileReader struct {
	baseDir   string
	cacheMu   sync.RWMutex
	cache     map[string]*BackupFile // 文件元数据缓存
	cacheTTL  time.Duration
	cacheTime time.Time
}

// NewFileReader 创建文件读取器
func NewFileReader(baseDir string) *FileReader {
	return &FileReader{
		baseDir:  baseDir,
		cache:    make(map[string]*BackupFile),
		cacheTTL: 5 * time.Minute,
	}
}

// ListBackupFiles 列出所有备份文件
func (fr *FileReader) ListBackupFiles(ctx context.Context) ([]BackupFile, error) {
	backupDir := filepath.Join(fr.baseDir, "backups")

	// 检查目录是否存在
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		return []BackupFile{}, nil
	}

	// 读取目录
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return nil, fmt.Errorf("read backup dir: %w", err)
	}

	var files []BackupFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 只处理 sessions-*.jsonl.gz 文件（支持带序号的文件）
		name := entry.Name()
		if !strings.HasPrefix(name, "sessions-") || !strings.HasSuffix(name, ".jsonl.gz") {
			continue
		}

		// 获取文件信息
		info, err := entry.Info()
		if err != nil {
			slog.Warn("file reader: failed to get file info", "filename", name, "error", err)
			continue
		}

		// 提取日期
		date := strings.TrimPrefix(name, "sessions-")
		date = strings.TrimSuffix(date, ".jsonl.gz")

		file := BackupFile{
			Filename:   name,
			Path:       filepath.Join(backupDir, name),
			Date:       date,
			Size:       info.Size(),
			CreatedAt:  info.ModTime(), // 使用修改时间作为创建时间
			ModifiedAt: info.ModTime(),
		}

		files = append(files, file)
	}

	// 按日期排序（最新的在前）
	sort.Slice(files, func(i, j int) bool {
		return files[i].Date > files[j].Date
	})

	return files, nil
}

// GetFileSummary 获取单个文件的统计信息
func (fr *FileReader) GetFileSummary(ctx context.Context, filename string) (*BackupFile, error) {
	// 检查缓存
	fr.cacheMu.RLock()
	if cached, ok := fr.cache[filename]; ok {
		if time.Since(fr.cacheTime) < fr.cacheTTL {
			fr.cacheMu.RUnlock()
			return cached, nil
		}
	}
	fr.cacheMu.RUnlock()

	backupDir := filepath.Join(fr.baseDir, "backups")
	path := filepath.Join(backupDir, filename)

	// 获取文件信息
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	// 提取日期
	date := strings.TrimPrefix(filename, "sessions-")
	date = strings.TrimSuffix(date, ".jsonl.gz")

	file := &BackupFile{
		Filename:   filename,
		Path:       path,
		Date:       date,
		Size:       info.Size(),
		CreatedAt:  info.ModTime(),
		ModifiedAt: info.ModTime(),
	}

	// 统计记录数和会话数
	sessionSet := make(map[string]struct{})
	recordCount := 0

	err = fr.ReadRecords(ctx, filename, func(record BackupRecord) error {
		recordCount++
		sessionSet[record.SessionID] = struct{}{}
		return nil
	})

	if err != nil {
		slog.Warn("file reader: failed to count records", "filename", filename, "error", err)
		// 不返回错误，只是统计信息不完整
	}

	file.RecordCount = recordCount
	file.SessionCount = len(sessionSet)

	// 更新缓存
	fr.cacheMu.Lock()
	fr.cache[filename] = file
	fr.cacheTime = time.Now()
	fr.cacheMu.Unlock()

	return file, nil
}

// GetBackupSummary 获取所有备份的汇总信息
func (fr *FileReader) GetBackupSummary(ctx context.Context) (*BackupSummary, error) {
	files, err := fr.ListBackupFiles(ctx)
	if err != nil {
		return nil, err
	}

	summary := &BackupSummary{
		Files: make([]BackupFile, 0, len(files)),
	}

	allSessions := make(map[string]struct{})
	var dates []string

	for _, file := range files {
		// 获取详细信息（包含统计）
		detailed, err := fr.GetFileSummary(ctx, file.Filename)
		if err != nil {
			slog.Warn("file reader: failed to get file summary", "filename", file.Filename, "error", err)
			summary.Files = append(summary.Files, file)
			continue
		}

		summary.Files = append(summary.Files, *detailed)
		summary.TotalRecords += detailed.RecordCount
		summary.TotalSize += detailed.Size
		dates = append(dates, detailed.Date)

		// 统计唯一会话数（需要读取文件）
		fr.ReadRecords(ctx, file.Filename, func(record BackupRecord) error {
			allSessions[record.SessionID] = struct{}{}
			return nil
		})
	}

	summary.TotalFiles = len(files)
	summary.TotalSessions = len(allSessions)

	// 计算日期范围
	if len(dates) > 0 {
		sort.Strings(dates)
		if dates[0] == dates[len(dates)-1] {
			summary.DateRange = dates[0]
		} else {
			summary.DateRange = fmt.Sprintf("%s to %s", dates[0], dates[len(dates)-1])
		}
	}

	return summary, nil
}

// ReadRecords 流式读取记录并回调处理
func (fr *FileReader) ReadRecords(ctx context.Context, filename string, callback func(BackupRecord) error) error {
	backupDir := filepath.Join(fr.baseDir, "backups")
	path := filepath.Join(backupDir, filename)

	// 打开文件
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// 创建 gzip reader
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzipReader.Close()

	// 逐行读取（JSON Lines 格式）
	scanner := bufio.NewScanner(gzipReader)
	// 增加缓冲区大小以处理大记录
	const maxScanTokenSize = 10 * 1024 * 1024 // 10MB
	buf := make([]byte, maxScanTokenSize)
	scanner.Buffer(buf, maxScanTokenSize)

	lineNum := 0
	for scanner.Scan() {
		lineNum++

		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record BackupRecord
		if err := json.Unmarshal(line, &record); err != nil {
			slog.Warn("file reader: failed to unmarshal record",
				"filename", filename,
				"line", lineNum,
				"error", err,
			)
			continue // 跳过损坏的记录
		}

		if err := callback(record); err != nil {
			return fmt.Errorf("callback error at line %d: %w", lineNum, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan file: %w", err)
	}

	return nil
}

// ValidateFile 验证文件格式完整性
func (fr *FileReader) ValidateFile(ctx context.Context, filename string) error {
	recordCount := 0
	err := fr.ReadRecords(ctx, filename, func(record BackupRecord) error {
		recordCount++

		// 基本验证
		if record.SessionID == "" {
			return fmt.Errorf("empty session_id in record")
		}
		if record.Type != "snapshot" && record.Type != "rotation" {
			return fmt.Errorf("invalid record type: %s", record.Type)
		}
		if record.Type == "snapshot" && record.Session == nil {
			return fmt.Errorf("snapshot record missing session data")
		}
		if record.Type == "rotation" && record.Rotation == nil {
			return fmt.Errorf("rotation record missing rotation data")
		}

		return nil
	})

	if err != nil {
		return err
	}

	if recordCount == 0 {
		return fmt.Errorf("file is empty")
	}

	slog.Info("file reader: validation passed",
		"filename", filename,
		"records", recordCount,
	)

	return nil
}

// InvalidateCache 清除缓存
func (fr *FileReader) InvalidateCache() {
	fr.cacheMu.Lock()
	defer fr.cacheMu.Unlock()
	fr.cache = make(map[string]*BackupFile)
}
