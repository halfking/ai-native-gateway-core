package dbdegradation

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var backupFilenamePattern = regexp.MustCompile(`^sessions-(\d{4})-(\d{2})-(\d{2})(?:-\d{2})?\.jsonl\.gz$`)

// cachedBackupFile 缓存的备份文件信息
type cachedBackupFile struct {
	file     *BackupFile
	cachedAt time.Time
}

// R80 轮（2026-10-24）的两个常量。
const (
	// summaryCacheTTLDefault 摘要缓存的默认有效期。
	//
	// ★ R81 轮按实测重算：全量重算 = 100 个文件 × ~5~8s（流式）≈ **8~13min**
	//	⇒ TTL 必须**远大于**重算耗时，否则会变成「一直在重算」（纯烧 CPU）。
	//	⇒ 6h ⇒ 稳态 CPU ≈ 13min / 6h ≈ **3.6% 单核**。
	summaryCacheTTLDefault = 6 * time.Hour
	// summaryComputeBudget 后台重算的预算（远大于端点的 5s 请求预算）。
	//
	// ★ R81 轮：实测全量重算 ~8~13min ⇒ 30min 预算足够跑完
	//	（★ 预算太小 ⇒ 后台**永远算不完** ⇒ 每轮都白烧一次 CPU）。
	summaryComputeBudget = 30 * time.Minute
)

// cachedBackupSummary 是 GetBackupSummary 的**整体结果**缓存。
//
// ★ R79 轮（2026-10-24）：为什么需要它 ——
// 旧实现里 `GetFileSummary`（**有** per-file 缓存）算完 stats 后，
// `GetBackupSummary` 又对**同一个文件**再 `ReadRecords` 一遍，只为了数
// **跨文件全局唯一**的 session ⇒ ⇒ **每个备份文件被完整解压两遍**，
// 且第二遍**完全不受 per-file 缓存保护** ⇒ ⇒ 端点
// `/api/admin/data-lifecycle/degradation/status` **每次请求**都要重解压全部备份。
//
// ★ 缓存口径：**按「备份文件集指纹」**（文件名 + 大小 + mtime 的哈希）⇒
// 备份文件增/改/删都会让指纹变 ⇒ 自动失效。
//
// ★ R80 轮（2026-10-24）新增两个字段：
//
//	`partial` / `partialReason` —— ★★ **「部分结果」必须能被区分于「完整结果」**。
//	R79 给端点加了 5s ctx 预算 ⇒ 备份摘要的计算**可能被预算截断**
//	⇒ 若仍把截断结果当完整结果缓存，端点就会对外说
//	「100 个备份 / 5.18GB / 记录数 0 / 会话数 0」——**这是静默的错误数据**。
//	⇒ ⇒ 部分结果**照样缓存**（避免每次请求重解压 5GB），
//	但**必须**带 partial 标记 ⇒ 调用方据此显式标注降级。
type cachedBackupSummary struct {
	summary     *BackupSummary
	fingerprint string
	cachedAt    time.Time
	// partial=true 表示这份摘要**不完整**（有文件读失败 / 计算被 ctx 截断）。
	partial bool
	// partialReason 说明为什么不完整（第一个失败原因）。
	partialReason string
}

// partialError 把「不完整」翻译成 error。
//
// ★ 纪律（同 R77/R79）：**部分结果 ≠ 完整结果** ⇒
// 调用方必须能区分 ⇒ 这里用 error 表达 ⇒ 端点标 `degraded`。
func (c *cachedBackupSummary) partialError() error {
	if c == nil || !c.partial {
		return nil
	}
	return backupSummaryError(c.partialReason)
}

// backupSummaryError 给备份摘要的降级原因加**统一前缀**（单一真值源）。
//
// ★ R82b 轮（2026-10-24）：R80 时代前缀加在**两处**
//
//	（域层 `partialError()` + admin handler）⇒ 端点对外出现
//	「backup summary incomplete: backup summary incomplete: …」；
//	而**冷启动路径**又完全没前缀 ⇒ 同一端点的前缀时有时无。
//	⇒ ⇒ 前缀只在**域层**加一次，所有路径统一。
func backupSummaryError(reason string) error {
	if reason == "" {
		return nil
	}
	return fmt.Errorf("backup summary incomplete: %s", reason)
}

