// Package admin — data_lifecycle_storage.go
//
// 2026-07-01: 存储总览（Storage Overview）端点。
// 解决诉求：
//   - 数据库大小 vs 本机磁盘的对比（独立显示，避免误以为 DB 在本机）
//   - 表级 Top-N 占用排行（pg_stat_user_tables 视角）
//   - 本机日志目录大小（lumberjack 旋转的 .log / .log.gz）
//   - 给运维一个"还剩多少、谁在涨"的总览面板
//
// 实现要点：
//   - 不引入新依赖；用 syscall.Statfs（stdlib）拿磁盘容量
//   - 复用 h.db 查询 pg_database_size / pg_total_database_size
//   - 复用 log file 路径（settings.Global "log.*" 之外的 cfg.LogFile
//     在结构上不可见，所以这里走 os.Stat 探测常见路径 + 全局 slog
//     写入器 fallback；详见 resolveLogDir）

package admin

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// databaseStorageInfo Postgres 实例维度的存储信息
type databaseStorageInfo struct {
	// pg_database_size：当前连接的 database 自己的数据+索引
	DatabaseBytes int64  `json:"database_bytes"`
	DatabaseHuman string `json:"database_human"`
	// pg_total_database_size：含 WAL、pg_xlog、TOAST、统计信息
	// 通常 DatabaseBytes < TotalBytes
	TotalBytes int64  `json:"total_bytes"`
	TotalHuman string `json:"total_human"`
	// 表 / 索引 / TOAST 三段拆分（来自 pg_total_relation_size 之和）
	TablesBytes   int64  `json:"tables_bytes"`
	IndexesBytes  int64  `json:"indexes_bytes"`
	ToastBytes    int64  `json:"toast_bytes"`
	FreeBytes     int64  `json:"free_bytes"` // 当前 connection 看不到 PG server 端 fs 剩余（仅客户端视角）
	FreeHuman     string `json:"free_human"`
	ServerVersion string `json:"server_version,omitempty"`
}

// filesystemInfo 本机视角的磁盘容量（针对 gateway binary 所在 filesystem）
type filesystemInfo struct {
	Path        string `json:"path"` // 探测路径
	TotalBytes  int64  `json:"total_bytes"`
	TotalHuman  string `json:"total_human"`
	UsedBytes   int64  `json:"used_bytes"`
	UsedHuman   string `json:"used_human"`
	FreeBytes   int64  `json:"free_bytes"`
	FreeHuman   string `json:"free_human"`
	UsedPercent int    `json:"used_percent"` // 0-100
}

// directoryInfo 本机目录维度（lumberjack log dir / attachment dir）
type directoryInfo struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Files       int    `json:"files"`
	SizeBytes   int64  `json:"size_bytes"`
	SizeHuman   string `json:"size_human"`
	OldestMtime int64  `json:"oldest_mtime"` // unix seconds
	NewestMtime int64  `json:"newest_mtime"`
}

// tableSizeInfo 单表大小（来自 pg_total_relation_size）
type tableSizeInfo struct {
	Table         string `json:"table"`
	Schema        string `json:"schema"`
	Rows          int64  `json:"rows"`
	TotalBytes    int64  `json:"total_bytes"`
	TotalHuman    string `json:"total_human"`
	IndexBytes    int64  `json:"index_bytes"`
	ToastBytes    int64  `json:"toast_bytes"`
	PercentOfDB   int    `json:"percent_of_db"` // 0-100
	IsPartitioned bool   `json:"is_partitioned"`
}

// storageOverview 总览响应
type storageOverview struct {
	Database    databaseStorageInfo `json:"database"`
	Filesystem  filesystemInfo      `json:"filesystem"`
	LocalLogs   *directoryInfo      `json:"local_logs,omitempty"`
	Warnings    []string            `json:"warnings"`
	CollectedAt time.Time           `json:"collected_at"`
}

// tableSizesResponse 表级大小响应（Top-N）
type tableSizesResponse struct {
	Tables      []tableSizeInfo `json:"tables"`
	TotalBytes  int64           `json:"total_bytes"`
	TotalHuman  string          `json:"total_human"`
	CollectedAt time.Time       `json:"collected_at"`
}

