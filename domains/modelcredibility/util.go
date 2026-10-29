// util.go — 包内小工具。
package modelcredibility

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

// isNoRows 判断一个查询错误是否是「没有行」。
//
// pgx v5 的 QueryRow 在无行时返回 pgx.ErrNoRows；本包多处把「没数据」
// 当成正常状态（缺失维度），所以统一走这个判定，避免各处写
// errors.Is(err, pgx.ErrNoRows) 时漏掉包装。
func isNoRows(err error) bool {
	return err != nil && errors.Is(err, pgx.ErrNoRows)
}