// backupSetFingerprint 计算备份文件集的指纹。
//
// ★ 必须包含 **size + mtime**：只按文件名算的话，同名文件被覆盖写（备份轮转）
// 不会让指纹变 ⇒ 缓存会返回**旧数据**。
func backupSetFingerprint(files []BackupFile) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s|%d|%d\n", f.Filename, f.Size, f.ModifiedAt.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FileReader 文件备份读取器（支持 gzip 解压）
type FileReader struct {
	baseDir  string
	cacheMu  sync.RWMutex
	cache    map[string]*cachedBackupFile // 每文件独立缓存时间
	cacheTTL time.Duration
	// summaryMu/summaryCache 是 GetBackupSummary 的**整体结果**缓存
	// （R79 轮：消除「每个文件解压两遍」里的第二遍 —— 见 cachedBackupSummary）。
	summaryMu    sync.Mutex
	summaryCache *cachedBackupSummary
	// summaryRefreshing 是后台刷新的**单飞闸门**（R80 轮）。
	//
	// ★ 为什么要有后台刷新：备份摘要在生产上要**解压 5.18GB / 100 个文件**
	//	（实测冷路径 ~48s）⇒ 它**不可能**塞进端点的 5s 请求预算。
	//	⇒ 请求路径**绝不**付这个代价 ⇒ 由后台 goroutine 慢慢算，
	//	算完写回缓存 ⇒ 请求读缓存（毫秒级）。
	summaryRefreshing atomic.Bool
	// summaryRefreshDisabled 关闭后台刷新（**仅供测试注入**）。
	//
	// ★ 生产必须为 false：后台刷新会**异步**改写缓存与重算计数
	//	⇒ 测试要断言「重算次数」就必须能把它关掉，否则门会 flaky。
	summaryRefreshDisabled bool
	// summaryCacheTTL 摘要缓存的有效期（R80 轮）。
	//
	// ★ 做成**字段**（不是 const）：门禁需要把它调到极小来触发「过期」路径，
	//	而生产值必须是 30min（摘要每次重算要解压 5.18GB ⇒ 不能更频繁）。
	summaryCacheTTL time.Duration
	// summaryComputations 统计「摘要被重算」的次数（= 缓存未命中次数）。
	//
	// ★ R79 轮：给「第二遍解压是否还在发生」一个**可直接断言**的读数 ——
	//   门禁用它钉住不变量：文件没变时，两次调用**只算一次**。
	// ★ 它也是生产侧的可观测点（想知道端点是不是在反复重解压，看它涨不涨）。
	summaryComputations int64
	// summarySalvageFallbacks 统计「摘要链路回退到打捞路径」的次数（R82 轮）。
	//
	// ★ 为什么要有它：R81 把摘要切成纯流式，**在生产坏文件上读不出任何记录**
	//	（154 实测 53/100 个文件 `gzip -t` 失败 ⇒ 单遍 gzip 0 条可恢复）
	//	⇒ R82 改成「快路径 + 打捞回退」。
	//	⇒ 这个计数 = 「快路径失败的文件数」⇒ 生产上应当 ≈ 坏文件数；
	//	⇒ 若它暴涨 ⇒ **好文件也开始读不出**（更严重的信号）。
	summarySalvageFallbacks int64
	// fileReads 统计「备份文件被读（解压）了多少次」（R80 轮）。
	//
	// ★ 这是「每个文件只解压一遍」这条不变量的**可断言读数**：
	//	旧实现算一次摘要 ⇒ 每个文件解压 **2** 遍（统计一遍 + 全局 session 一遍）；
	//	新实现 ⇒ 每个文件解压 **1** 遍 ⇒ N 个文件的摘要 ⇒ fileReads 应 == N。
	// ★ 生产侧也是观测点（摘要重算的成本直接由它决定）。
	fileReads int64
	// salvageCapBytes 覆盖打捞路径压缩字节上限（0 = 默认
	// salvageMaxCompressedBytes）。仅供测试注入：真 256MB 压缩文件在测试
	// 预算内不可构造，缩小阈值即可走同一段超限分支代码。
	salvageCapBytes int64
}

