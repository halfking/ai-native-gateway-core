// 本文件保留双模式存储的配置类型。
// 存储工厂本体位于子包 storage/factory（github.com/kaixuan/llm-gateway-go/storage/factory）：
// 各实现子包（storage/sqlite、storage/memory、storage/file）反向依赖根包获取
// 接口与类型，根包若 import 实现包将构成 import cycle，因此工厂必须独立成包，
// 依赖方向详见 storage/factory 包文档。
package storage

import "time"

// StorageConfig 存储配置。
type StorageConfig struct {
	Mode StorageMode

	// Full 模式专用
	PostgresURL string
	RedisURL    string

	// Lite 模式专用
	SQLitePath string
	BodiesDir  string
	CacheDir   string
	LogsDir    string

	// AsyncWriters lite 模式下 BodiesStore 后台异步写 worker 数；
	// <=0 时工厂回落默认值 4（见 factory 包 defaultFileWorkers）。
	AsyncWriters int

	// SQLitePragmas lite 模式下 SQLite PRAGMA 覆盖项（PRAGMA 名 → 值）。
	// 空表示全部使用 storage/sqlite.DefaultPragmas() 默认集合，显式项逐条覆盖同名默认项。
	// 使用中立 map 类型：根包不能 import storage/sqlite（会构成 import cycle），
	// 由 factory 子包转换为 sqlitestore.Pragma。
	SQLitePragmas map[string]string

	// 通用配置
	MaxConnections int
	Timeout        time.Duration
}