// handleDataLifecycleStorage GET /api/admin/data-lifecycle/storage
func (h *Handler) handleDataLifecycleStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	resp := storageOverview{
		Warnings:    []string{},
		CollectedAt: time.Now().UTC(),
	}

	// 1. Database size
	dbInfo, dbErr := queryDatabaseStorage(ctx, h)
	if dbErr != nil {
		slog.Warn("storage: db query failed", "error", dbErr)
		resp.Warnings = append(resp.Warnings, "数据库大小查询失败: "+dbErr.Error())
	} else {
		resp.Database = *dbInfo
	}

	// 2. Filesystem
	fsInfo, fsErr := queryFilesystem(".")
	if fsErr != nil {
		slog.Warn("storage: fs query failed", "error", fsErr)
		resp.Warnings = append(resp.Warnings, "本机磁盘查询失败: "+fsErr.Error())
	} else {
		resp.Filesystem = *fsInfo
	}

	// 3. 本机日志目录（来自 slog 包探测）
	logDir, logErr := resolveLogDir()
	if logErr == nil {
		dirInfo := queryDirectory(logDir)
		resp.LocalLogs = &dirInfo
	}

	// 4. 横向 warning：DB 占比过大、磁盘压力大、归档未做
	if resp.Database.TotalBytes > 0 && resp.Filesystem.TotalBytes > 0 {
		ratio := float64(resp.Database.TotalBytes) / float64(resp.Filesystem.TotalBytes)
		if ratio > 5.0 {
			resp.Warnings = append(resp.Warnings,
				"数据库总大小超过本机磁盘容量的 5 倍 — 表明 DB 部署在独立节点")
		}
	}
	if resp.Filesystem.UsedPercent >= 90 {
		resp.Warnings = append(resp.Warnings, "本机磁盘已用 ≥ 90%，请检查日志/缓存/附件")
	}
	if resp.LocalLogs != nil && resp.LocalLogs.Exists && resp.LocalLogs.SizeBytes > 5<<30 {
		// > 5GB
		resp.Warnings = append(resp.Warnings,
			"本机日志目录已超过 5GB，建议调小 log.max_size_mb / log.max_age_days")
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleDataLifecycleTableSizes GET /api/admin/data-lifecycle/storage/tables?limit=20
func (h *Handler) handleDataLifecycleTableSizes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tables, totalBytes, totalHuman, err := queryTableSizes(ctx, h, limit)
	if err != nil {
		slog.Warn("storage: table sizes query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "查询表大小失败: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, tableSizesResponse{
		Tables:      tables,
		TotalBytes:  totalBytes,
		TotalHuman:  totalHuman,
		CollectedAt: time.Now().UTC(),
	})
}

// ── helpers ────────────────────────────────────────────────────────

// queryDatabaseStorage 查询 PG 当前 database 的三段大小 + server version
func queryDatabaseStorage(ctx context.Context, h *Handler) (*databaseStorageInfo, error) {
	out := &databaseStorageInfo{}
	row := h.db.QueryRow(ctx, `
		SELECT
			pg_database_size(current_database()),
			pg_size_pretty(pg_database_size(current_database())),
			pg_total_database_size(current_database()),
			pg_size_pretty(pg_total_database_size(current_database())),
			current_setting('server_version')
	`)
	var totalBytes int64
	if err := row.Scan(
		&out.DatabaseBytes,
		&out.DatabaseHuman,
		&totalBytes,
		&out.TotalHuman,
		&out.ServerVersion,
	); err != nil {
		return nil, err
	}
	out.TotalBytes = totalBytes

	// 表 / 索引 / TOAST 拆分（pg_class 视角）
	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(SUM(pg_relation_size(c.oid)) FILTER (WHERE c.relkind IN ('r','p')), 0)::bigint,
			COALESCE(SUM(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 'i'), 0)::bigint,
			COALESCE(SUM(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 't'), 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')
	`)
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			_ = rows.Scan(&out.TablesBytes, &out.IndexesBytes, &out.ToastBytes)
		}
	}

	return out, nil
}

// queryFilesystem syscall.Statfs 探测 path 所在 filesystem 容量
func queryFilesystem(path string) (*filesystemInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(abs, &stat); err != nil {
		return nil, err
	}
	total := int64(stat.Blocks) * int64(stat.Bsize)
	free := int64(stat.Bavail) * int64(stat.Bsize)
	used := total - int64(stat.Bfree)*int64(stat.Bsize)
	pct := 0
	if total > 0 {
		pct = int(used * 100 / total)
	}
	return &filesystemInfo{
		Path:        abs,
		TotalBytes:  total,
		TotalHuman:  humanBytes(total),
		UsedBytes:   used,
		UsedHuman:   humanBytes(used),
		FreeBytes:   free,
		FreeHuman:   humanBytes(free),
		UsedPercent: pct,
	}, nil
}

// queryDirectory 递归扫描目录，汇总文件数和总字节数
func queryDirectory(path string) directoryInfo {
	abs, _ := filepath.Abs(path)
	out := directoryInfo{Path: abs}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		out.Exists = false
		return out
	}
	out.Exists = true
	_ = filepath.Walk(path, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		out.Files++
		out.SizeBytes += fi.Size()
		mt := fi.ModTime().Unix()
		if out.OldestMtime == 0 || mt < out.OldestMtime {
			out.OldestMtime = mt
		}
		if mt > out.NewestMtime {
			out.NewestMtime = mt
		}
		return nil
	})
	out.SizeHuman = humanBytes(out.SizeBytes)
	return out
}

// queryTableSizes Top-N 大表（含索引和 TOAST），带 DB 占比
func queryTableSizes(ctx context.Context, h *Handler, limit int) ([]tableSizeInfo, int64, string, error) {
	rows, err := h.db.Query(ctx, `
		SELECT
			c.relname AS table_name,
			n.nspname AS schema_name,
			COALESCE(s.n_live_tup, 0) AS row_estimate,
			pg_total_relation_size(c.oid) AS total_bytes,
			pg_indexes_size(c.oid) AS index_bytes,
			COALESCE(pg_total_relation_size((
				SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid
			)), 0) AS toast_bytes,
			(c.relkind = 'p') AS is_partitioned
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')
		  AND c.relkind IN ('r','p')
		ORDER BY pg_total_relation_size(c.oid) DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, 0, "", err
	}
	defer rows.Close()

	out := make([]tableSizeInfo, 0, limit)
	var totalBytes int64
	for rows.Next() {
		var t tableSizeInfo
		if err := rows.Scan(
			&t.Table, &t.Schema, &t.Rows,
			&t.TotalBytes, &t.IndexBytes, &t.ToastBytes,
			&t.IsPartitioned,
		); err != nil {
			continue
		}
		t.TotalHuman = humanBytes(t.TotalBytes)
		totalBytes += t.TotalBytes
		out = append(out, t)
	}

	// 占比（对 Top-N 求和，不严格等于 DB 总数，仅作参考）
	for i := range out {
		if totalBytes > 0 {
			out[i].PercentOfDB = int(out[i].TotalBytes * 100 / totalBytes)
		}
	}

	return out, totalBytes, humanBytes(totalBytes), nil
}

// resolveLogDir 探测 lumberjack 日志目录。
// 2026-07-02: 优先读 logging.ActiveConfig().File 的真实目录（不再猜测）。
// 兜底：探测常见候选路径 ./var/log、./logs、/var/log/llm-gateway-go。
// 永远返回绝对路径，调用方不应当 panic。
func resolveLogDir() (string, error) {
	// 优先：从 lumberjack 运行时配置读真实路径
	if cfg := logging.ActiveConfig(); cfg.File != "" {
		abs, err := filepath.Abs(filepath.Dir(cfg.File))
		if err == nil {
			return abs, nil
		}
	}
	candidates := []string{
		"./var/log",
		"./logs",
		"/var/log/llm-gateway-go",
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if info, err := os.Stat(abs); err == nil && info.IsDir() {
				return abs, nil
			}
		}
	}
	// fallback：cwd
	abs, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}
	return abs, nil
}

// humanBytes 1024 进制 human-readable
func humanBytes(n int64) string {
	if n < 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return formatInt(int64(v)) + " " + units[i]
	}
	return strings.TrimRight(strings.TrimRight(
		formatFloat(v), "0"), ".") + " " + units[i]
}

// humanBytes exported alias for other files in this package
func humanBytesExport(n int64) string { return humanBytes(n) }

// formatInt / formatFloat 极简实现，避免再拉一个 fmt 包路径分支
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func formatFloat(v float64) string {
	// 保留 1 位小数
	intPart := int64(v)
	frac := int64((v - float64(intPart)) * 10)
	if frac < 0 {
		frac = -frac
	}
	return formatInt(intPart) + "." + formatInt(frac)
}

// sortTablesDesc util：按 TotalBytes 倒序
func sortTablesDesc(tables []tableSizeInfo) {
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].TotalBytes > tables[j].TotalBytes
	})
}