// NewFileReader 创建文件读取器
func NewFileReader(baseDir string) *FileReader {
	return &FileReader{
		baseDir:         baseDir,
		cache:           make(map[string]*cachedBackupFile),
		cacheTTL:        5 * time.Minute,
		summaryCacheTTL: summaryCacheTTLDefault,
	}
}

// validateBackupFilename 验证备份文件名格式并检查日期有效性
func validateBackupFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}

	// 检查路径遍历字符
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		return fmt.Errorf("filename contains invalid characters")
	}

	// 验证格式并提取日期
	matches := backupFilenamePattern.FindStringSubmatch(filename)
	if matches == nil {
		return fmt.Errorf("filename must match format: sessions-YYYY-MM-DD.jsonl.gz or sessions-YYYY-MM-DD-NN.jsonl.gz")
	}

	// 验证月份范围 01-12
	month, _ := strconv.Atoi(matches[2])
	if month < 1 || month > 12 {
		return fmt.Errorf("invalid month: must be 01-12")
	}

	// 验证日期范围 01-31
	day, _ := strconv.Atoi(matches[3])
	if day < 1 || day > 31 {
		return fmt.Errorf("invalid day: must be 01-31")
	}

	return nil
}

// backupPath 返回备份文件的完整路径，并验证文件类型
func (fr *FileReader) backupPath(filename string) (string, error) {
	if err := validateBackupFilename(filename); err != nil {
		return "", err
	}

	backupDir := filepath.Join(fr.baseDir, "backups")
	path := filepath.Join(backupDir, filename)

	// 使用 Lstat 检查文件类型（不跟随符号链接）
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}

	// 拒绝符号链接和非常规文件
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}

	return path, nil
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

		// 验证文件名格式
		if err := validateBackupFilename(name); err != nil {
			slog.Warn("file reader: invalid backup filename", "filename", name, "error", err)
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

// readFileSummary 读一个备份文件并统计（**不查缓存**）。
//
// ★ R80 轮（2026-10-24）：它是 `GetFileSummary` 与 `computeSummary` 的**共同底座**，
//
//	一次解压同时产出三样东西：
//
//	① 文件元信息 + ② 记录数 + ③ **该文件的 session id 集合**。
//
//	★★ 关键收益：旧实现里 `GetBackupSummary` 为了算「跨文件全局唯一 session」
//	会对**同一个文件再解压一遍**（第二遍完全不受 per-file 缓存保护）
//	⇒ **每个备份文件被完整解压两遍**。
//	⇒ 现在第一遍就把 session 集合带出来 ⇒ **每个文件只解压一遍**
//	⇒ 摘摘要的计算成本减半（R79 只把它「缓存掉了」，R80 把它「消掉了」）。
//
// ★ 返回的 error 表示「统计**不完整**」（ctx 截断 / 文件不可读）：
//
//	**调用方必须区分**它 —— 否则会对外说「这个文件有 0 条记录」。
func (fr *FileReader) readFileSummary(ctx context.Context, filename string) (*BackupFile, map[string]struct{}, error) {
	// 验证并获取路径
	path, err := fr.backupPath(filename)
	if err != nil {
		return nil, nil, err
	}

	// 获取文件信息
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stat file: %w", err)
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

	// 统计记录数和会话数（**一趟**）
	//
	sessionSet := make(map[string]struct{})
	recordCount := 0

	err = fr.ReadRecords(ctx, filename, func(record BackupRecord) error {
		recordCount++
		// ★ R82b 轮：**空串不是会话** ⇒ 不计入 session 集合
		//	（否则生产上「唯一 session 数」恒为 1 —— 读数失真）。
		if record.SessionID != "" {
			sessionSet[record.SessionID] = struct{}{}
		}
		return nil
	})

	if err != nil {
		slog.Warn("file reader: failed to count records", "filename", filename, "error", err)
	}

	file.RecordCount = recordCount
	file.SessionCount = len(sessionSet)

	if err != nil {
		return file, sessionSet, fmt.Errorf("统计不完整（%s）：%w", filename, err)
	}
	return file, sessionSet, nil
}

// readFileSummaryStreaming 与 `readFileSummary` 同义，但走**纯流式**读。
//
// ★ R81 轮（2026-10-24）为什么必须有它（实测，154 生产）：
//
//	同一个 106MB 备份文件：
//	  - `gzip -dc | wc -l`（纯解压）        = **2.92s**
//	  - 网关摘要路径（旧，打捞路径）           = **~45s**
//	⇒ 差 ~15×，差在**打捞路径的 member 边界搜索**
//	（`gzipMemberEnd` 用「指数查找 + 二分查找」逐个**试解**前缀
//	  ⇒ 同一个文件被**反复完整解码 ~20~30 次**）。
//
// ⇒ 摘要只是要「计数」，**不需要打捞**（打捞是给「恢复」链路用的）⇒
//
//	⇒ 摘要链路改走纯流式 ⇒ 成本降 ~15×。
//	⚠ 口径不变：完好文件上，流式与打捞读出的记录数/会话数**逐字相同**
//	（由 `TestR81StreamingSummaryMatchesSalvageCounts` 钉住）。
//	⚠ 损坏文件：流式会报错 ⇒ 该文件被计入「失败」⇒ 摘要标 **partial**
//	（诚实：打捞路径只在**恢复**链路才需要）。
func (fr *FileReader) readFileSummaryStreaming(ctx context.Context, filename string) (*BackupFile, map[string]struct{}, error) {
	path, err := fr.backupPath(filename)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stat file: %w", err)
	}

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

	sessionSet := make(map[string]struct{})
	recordCount := 0

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// ★ 计一次解压（与 ReadRecordsWithStats 同口径 ⇒ 「每个文件只解压一遍」可断言）
	atomic.AddInt64(&fr.fileReads, 1)

	if err := readGzipStream(ctx, f, func(record BackupRecord) error {
		recordCount++
		// ★ R82b 轮：**空串不是会话** ⇒ 不计入 session 集合。
		if record.SessionID != "" {
			sessionSet[record.SessionID] = struct{}{}
		}
		return nil
	}); err != nil {
		slog.Warn("file reader: failed to count records (streaming)", "filename", filename, "error", err)
		file.RecordCount = recordCount
		file.SessionCount = len(sessionSet)
		return file, sessionSet, fmt.Errorf("统计不完整（%s）：%w", filename, err)
	}

	file.RecordCount = recordCount
	file.SessionCount = len(sessionSet)
	return file, sessionSet, nil
}

