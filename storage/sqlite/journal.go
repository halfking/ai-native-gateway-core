package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// ErrDriverUnavailable：当前二进制以 CGO_ENABLED=0 构建，SQLite 驱动不可用（见 driver_nocgo.go）。
// 调用方用 errors.Is 判别，决定是跳过测试还是走降级路径。
var ErrDriverUnavailable = errors.New("sqlite driver unavailable: binary built with CGO_ENABLED=0")

// OpenJournal 打开一个「日志型」SQLite 文件：不建 lite 模式的 schema、不跑迁移，建表由调用方负责。
// 给对账的日志型 outbox（provider attempt journal）与它的投影锁文件用。
//
// ★ 为什么在这里、而不是由调用方自己打开（2026-11-25 第 28 轮，cgo 边界漂移的修法）：
//
//	mattn/go-sqlite3 只能从本包进入依赖图——tests/verify_cgo_convention_test.sh 登记的 cgo 边界。
//	domains/reconciliation 此前在包顶层无条件空导入它（cdd155fff 带入）⇒
//	· cgo=1 闭包里能到达 go-sqlite3 的包由登记的 6 个涨到 39 个；
//	· cgo=0 的网关依赖图里也重新出现 go-sqlite3（tests/deploy_nocgo_build_test.sh 红）。
//	⇒ 「打开日志」收进本包，由调用方（cmd/gateway）注入 ⇒ 边界回到登记值。
//
// 与 OpenSQLite 同口径：!cgo 构建下返回包了 ErrDriverUnavailable 的显式错误，不静默。
// 行为与原先 reconciliation.openAttemptSQLite 逐项一致：先以 0600 建文件、单连接、Ping 探活。
func OpenJournal(ctx context.Context, path string) (*sql.DB, error) {
	name := driverFor(nil)
	if name == driverNameUnavailable {
		return nil, fmt.Errorf("sqlite: 当前二进制以 CGO_ENABLED=0 构建，SQLite 驱动不可用（日志型 outbox 需 CGO 构建）: %w", ErrDriverUnavailable)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 创建日志文件 %s 失败: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("sqlite: 关闭日志文件 %s 失败: %w", path, err)
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "_busy_timeout=75&_synchronous=FULL&_txlock=immediate"}
	db, err := sql.Open(name, dsn.String())
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开日志 %s 失败: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: 连接日志 %s 失败: %w", path, err)
	}
	return db, nil
}
