// Package sqlite 实现 lite 存储模式下基于 SQLite 的元数据存储，
// 覆盖 storage.SessionStore、storage.TurnsStore 与 storage.RequestLogStore 三个接口。
//
// PRAGMA 优化策略：go-sqlite3 原生支持的参数（journal_mode/synchronous/
// busy_timeout/foreign_keys/cache_size）直接写入 DSN 查询串，由驱动在
// 连接池中每个新连接建立时自动执行；不支持的参数（如 temp_store）通过
// 注册带 ConnectHook 的驱动逐连接执行。两种机制共同保证池中所有连接
// 的 PRAGMA 配置一致，避免"PRAGMA 只对单个连接生效"的问题。
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// SchemaSQL 建表语句（全部使用 IF NOT EXISTS，幂等可重复执行）。
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	tenant_id  TEXT NOT NULL,
	user_id    TEXT,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	metadata   TEXT,
	UNIQUE (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_sessions_tenant ON sessions (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_user   ON sessions (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS session_turns (
	tenant_id            TEXT NOT NULL,
	session_id           TEXT NOT NULL,
	turn_no              INTEGER NOT NULL,
	ts                   INTEGER,
	compression_strategy TEXT,
	prompt_tokens        INTEGER DEFAULT 0,
	completion_tokens    INTEGER DEFAULT 0,
	metadata             TEXT,
	PRIMARY KEY (tenant_id, session_id, turn_no)
);

CREATE INDEX IF NOT EXISTS idx_turns_session ON session_turns (session_id, turn_no);

CREATE TABLE IF NOT EXISTS request_logs (
	request_id  TEXT PRIMARY KEY,
	tenant_id   TEXT NOT NULL,
	session_id  TEXT,
	ts          INTEGER NOT NULL,
	method      TEXT NOT NULL,
	path        TEXT NOT NULL,
	status_code INTEGER,
	duration_ms INTEGER,
	has_body    INTEGER DEFAULT 0,
	UNIQUE (tenant_id, request_id)
);

CREATE INDEX IF NOT EXISTS idx_logs_tenant_time ON request_logs (tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_logs_session     ON request_logs (session_id, ts DESC);

CREATE TABLE IF NOT EXISTS configs (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at INTEGER
);
`

// Pragma 单条 PRAGMA 配置项。
type Pragma struct {
	Name  string
	Value string
}

// DefaultPragmas 返回默认 PRAGMA 优化集合：
//   - journal_mode=WAL：写不阻塞读，适合网关并发读写场景；
//   - synchronous=NORMAL：WAL 模式推荐持久化级别，兼顾性能与安全；
//   - busy_timeout=5000：写锁冲突时最多等待 5s，避免并发写入立刻报 SQLITE_BUSY；
//   - foreign_keys=on：开启外键约束；
//   - cache_size=-64000：页缓存约 64MB（负数单位为 KiB）；
//   - temp_store=MEMORY：临时表与排序在内存中完成。
func DefaultPragmas() []Pragma {
	return []Pragma{
		{Name: "journal_mode", Value: "WAL"},
		{Name: "synchronous", Value: "NORMAL"},
		{Name: "busy_timeout", Value: "5000"},
		{Name: "foreign_keys", Value: "on"},
		{Name: "cache_size", Value: "-64000"},
		{Name: "temp_store", Value: "MEMORY"},
	}
}

// dsnPragmas 列出 go-sqlite3 支持通过 DSN 查询参数表达的 PRAGMA，
// 这些参数由驱动在池中每个新连接打开时自动执行。
var dsnPragmas = map[string]bool{
	"journal_mode": true,
	"synchronous":  true,
	"busy_timeout": true,
	"foreign_keys": true,
	"cache_size":   true,
}

// driverNameUnavailable 是 !cgo 构建（CGO_ENABLED=0）下 driverFor 返回的
// 哨兵驱动名。mattn/go-sqlite3 依赖 CGO，纯静态二进制无法编入该驱动；
// full 模式（PG+Redis）不经过本包，lite 模式在运行期会收到 OpenSQLite
// 的显式错误而非 "unknown driver" 的隐式失败（实现见 driver_nocgo.go）。
const driverNameUnavailable = "llmgw-sqlite-nocgo"

// mergePragmas 合并 PRAGMA 列表，extra 中的同名项按顺序覆盖 base。
func mergePragmas(base, extra []Pragma) []Pragma {
	merged := make([]Pragma, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	for _, list := range [][]Pragma{base, extra} {
		for _, p := range list {
			if i, ok := index[p.Name]; ok {
				merged[i] = p
				continue
			}
			index[p.Name] = len(merged)
			merged = append(merged, p)
		}
	}
	return merged
}

// buildDSN 将 PRAGMA 列表拆分为两部分：驱动 DSN 原生支持的参数写入
// 查询串（file:path?_key=value 形式），其余返回给 ConnectHook 逐连接执行。
func buildDSN(path string, pragmas []Pragma) (dsn string, hookPragmas []Pragma) {
	query := url.Values{}
	for _, p := range pragmas {
		if dsnPragmas[p.Name] {
			query.Set("_"+p.Name, p.Value)
			continue
		}
		hookPragmas = append(hookPragmas, p)
	}
	dsn = (&url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     filepath.ToSlash(path),
		RawQuery: query.Encode(),
	}).String()
	return dsn, hookPragmas
}

// pragmaValue 将 PRAGMA 值渲染为安全的 SQL 字面量：
// 纯整数直接输出，其余按字符串加单引号并转义，防止拼接注入。
func pragmaValue(v string) string {
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// driverFor 根据需要逐连接执行的 PRAGMA 返回驱动名：
// cgo 构建下注册（进程内仅一次）带 ConnectHook 的 go-sqlite3 自定义驱动
// （见 driver_cgo.go）；!cgo 构建下返回不可用哨兵（见 driver_nocgo.go）。

// InitSchema 在数据库上执行 SchemaSQL，创建全部表与索引（幂等）。
func InitSchema(db *sql.DB) error {
	if _, err := db.Exec(SchemaSQL); err != nil {
		return fmt.Errorf("sqlite: 初始化 schema 失败: %w", err)
	}
	return nil
}

// OpenSQLite 打开（必要时创建）SQLite 数据库并完成初始化：
//
//  1. 通过 DSN 参数 + ConnectHook 配置 PRAGMA，保证连接池中所有连接一致；
//  2. Ping 验证连通性；
//  3. 执行 InitSchema 建表。
//
// pragmas 为空时使用 DefaultPragmas；传入项按顺序覆盖同名默认项。
// 注意应使用文件路径而非 :memory:（WAL 模式不支持内存库）。
// 后续存储工厂接线（lite 模式）将调用本函数获取 *sql.DB。
// validPragmaName 校验 PRAGMA 名只含字母/数字/下划线（值侧已有 pragmaValue
// 转义；名字直接拼进 "PRAGMA <name> = ..." 语句，必须白名单防注入）。
func validPragmaName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false // 首字符不允许数字
			}
		default:
			return false
		}
	}
	return true
}

func OpenSQLite(path string, pragmas ...Pragma) (*sql.DB, error) {
	for _, p := range pragmas {
		if !validPragmaName(p.Name) {
			return nil, fmt.Errorf("sqlite: 非法 PRAGMA 名 %q（仅允许字母/数字/下划线）", p.Name)
		}
	}
	merged := mergePragmas(DefaultPragmas(), pragmas)
	dsn, hookPragmas := buildDSN(path, merged)

	name := driverFor(hookPragmas)
	if name == driverNameUnavailable {
		return nil, fmt.Errorf("sqlite: 当前二进制以 CGO_ENABLED=0 构建，SQLite 驱动不可用（lite 存储模式需 CGO 构建；full 模式不受影响）")
	}

	db, err := sql.Open(name, dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开数据库 %s 失败: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: 连接数据库 %s 失败: %w", path, err)
	}
	if err := InitSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