// GetFileSummary 获取单个文件的统计信息
//
// ★ R80 轮：**契约不变**（仍然吞掉统计错误 ⇒ 调用方拿到的还是那个 file），
//
//	但统计的**底座**换成了 readFileSummary（一次解压同时给出 session 集合）。
//
//	⚠ 注意：这里**故意**保留「吞错」语义 ——
//	恢复链路（generic_recovery / handleRecoverBackupFile）依赖「能救多少救多少」；
//	而**摘要**链路（computeSummary）改用 readFileSummary 的错误 ⇒ 那里才会
//	把「统计不完整」如实报出来。
func (fr *FileReader) GetFileSummary(ctx context.Context, filename string) (*BackupFile, error) {
	// 检查缓存
	fr.cacheMu.RLock()
	if cached, ok := fr.cache[filename]; ok {
		if time.Since(cached.cachedAt) < fr.cacheTTL {
			fr.cacheMu.RUnlock()
			return cached.file, nil
		}
	}
	fr.cacheMu.RUnlock()

	file, _, statErr := fr.readFileSummary(ctx, filename)
	if file == nil {
		// ★ 路径/ stat 失败 ⇒ 真正的错误（与旧实现一致）
		return nil, statErr
	}

	// 更新缓存
	fr.cacheMu.Lock()
	fr.cache[filename] = &cachedBackupFile{
		file:     file,
		cachedAt: time.Now(),
	}
	fr.cacheMu.Unlock()

	return file, nil
}

