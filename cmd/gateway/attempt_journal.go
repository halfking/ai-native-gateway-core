package main

import (
	"context"
	"database/sql"

	"github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// attemptJournalOpener 给对账的日志型 outbox（provider attempt journal）提供磁盘日志。
//
// ★ 为什么单独一个文件、而不是在 main.go 里直接写 sqlite.OpenJournal（2026-11-25 第 28 轮）：
//
//	main.go 的行号被大量仓内量具按「文件:行号」锚定（admin 路由快照、前端 29 个测试文件、
//	请求面文档的证据指针）⇒ 在它的导入块里加一行，所有锚点都要跟着挪。
//	本文件承担对 storage/sqlite 的依赖（cmd/gateway 本就在登记的 cgo 边界内），
//	main.go 的导入块与行数保持不变，只在调用点原地改成 attemptJournalOpener。
//
// 背景见 storage/sqlite.OpenJournal：对账包自己不再导入 go-sqlite3。
func attemptJournalOpener(ctx context.Context, path string) (*sql.DB, error) {
	return sqlite.OpenJournal(ctx, path)
}
