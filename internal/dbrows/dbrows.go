// Package dbrows 提供 pgx.Rows 迭代错误分类的共享语义（R66）。
//
// 背景：R35-N1/R65 在 admin 聚合读面确立了「rows 迭代不得静默截断」的
// 分类三门（writeAggRowsErr / warnRowSkip / writeLookupErr），但那些
// helper 是 admin 包私有的。R66 把同一族缺陷迁移到 admin 之外的包时
// 发现：bg 探针、center/vibecoding 存储层、security 名单、domains
// 仓储层共 40+ 处循环没有任何共享语义可复用，每处各自发明 warn 形态
// 只会让「迭代中断静默返回 200 / 静默少算若干行」继续潜伏。
//
// 本包只提供**与传输层无关**的两件事，HTTP 状态码分类留给各包自己的
// 响应面：
//
//  1. Err —— 把 rows.Err() 的产物与单行 Scan 失败一起收敛成可上抛的
//     error，供仓储层/后台任务 fail-fast；
//  2. WarnRowSkip —— 给「跳行容错」通道留下服务端痕迹。跳行语义本身
//     保留（单行坏数据不应毒化整批聚合），但必须可查，否则 schema 演进
//     引发的持续类型失配会让聚合数据静默缩水且无人知晓。
//
// 为什么不放进 admin：admin 的三门要写 http.ResponseWriter，与本包的
// 纯 error/slog 语义正交。admin 继续用自己的分类门。
package dbrows

import (
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// Err 返回 rows 迭代的终检错误，正常收敛返回 nil。
//
// pgx.Rows 的迭代中断（连接断开、服务端错误、ctx 取消）只会让 Next()
// 返回 false，**不会**通过 Query 返回；不显式调用 Err() 就等于把
// 「读到第 500 行时连接断了」和「正常读完 500 行」当成同一件事。
//
// 惯用法：
//
//	rows, err := q.Query(ctx, sql, args...)
//	if err != nil {
//		return err
//	}
//	defer rows.Close()
//	var out []T
//	for rows.Next() {
//		var t T
//		if err := rows.Scan(&t.A, &t.B); err != nil {
//			warnRowSkip(op, err) // 或 return err
//			continue
//		}
//		out = append(out, t)
//	}
//	if err := rows.Err(); err != nil {
//		return fmt.Errorf("%s: iterate rows: %w", op, err)
//	}
func Err(rows pgx.Rows) error {
	if rows == nil {
		return nil
	}
	return rows.Err()
}

// WarnRowSkip 给聚合/批量循环内的单行 Scan 失败留下服务端痕迹。
//
// op 应当能唯一定位循环（建议 "<包>.<函数>" 或端点路径），否则日志无法
// 反查到具体消费面。
func WarnRowSkip(op string, err error) {
	if err == nil {
		return
	}
	slog.Warn("db rows scan failed; row skipped", "op", op, "error", err)
}

// SkipOrFail 收敛「跳行 + 留痕」这一最常见形态：单行 Scan 失败时记
// WarnRowSkip 并返回 true 表示「跳过」，调用方 continue（err==nil 返回
// false 继续正常路径）。12h 审计订正：原注释把返回值写反了——22 处
// 调用方全部按 `if SkipOrFail(...) { continue }` 使用，行为一直是对的。
//
// 用它取代裸 `if err != nil { continue }` 的收益是：调用点必然留下
// 痕迹，且痕迹带得上 op 定位串。
func SkipOrFail(op string, err error) bool {
	if err == nil {
		return false
	}
	WarnRowSkip(op, err)
	return true
}