// GetBackupSummary 获取所有备份的汇总信息
//
// ★ R79 轮（2026-10-24）：加**整体结果缓存**（按「备份文件集指纹」）。
//
//	旧实现：GetFileSummary（**有** per-file 缓存）算完 stats 后，
//	GetBackupSummary 又对**同一个文件**再 ReadRecords 一遍，只为了数
//	**跨文件全局唯一**的 session ⇒ ⇒ **每个备份文件被完整解压两遍**，
//	而第二遍**完全不受 per-file 缓存保护** ⇒ ⇒ 端点
//	`/api/admin/data-lifecycle/degradation/status` **每次请求**都重解压全部备份。
//
//	现在：指纹命中 ⇒ 直接返回缓存（零 IO、零解压）；
//	指纹变（文件增/改/删/被覆盖写）⇒ 重算。
//
//	★ **口径不变**：TotalSessions 仍是**跨文件全局唯一**的 session 数
//	（不是各文件 SessionCount 之和 —— 那会把跨文件重名会话算多次）。
//
// ★ R80 轮（2026-10-24）的三处语义（**都是被生产读数逼出来的**）：
//
// ① **请求路径不再付解压代价**：生产实测冷路径要解压 5.18GB / 100 个文件
//
//	（~48s）⇒ 它**装不进**端点的 5s 预算 ⇒
//	⇒ 缓存缺失/过期时**触发后台刷新**（单飞），请求**立即返回现有缓存**
//	（没有缓存才就地算，且可能不完整 ⇒ 显式报错）。
//
// ② **部分结果必须可识别**：计算被 ctx 截断 / 有文件读失败 ⇒
//
//	摘要**不完整** ⇒ 返回 error ⇒ 端点标 `degraded`。
//	⇒ R79 的教训：截断结果若被当完整结果缓存，端点会对外说
//	「100 个备份 / 5.18GB / 记录数 0 / 会话数 0」——**静默的错误数据**。
//
// ③ **部分结果照样缓存**（带 partial 标记）：否则每次请求都要重解压 5GB
//
//	⇒ 共享生产机上这是持续的 CPU 浪费。
func (fr *FileReader) GetBackupSummary(ctx context.Context) (*BackupSummary, error) {
	files, err := fr.ListBackupFiles(ctx)
	if err != nil {
		return nil, err
	}

	fingerprint := backupSetFingerprint(files)

	fr.summaryMu.Lock()
	cached := fr.summaryCache
	fr.summaryMu.Unlock()

	// ① 快路径：指纹一致且未过期 ⇒ 直接返回缓存（零 IO、零解压）
	if cached != nil && cached.fingerprint == fingerprint && time.Since(cached.cachedAt) < fr.summaryCacheTTL {
		if cached.partial {
			// ★ 缓存里是**部分结果** ⇒ 后台去补完整版（本次请求**不等**）
			fr.triggerSummaryRefresh(files, fingerprint)
		}
		return cloneBackupSummary(cached.summary), cached.partialError()
	}

	// ② 过期但指纹一致 ⇒ 先把旧值返回（快），同时后台刷新
	if cached != nil && cached.fingerprint == fingerprint {
		fr.triggerSummaryRefresh(files, fingerprint)
		return cloneBackupSummary(cached.summary), cached.partialError()
	}

	// ③ 冷启动 / 备份集变了（指纹变 ⇒ 旧值口径已不可信）⇒ 就地算
	//	（受**请求 ctx** 限制 ⇒ 可能被截断 ⇒ 不完整 ⇒ 报错）
	//
	//	★ R82 轮：请求路径**不做**打捞回退（打捞对坏文件要 ~11min
	//	⇒ 5s 预算里必然被打断 ⇒ 只会更空）；后台刷新才做回退。
	//
	//	★★★ R82b 轮（**生产读数抓到的活锁**）：这里就地算出来的
	//	**partial 结果绝不能写缓存** ——
	//	★ 生产实测（2830）：后台刷新跑完（写出**真实记录数**的 partial），
	//		而当天日文件被改写 ⇒ 指纹变 ⇒ 下一次请求走本路径
	//		⇒ 就地算被 5s 预算截断（记录数 **0**）
	//		⇒ ★ **覆盖掉了后台刷新的好结果** ⇒ 端点继续显示「记录数 0」
	//		⇒ ★⇒ 而指纹**每天都会变** ⇒ 真实数字每天只存在十几分钟
	//	⇒ ⇒ **规则：只有【后台刷新】的结果才写缓存**；
	//	　　请求路径的就地结果**只给本次请求**（⇒ 零 IO 浪费，也零覆盖）。
	//
	//	★ 附带：**后台刷新正在跑**时 ⇒ **不做**就地计算
	//	（否则刷新窗口内的每个请求都要白烧 5s CPU）。
	if fr.summaryRefreshing.Load() {
		fr.triggerSummaryRefresh(files, fingerprint)
		return nil, backupSummaryError("备份摘要正在后台重算，请稍后重试")
	}
	summary, partial, perr := fr.computeSummaryWith(ctx, files, false)
	if perr != nil && !partial {
		return nil, backupSummaryError(perr.Error())
	}
	// ★ R82b 轮：就地计算的**部分结果不写缓存**（见上面的活锁说明）
	if !partial {
		fr.storeSummary(summary, fingerprint, false, "")
		atomic.AddInt64(&fr.summaryComputations, 1)
		return cloneBackupSummary(summary), nil
	}
	// ★ R82 轮：冷启动路径也**必须**触发后台刷新
	//	（否则要等到**第二次**请求才会触发 ⇒ 只有一个用户打开页面时，
	//	 真实数字永远出不来）。
	fr.triggerSummaryRefresh(files, fingerprint)
	return cloneBackupSummary(summary), backupSummaryError(perr.Error())
}

