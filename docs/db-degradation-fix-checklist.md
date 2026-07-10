# 数据库降级模块修复清单

**基于审计报告**: `db-degradation-audit-report.md`  
**修复时间估算**: 1-2 天（Critical + High 优先级）

---

## 🔴 Critical - 必须立即修复（阻塞上线）

### ✅ C1. 路径遍历漏洞修复

**文件**: `admin/db_degradation_handlers.go`

**修改位置**: 71-89, 93-124, 179-202 行

**新增代码**:

```go
package admin

import (
	"regexp"
	"strings"
)

var backupFilenamePattern = regexp.MustCompile(`^sessions-\d{4}-\d{2}-\d{2}\.jsonl\.gz$`)

// validateBackupFilename 验证备份文件名格式
func validateBackupFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}
	
	// 检查路径遍历字符
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		return fmt.Errorf("filename contains invalid characters")
	}
	
	// 验证格式: sessions-YYYY-MM-DD.jsonl.gz
	if !backupFilenamePattern.MatchString(filename) {
		return fmt.Errorf("filename must match format: sessions-YYYY-MM-DD.jsonl.gz")
	}
	
	return nil
}
```

**修改后的 handler**:

```go
// handleGetBackupFile 获取单个备份文件详情
func (h *Handler) handleGetBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.fileReader == nil {
		writeError(w, http.StatusServiceUnavailable, "file reader not configured")
		return
	}

	filename := r.PathValue("filename")
	if err := validateBackupFilename(filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	file, err := h.fileReader.GetFileSummary(r.Context(), filename)
	if err != nil {
		slog.Error("failed to get backup file", "filename", filename, "error", err)
		writeError(w, http.StatusNotFound, "backup file not found")
		return
	}

	writeJSON(w, http.StatusOK, file)
}

// handleRecoverBackupFile 恢复单个备份文件
func (h *Handler) handleRecoverBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.recovery == nil {
		writeError(w, http.StatusServiceUnavailable, "recovery not configured")
		return
	}

	filename := r.PathValue("filename")
	if err := validateBackupFilename(filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req struct {
		DeleteAfter bool `json:"delete_after"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	taskID, err := h.recovery.RecoverFile(r.Context(), filename, req.DeleteAfter)
	if err != nil {
		slog.Error("failed to start recovery", "filename", filename, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start recovery task")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id": taskID,
		"status":  "pending",
	})
}

// handleValidateBackupFile 验证备份文件
func (h *Handler) handleValidateBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.fileReader == nil {
		writeError(w, http.StatusServiceUnavailable, "file reader not configured")
		return
	}

	filename := r.PathValue("filename")
	if err := validateBackupFilename(filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.fileReader.ValidateFile(r.Context(), filename); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"valid": false,
			"error": "file validation failed",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid": true,
	})
}
```

**测试验证**:
```bash
# 测试路径遍历攻击
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/api/admin/backups/../etc/passwd.gz"
# 预期: 400 Bad Request - "filename contains invalid characters"

curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/api/admin/backups/sessions-2026-07-10.jsonl.gz"
# 预期: 200 OK - 正常返回
```

---

## 🟠 High - 上线前强烈建议修复

### ✅ H2. 修复 Recovery 构造函数

**文件**: `domains/dbdegradation/recovery.go`

**选项 1**: 移除 dbWriter 参数（推荐 - 简单直接）

```go
// 修改文档 docs/db-degradation-implementation-summary.md:102-107
// 改为：
recovery = dbdegradation.NewRecovery(
    dbConn.Pool(),
    fileReader,
    100,  // batch size
)

// recovery.go 保持不变（已经是正确的）
```

**选项 2**: 添加 dbWriter 参数（如果需要批量优化）

```go
// recovery.go:17-36
type Recovery struct {
	db         *pgxpool.Pool
	fileReader *FileReader
	dbWriter   *session.DBWriter  // 新增
	batchSize  int
	tasks      sync.Map
}

