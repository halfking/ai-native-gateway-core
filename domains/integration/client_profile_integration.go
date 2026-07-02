// Package integration — client_profile_integration（Task E）。
//
// 把 clientprofile 域（Store / Aggregator / Worker / Emitter）拼装到
// analysis 事件总线中：
//
//  1. 构造 PostgresStore（走 *sql.DB）+ Aggregator + ProfileWorker
//  2. 启动 bus.RunLoop 让 worker 消费 analysis_events 表里的事件
//  3. 返回 EventEmitter 供主流程（streaming/handler.go）发送事件
//
// 与 main_pipeline.go 中 IntentWorker 的模式一致：PGDBPool + Worker
// → RunLoop(ctx, worker, NewPGPollFunc, NewPGMarkFunc, LoopConfig)。
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/clientprofile" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ClientProfileDB 是 ProfileWorker 循环所需的最小 DB 接口。
//
// 同时被 *sql.DB 和 pgxpool.Pool 通过 bus.PGDB / bus.AsPGDB 桥接，
// 测试时可注入 pgxmock 实现而不需要真实数据库。
type ClientProfileDB = bus.PGDB

// ClientProfileLoopConfig 启动 ProfileWorker 循环所需的配置。
type ClientProfileLoopConfig struct {
	// Interval 轮询间隔；<=0 默认 5s
	Interval time.Duration
	// BatchSize 单次拉取条数；<=0 默认 10
	BatchSize int
	// Logger 可选；nil → slog.Default()
	Logger *slog.Logger
}

// ClientProfileBundle Setup 返回的拼装产物。
//
// 字段说明：
//   - Worker：原始 worker，调用方可挂载到 telemetry
//   - Emitter：供主流程发送事件（注入到 session handler / streaming handler）
//   - Store / Aggregator：供直接查询（仅当 Setup 提供了 sqlDB）
//   - Cancel：用于优雅停机
type ClientProfileBundle struct {
	Worker     *clientprofile.ProfileWorker
	Emitter    *clientprofile.EventEmitter
	Store      clientprofile.Store
	Aggregator *clientprofile.Aggregator
	Cancel     context.CancelFunc
}

// SetupClientProfileIntegration 构造 + 启动 worker 循环，返回 bundle。
//
// 参数：
//   - ctx：父 context；cancel 时 Loop 退出
//   - db：用于 PGPollFunc / PGMarkFunc 的 DB 句柄（*sql.DB / *pgxpool.Pool / pgxmock）
//   - sqlDB：可选，*sql.DB（用于 PostgresStore）；nil 时 Store 留空，由 cmd 自行注入
//   - publisher：analysis 事件发布器，用于 EventEmitter
//   - cfg：循环配置
func SetupClientProfileIntegration(
	ctx context.Context,
	db ClientProfileDB,
	sqlDB *sql.DB,
	publisher clientprofile.EventBus,
	cfg ClientProfileLoopConfig,
) (*ClientProfileBundle, error) {
	if db == nil {
		return nil, fmt.Errorf("clientprofile.Setup: db is required")
	}
	if publisher == nil {
		return nil, fmt.Errorf("clientprofile.Setup: publisher is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	// 1. Store（可选 — sqlDB 为 nil 时留空，由 cmd 注入）
	var store clientprofile.Store
	if sqlDB != nil {
		store = clientprofile.NewPostgresStore(sqlDB, logger)
	}

	// 2. Aggregator
	aggregator := clientprofile.NewAggregator(store, logger)

	// 3. Worker
	worker := clientprofile.NewProfileWorker(aggregator, logger)

	// 4. EventEmitter
	emitter := clientprofile.NewEventEmitter(publisher, logger)

	bundle := &ClientProfileBundle{
		Worker:     worker,
		Emitter:    emitter,
		Store:      store,
		Aggregator: aggregator,
	}

	// 5. 启动 RunLoop
	loopCtx, cancel := context.WithCancel(ctx)
	bundle.Cancel = cancel
	go runClientProfileLoop(loopCtx, db, worker, interval, batchSize, logger)

	logger.Info("clientprofile.Setup: integration complete",
		"worker", worker.Name(),
		"loop_interval", interval.String(),
		"batch_size", batchSize,
		"has_store", store != nil,
	)
	return bundle, nil
}

// runClientProfileLoop 启动 worker 的轮询循环。
func runClientProfileLoop(
	ctx context.Context,
	db ClientProfileDB,
	worker *clientprofile.ProfileWorker,
	interval time.Duration,
	batchSize int,
	logger *slog.Logger,
) {
	poll := bus.NewPGPollFunc(db, worker.SubscribedTypes(), batchSize)
	mark := bus.NewPGMarkFunc(db, logger)
	bus.RunLoop(ctx, worker, poll, mark, bus.LoopConfig{
		Interval:  interval,
		BatchSize: batchSize,
		Logger:    logger,
	})
}

// 编译期检查：*ProfileWorker 实现了 analysis.Worker
var _ analysis.Worker = (*clientprofile.ProfileWorker)(nil)