// computeSummary 计算备份摘要（**不持有 summaryMu**）。
//
// ★ 返回 (摘要, 是否不完整, 错误)：
//
//	不完整 = 有文件**读不出**（快路径 + 打捞回退都失败）
//	       **或** 有文件**有损**（读出来了，但有内容不可恢复）
//	（★ R80 轮补上的洞：旧实现**忽略**了 ReadRecords 的返回值
//	  ⇒ ctx 截断时「跨文件唯一 session 数」会被静默低估）。
//
// ★ R82 轮（2026-10-24）：**读法修正**。
//
//	生产实测：154 上 **53/100 个备份文件**单遍 gzip 读不出
//	（孤儿 gzip header 连排 ⇒ `gzip.NewReader` 首个 member 即失败）
//	⇒ 纯流式会把它们**全部读成 0 条** ⇒ 端点对外说「记录数 0」。
//	⇒ ⇒ 改成「快路径（流式）失败 ⇒ 回退打捞」，口径与 R80 逐字相同。
//
// ★ allowSalvage：
//
//	true  = 后台刷新用（预算 30min ⇒ 允许 ~11min 的打捞）
//	false = 请求路径用（预算 5s ⇒ 打捞必然被打断 ⇒ 徒增 IO）
func (fr *FileReader) computeSummary(ctx context.Context, files []BackupFile) (*BackupSummary, bool, error) {
	return fr.computeSummaryWith(ctx, files, true)
}