func NewRecovery(db *pgxpool.Pool, fileReader *FileReader, dbWriter *session.DBWriter, batchSize int) *Recovery {
	if batchSize <= 0 {
		batchSize = 100
	}
	return &Recovery{
		db:         db,
		fileReader: fileReader,
		dbWriter:   dbWriter,
		batchSize:  batchSize,
	}
}
```

**推荐**: 采用选项 1，更新文档即可。

---

### ✅ H1. 文件大小限制

**文件**: `domains/dbdegradation/file_writer.go`

**修改**:

```go
// 第 18-28 行 - 更新 FileWriter 结构
type FileWriter struct {
	baseDir       string
	mu            sync.Mutex
	currentFile   *os.File
	currentGzip   *gzip.Writer
	currentDate   string
	currentSeq    int           // 新增：当日文件序号
	encoder       *json.Encoder
	stats         atomic.Value
	retryMax      int
	maxFileSize   int64         // 新增：单文件最大大小（字节）
	maxDailyFiles int           // 新增：单日最大文件数
}

// 第 30-38 行 - 更新构造函数
func NewFileWriter(baseDir string) *FileWriter {
	fw := &FileWriter{
		baseDir:       baseDir,
		retryMax:      3,
		maxFileSize:   100 * 1024 * 1024, // 100MB
		maxDailyFiles: 10,
	}
	fw.stats.Store(Stats{})
	return fw
}

// 第 88-137 行 - 修改 writeRecordOnce
func (fw *FileWriter) writeRecordOnce(record BackupRecord) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	date := time.Now().Format("2006-01-02")
	
	// 检查是否需要轮转
	if fw.currentFile != nil && fw.currentDate == date {
		if fileInfo, err := fw.currentFile.Stat(); err == nil {
			if fw.maxFileSize > 0 && fileInfo.Size() >= fw.maxFileSize {
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

	// 序列化并写入
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	data = append(data, '\n')

	n, err := fw.currentGzip.Write(data)
	if err != nil {
		return fmt.Errorf("write to gzip: %w", err)
	}

	// 更新统计
	stats := fw.stats.Load().(Stats)
	stats.TotalRecords++
	stats.TotalBytes += int64(len(data))
	stats.LastWriteTime = time.Now()
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

// 新增：轮转文件
func (fw *FileWriter) rotateFile(date string) error {
	if fw.currentDate != date {
		fw.currentSeq = 0
	} else {
		fw.currentSeq++
	}
	
	if fw.currentSeq >= fw.maxDailyFiles {
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

// 第 140-181 行 - 修改 ensureFile 支持序号
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

	// 创建备份目录
	backupDir := filepath.Join(fw.baseDir, "backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {  // 修改权限为 0700
		return fmt.Errorf("create backup dir: %w", err)
	}

	// 生成文件名（支持序号）
	var filename string
	if fw.currentSeq == 0 {
		filename = fmt.Sprintf("sessions-%s.jsonl.gz", date)
	} else {
		filename = fmt.Sprintf("sessions-%s-%02d.jsonl.gz", date, fw.currentSeq)
	}
	filepath := filepath.Join(backupDir, filename)

	file, err := os.OpenFile(filepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)  // 修改权限为 0600
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}

	gzipWriter := gzip.NewWriter(file)

	fw.currentFile = file
	fw.currentGzip = gzipWriter
	fw.currentDate = date

	slog.Info("file writer: opened new backup file",
		"filename", filename,
		"path", filepath,
	)

	return nil
}
```

**测试验证**:
```bash
# 模拟大量写入，验证文件轮转
# 预期：单文件达到 100MB 时自动创建 sessions-2026-07-10-01.jsonl.gz
```

---

### ✅ H3. 敏感错误信息隐藏

**文件**: `admin/db_degradation_handlers.go`, `domains/dbdegradation/recovery.go`

**修改**: 在 C1 修复中已包含（所有 handler 统一改为通用错误消息）

**recovery.go 额外修改**:

```go
// 第 103-106 行
if err != nil {
	task.Status = "failed"
	task.Error = "failed to read backup records"  // 通用错误
	task.CompletedAt = time.Now()
	slog.Error("recovery: failed to read records", "task_id", task.ID, "error", err)  // 详细日志
	return
}
```

---

## 🟡 Medium - 后续迭代优化

### M1. goroutine 超时保护

**文件**: `domains/dbdegradation/recovery.go:38-51`, `monitor.go:196-212`

```go
// recovery.go
func (r *Recovery) RecoverFile(ctx context.Context, filename string, deleteAfter bool) (string, error) {
	taskID := uuid.New().String()
	task := &RecoveryTask{
		ID:       taskID,
		Filename: filename,
		Status:   "pending",
	}
	r.tasks.Store(taskID, task)

	// 使用带超时的 context
	taskCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	go func() {
		defer cancel()
		r.executeRecovery(taskCtx, task, deleteAfter)
	}()

	return taskID, nil
}

// monitor.go
func (m *Monitor) notifyListeners(event StatusChangeEvent) {
	m.listenersMu.RLock()
	listeners := make([]StatusChangeListener, len(m.listeners))
	copy(listeners, m.listeners)
	m.listenersMu.RUnlock()

	for _, listener := range listeners {
		go func(l StatusChangeListener) {
			done := make(chan struct{})
			go func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("listener panic", "panic", r)
					}
					close(done)
				}()
				l(event)
			}()
			
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				slog.Warn("listener timeout", "event", event.NewStatus.String())
			}
		}(listener)
	}
}
```

### M3. TTL 延长重试

**文件**: `domains/dbdegradation/ttl_manager.go:94-110`

```go
func (tm *TTLManager) runExtendLoop() {
	defer close(tm.doneCh)
	ticker := time.NewTicker(tm.extendInterval)
	defer ticker.Stop()
	
	retryCount := 0
	const maxRetries = 3

	for {
		select {
		case <-tm.stopCh:
			slog.Info("ttl_manager: extend loop stopped")
			return
		case <-ticker.C:
			if err := tm.extendAllSessionTTLs(context.Background()); err != nil {
				retryCount++
				slog.Warn("ttl_manager: failed to extend TTLs", 
					"error", err, 
					"retry_count", retryCount,
				)
				if retryCount < maxRetries {
					time.Sleep(time.Duration(retryCount) * 5 * time.Second)
					continue
				}
			}
			retryCount = 0
		}
	}
}
```

### M5. 事务超时

**文件**: `domains/dbdegradation/recovery.go:198-238`

```go
func (r *Recovery) recoverSession(ctx context.Context, sessionID string, records []BackupRecord) error {
	// ... 查找快照和轮换记录 ...

	// 设置事务超时
	txCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tx, err := r.db.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(txCtx)

	if err := r.writeSnapshot(txCtx, tx, snapshot); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	if err := r.writeRotations(txCtx, tx, sessionID, rotations); err != nil {
		return fmt.Errorf("write rotations: %w", err)
	}

	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}
