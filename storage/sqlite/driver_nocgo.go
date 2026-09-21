//go:build !cgo

// CGO_ENABLED=0 构建下 mattn/go-sqlite3 无法编入：driverFor 返回不可用
// 哨兵驱动名，由 OpenSQLite 转换为显式错误。full 模式（PG+Redis）不经过
// 本包路径，部署脚本的纯静态构建因此不再断裂（历史事故：编译失败被
// 静默忽略 → 复用陈旧 gateway.build 部署）。
package sqlite

// driverFor 在 !cgo 构建下恒返回 driverNameUnavailable 哨兵，
// OpenSQLite 据此拒绝打开 SQLite 数据库并给出明确错误信息。
func driverFor([]Pragma) string {
	return driverNameUnavailable
}