// computeSummaryWith 是 `computeSummary` 的可切换版本（allowSalvage 决定是否回退打捞）。
func (fr *FileReader) computeSummaryWith(ctx context.Context, files []BackupFile, allowSalvage bool) (*BackupSummary, bool, error) {
	summary := &BackupSummary{
		Files: make([]BackupFile, 0, len(files)),
	}

	allSessions := make(map[string]struct{})
	var dates []string
	failed := 0
	lossy := 0
	var firstErr error

	for _, file := range files {
		// ★ R82 轮：快路径（流式）失败 ⇒ 回退打捞 ⇒ 能读出坏文件里的记录。
		outcome := fr.readFileSummaryResilient(ctx, file.Filename, allowSalvage)
		if outcome.err != nil {
			// ★ 统计不完整（ctx 截断 / 文件不可读）⇒ **必须记账**
			failed++
			if firstErr == nil {
				firstErr = outcome.err
			}
			if outcome.file != nil {
				summary.Files = append(summary.Files, *outcome.file)
				summary.TotalRecords += outcome.file.RecordCount
				summary.TotalSize += outcome.file.Size
				dates = append(dates, outcome.file.Date)
			} else {
				summary.Files = append(summary.Files, file)
			}
			continue
		}

		detailed := outcome.file
		summary.Files = append(summary.Files, *detailed)
		summary.TotalRecords += detailed.RecordCount
		summary.TotalSize += detailed.Size
		dates = append(dates, detailed.Date)

		// ★ R82 轮：读出来了但**有损** ⇒ 也要记账（数据不完整 ⇒ 不能当完整结果）
		if outcome.lossy {
			lossy++
		}

		// 跨文件全局唯一 session 数 = 各文件 session 集合的并集
		// （★ 口径不变：不是各文件 SessionCount 之和 —— 那会把重名会话算多次）
		for id := range outcome.sessions {
			allSessions[id] = struct{}{}
		}
	}

	summary.TotalFiles = len(files)
	summary.TotalSessions = len(allSessions)
	// ★ R82b 轮：会话数口径**当前是否可用**？
	//	有记录、却一个真实 session id 都没有 ⇒ 该读数**无意义**
	//	（生产实测：备份记录几乎全是 request_wal/request_log ⇒ 没有会话维度）
	//	⇒ 显式标记 ⇒ 端点据此标 degraded（而不是让「会话数 0/1」沉默）。
	//	★ **判据不能依赖「记录数」** —— 冷启动窗口里 `TotalRecords == 0` 的意思是
	//	「**还没统计完**」而不是「真的 0 条记录」⇒ 用它判定会误报「可用」。
	//	★ 改用「**有备份文件、却一个真实 session id 都没有**」⇒ 不可用。
	summary.SessionCountAvailable = !(summary.TotalFiles > 0 && summary.TotalSessions == 0)

	// 计算日期范围
	if len(dates) > 0 {
		sort.Strings(dates)
		if dates[0] == dates[len(dates)-1] {
			summary.DateRange = dates[0]
		} else {
			summary.DateRange = fmt.Sprintf("%s to %s", dates[0], dates[len(dates)-1])
		}
	}

	// ★ R82 轮：两类问题**分开记账** ⇒ 运维能区分
	//	「读不出来」（failed）与「读出来了但不完整」（lossy）。
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d 个备份文件读取失败：%v", failed, len(files), firstErr))
	}
	if lossy > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d 个备份文件有损（已打捞出可恢复部分，仍有内容不可恢复）", lossy, len(files)))
	}
	if len(parts) > 0 {
		return summary, true, fmt.Errorf("%s", strings.Join(parts, "；"))
	}
	return summary, false, nil
}

// triggerSummaryRefresh 触发**后台**摘要刷新（单飞，非阻塞）。
//
// ★ R80 轮：备份摘要在生产上要解压 5.18GB（~48s）⇒
//
//	它**不能**在请求路径上发生 ⇒ 后台慢慢算，算完写缓存。
//	⇒ 请求侧要么读旧缓存（快），要么就地算（冷启动，可能不完整 ⇒ 报错）。
func (fr *FileReader) triggerSummaryRefresh(files []BackupFile, fingerprint string) {
	if fr.summaryRefreshDisabled {
		return
	}
	if !fr.summaryRefreshing.CompareAndSwap(false, true) {
		return // 已有刷新在跑 ⇒ 不重复
	}
	go func() {
		defer fr.summaryRefreshing.Store(false)
		// ★ 后台预算远大于请求预算（生产实测 ~48s）⇒ 10 分钟绰绰有余
		ctx, cancel := context.WithTimeout(context.Background(), summaryComputeBudget)
		defer cancel()

		summary, partial, err := fr.computeSummaryWith(ctx, files, true)
		if partial {
			slog.Warn("file reader: background backup summary refresh produced incomplete result",
				"error", err)
			fr.storeSummary(summary, fingerprint, true, err.Error())
			return
		}
		fr.storeSummary(summary, fingerprint, false, "")
		atomic.AddInt64(&fr.summaryComputations, 1)
	}()
}