```

---

## 📋 修复验证清单

### 功能测试

- [ ] 路径遍历攻击被阻止（`../`、`/etc/passwd.gz`）
- [ ] 正常文件名可以访问（`sessions-2026-07-10.jsonl.gz`）
- [ ] 文件达到 100MB 时自动轮转
- [ ] 恢复任务 30 分钟超时正常工作
- [ ] TTL 延长失败后自动重试

### 安全测试

- [ ] 所有 API 错误消息不包含敏感路径
- [ ] 备份文件权限为 0600（仅 owner 可读写）
- [ ] 备份目录权限为 0700（仅 owner 可访问）

### 集成测试

- [ ] main.go 集成不报错（构造函数签名正确）
- [ ] 降级模式切换正常
- [ ] 数据恢复幂等性正常

---

## 🚀 部署步骤

1. **代码修复** - 按清单修复所有 Critical 和 High 问题
2. **单元测试** - 验证修复后的功能
3. **更新文档** - 修改 `db-degradation-implementation-summary.md` 中的构造函数示例
4. **代码审查** - 提交 PR 让团队审查修复代码
5. **集成测试** - 在测试环境完整测试降级和恢复流程
6. **生产部署** - 灰度发布，监控关键指标

---

**预计修复时间**: 
- Critical + High: 4-6 小时
- Medium 优化: 2-3 小时
- 测试验证: 2-3 小时

**总计**: 1-2 个工作日
