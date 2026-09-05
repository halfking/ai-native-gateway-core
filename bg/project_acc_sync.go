// Package bg — optional ACC project sync worker (Phase N: 2026-08-20).
//
// Enable with LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL (e.g. "10m"). Manual
// sync remains available via POST /api/admin/projects/sync-from-acc.
//
// 与 work_type ACC 同步的设计差异：
//
//   - 项目同步默认 **关闭**（env 缺省 = 启动不挂载 worker）。work_type
//     同步是常驻启动点；项目同步遵循计划第 3 项要求，因为它的下游
//     ProjectResolver 现在也是默认关闭（设置开关），如果默认开启同步会
//     出现"已同步但无人消费"的孤儿数据。
//   - tenant 维度：syncFn 接受 tenantID 入参。当前网关是单租户场景
//     （没有跨租户路由），传空串 = 同步公共项目；多租户部署应传入
//     当前租户 ID 或在 main.go 里 fan-out。
//   - 单轮超时 60s，覆盖分页 + 一次完整 upsert + disable-missing。超过
//     这个时间通常是 PG 卡死或 ACC 死循环，必须放弃这一轮但继续下一轮。
package bg

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// projectSyncRoundTimeout 单轮同步的超时上限。60s 覆盖：
//
//   - 200 项 × 1 页（默认 page_size 上限，accProjectsPageSize）的 HTTP 拉取。
//   - 单事务 upsert ~200 行 + 一条 UPDATE。
//   - 网络抖动 + DNS 重试。
//
// 远高于此值的耗时必然意味着 PG/ACC 出现严重故障，宁可放弃这一轮。
const projectSyncRoundTimeout = 60 * time.Second

// ProjectSyncFn 是 worker 调用的同步函数签名。
//
// 注入抽象便于测试（fake 函数可替代 admin.SyncProjectsFromACCForBG）。
// 入参 ctx 由 worker 加超时；tenantID 由 worker 启动时从 env 读取一次，
// 运行时不变。
type ProjectSyncFn func(ctx context.Context, db *pgxpool.Pool, tenantID string) error

// StartProjectACCSync 启动 ACC 项目同步 worker。env 未配置时直接返回，
// 不挂任何 goroutine；env 配置非法时记 WARN 后返回。
//
// syncFn 接受 db 和 tenantID，由调用方注入默认实现（admin.SyncProjectsFromACCForBG）。
// 当 db 为 nil 时 worker 也不启动——这是 fail-closed 的双重保险。
//
// 设计参考：StartWorkTypeACCSync。但项目 worker 额外接受 tenantID，因为
// 同一个网关进程可能需要为多个租户同步；当前默认实现只同步公共项目，
// 多租户部署应该在 main.go 启动多个 worker 实例或使用 fan-out。
func StartProjectACCSync(ctx context.Context, db *pgxpool.Pool, syncFn ProjectSyncFn) {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL"))
	if raw == "" {
		slog.Info("project ACC sync worker disabled (set LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL to enable)")
		return
	}
	if db == nil {
		slog.Warn("project ACC sync worker disabled: pgx pool is nil")
		return
	}
	if syncFn == nil {
		slog.Warn("project ACC sync worker disabled: syncFn is nil")
		return
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < time.Minute {
		slog.Warn("invalid LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL; minimum is 1m",
			"value", raw, "error", err)
		return
	}
	tenantID := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ACC_PROJECT_TENANT"))
	if tenantID == "" {
		slog.Info("project ACC sync worker starting without explicit tenant (公共项目模式)")
	} else {
		slog.Info("project ACC sync worker starting",
			"interval", interval.String(),
			"tenant", tenantID)
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCtx, cancel := context.WithTimeout(ctx, projectSyncRoundTimeout)
				if err := syncFn(runCtx, db, tenantID); err != nil {
					slog.Warn("project ACC sync failed",
						"tenant", tenantID, "error", err)
				}
				cancel()
			}
		}
	}()
}
