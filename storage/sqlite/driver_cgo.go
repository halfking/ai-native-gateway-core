//go:build cgo

// mattn/go-sqlite3 依赖 CGO：仅在 cgo 构建下编译真实驱动注册逻辑。
// CGO_ENABLED=0 构建见 driver_nocgo.go——full 模式（PG+Redis）的纯静态
// 二进制因此仍可编译，lite 模式在运行期收到显式错误。
package sqlite

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"strconv"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const (
	driverNameSQLite3 = "sqlite3"        // go-sqlite3 的默认注册名
	driverNamePrefix  = "sqlite3-llmgw-" // 带 ConnectHook 的自定义驱动名前缀
)

var (
	driverMu      sync.Mutex
	registeredDrv = map[string]bool{} // 已注册的自定义驱动名，防止 sql.Register 重复注册 panic
)

// driverFor 根据需要逐连接执行的 PRAGMA 返回驱动名：
// 无额外参数时直接复用 go-sqlite3 默认驱动；否则按 PRAGMA 集合的指纹
// 注册（进程内仅一次）一个带 ConnectHook 的驱动，保证池中每个连接
// 建立时都执行同一组 PRAGMA。
func driverFor(hookPragmas []Pragma) string {
	if len(hookPragmas) == 0 {
		return driverNameSQLite3
	}
	// 以 PRAGMA 集合的 FNV 指纹作为驱动名，不同集合互不冲突。
	h := fnv.New64a()
	for _, p := range hookPragmas {
		_, _ = h.Write([]byte(p.Name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p.Value))
	}
	name := driverNamePrefix + strconv.FormatUint(h.Sum64(), 16)

	driverMu.Lock()
	defer driverMu.Unlock()
	if registeredDrv[name] {
		return name
	}
	pragmas := append([]Pragma(nil), hookPragmas...)
	sql.Register(name, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			for _, p := range pragmas {
				stmt := "PRAGMA " + p.Name + " = " + pragmaValue(p.Value) + ";"
				if _, err := conn.Exec(stmt, nil); err != nil {
					return fmt.Errorf("sqlite: 执行 %s 失败: %w", stmt, err)
				}
			}
			return nil
		},
	})
	registeredDrv[name] = true
	return name
}