// storeSummary 写回缓存（**短锁**）。
func (fr *FileReader) storeSummary(summary *BackupSummary, fingerprint string, partial bool, reason string) {
	if summary == nil {
		return
	}
	fr.summaryMu.Lock()
	fr.summaryCache = &cachedBackupSummary{
		summary:       summary,
		fingerprint:   fingerprint,
		cachedAt:      time.Now(),
		partial:       partial,
		partialReason: reason,
	}
	fr.summaryMu.Unlock()
}

// cloneBackupSummary 复制一份摘要返回。
//
// ★ 必须复制：缓存里存的是共享指针 ⇒ 直接返回会被调用方就地改写
//
//	（端点的 JSON 编码不写，但库里其它调用方可能改）⇒ 污染缓存。
func cloneBackupSummary(in *BackupSummary) *BackupSummary {
	if in == nil {
		return nil
	}
	out := *in
	out.Files = make([]BackupFile, len(in.Files))
	copy(out.Files, in.Files)
	return &out
}

// ReadRecords 流式读取记录并回调处理。
//
// 2026-09-30 O5 收口：多进程 O_APPEND 追加 + 容器硬杀跳过 gzip.Close 会把
// 日文件打成「孤儿 header + 完整 member + 截断尾」的混合体，原单遍
// gzip.NewReader 在首个坏 member 即整体失败 → 整文件 0 条可恢复。现常规
// 文件走打捞路径（file_reader_salvage.go：逐 member 重同步 + 坏行跳过），
// 干净文件行为不变；超上限大文件维持纯流式。归档护栏见
// ReadRecordsWithStats / SalvageStats。
func (fr *FileReader) ReadRecords(ctx context.Context, filename string, callback func(BackupRecord) error) error {
	_, err := fr.ReadRecordsWithStats(ctx, filename, callback)
	return err
}

// readGzipStream 纯流式读取（gzip 默认 Multistream 透传多 member），遇坏
// member/坏行整体失败。供超打捞上限的大文件沿用既有行为。
func readGzipStream(ctx context.Context, file *os.File, callback func(BackupRecord) error) error {
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
			return fmt.Errorf("unmarshal record at line %d: %w", lineNum, err)
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

func (fr *FileReader) ArchiveFile(filename string) error {
	if err := validateBackupFilename(filename); err != nil {
		return err
	}
	backupDir := filepath.Join(fr.baseDir, "backups")
	archiveDir := filepath.Join(fr.baseDir, "archive")
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	if err := os.Rename(filepath.Join(backupDir, filename), filepath.Join(archiveDir, filename)); err != nil {
		return fmt.Errorf("move to archive: %w", err)
	}
	fr.InvalidateCache()
	return nil
}

// InvalidateCache 清除缓存
//
// ★ R79 轮：**摘要整体缓存也必须一起清** ——
//
//	`InvalidateCache()` 的契约是「丢掉一切已缓存的文件信息」；
//	若只清 per-file 缓存而留下摘要缓存，则「同名同大小同 mtime 的覆盖写」
//	（备份回滚/恢复）会让摘要缓存**命中旧数据**。
//	（正常路径下文件集指纹会变、摘要缓存会自然失效 ⇒ 这一步是**兜底**，
//	不是唯一防线。）
func (fr *FileReader) InvalidateCache() {
	fr.cacheMu.Lock()
	fr.cache = make(map[string]*cachedBackupFile)
	fr.cacheMu.Unlock()

	fr.summaryMu.Lock()
	fr.summaryCache = nil
	fr.summaryMu.Unlock()
}

// FileReads 返回「备份文件被读（解压）的次数」（R80 轮）。
//
// ★ 用途：把「每个文件只解压一遍」这条不变量变成**可断言的读数**
//
//	（N 个文件的摘要 ⇒ fileReads == N，而不是 2N）。
func (fr *FileReader) FileReads() int64 {
	return atomic.LoadInt64(&fr.fileReads)
}

// SummaryComputations 返回「备份摘要被重算」的次数（缓存未命中的次数）。
//
// ★ R79 轮：门禁与生产观测都用它回答同一个问题 ——
//
//	「端点是不是还在每次请求都把备份重新解压一遍」。
func (fr *FileReader) SummaryComputations() int64 {
	return atomic.LoadInt64(&fr.summaryComputations)
}
