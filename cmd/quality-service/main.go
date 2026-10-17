package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/kaixuan/llm-gateway-go/internal/quality"
	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

func main() {
	// 命令行参数
	var (
		dbURL         = flag.String("db", os.Getenv("DATABASE_URL"), "Database URL")
		httpAddr      = flag.String("http", ":8081", "HTTP listen address")
		schedulerOn   = flag.Bool("scheduler", true, "Enable scheduler")
		scheduleEvery = flag.Duration("interval", 5*time.Minute, "Scheduler interval")
		logLevel      = flag.String("log-level", "info", "log level (debug|info|warn|error)")
	)
	flag.Parse()

	// 初始化日志（按级别分流到 stdout / stderr）
	configureLogging(*logLevel)

	log := logger.New("quality-service")
	log.Info("starting quality service",
		"http_addr", *httpAddr,
		"scheduler_enabled", *schedulerOn,
		"scheduler_interval", *scheduleEvery,
		"log_level", *logLevel,
	)

	// 连接数据库
	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		log.Error("failed to open database", "error", err.Error())
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Error("failed to ping database", "error", err.Error())
		os.Exit(1)
	}

	log.Info("database connected")

	// 创建 HTTP API
	apiHandler := quality.NewAPIHandler(db)
	mux := http.NewServeMux()
	apiHandler.RegisterRoutes(mux)

	// 健康检查
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	})

	// 启动 HTTP 服务器
	server := &http.Server{
		Addr:         *httpAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("http server listening", "addr", *httpAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server failed", "error", err.Error())
			os.Exit(1)
		}
	}()

	// 启动调度器 (如果启用)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *schedulerOn {
		scheduler := quality.NewScheduler(db, *scheduleEvery)
		go func() {
			if err := scheduler.Start(ctx); err != nil {
				log.Error("scheduler failed", "error", err.Error())
			}
		}()
		defer scheduler.Stop()
	}

	// 优雅关闭
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown failed", "error", err.Error())
	}

	log.Info("quality service stopped")
}

// configureLogging 按级别把日志分流到两条流。
//
// 为什么需要（2026-10-17 实测 245）：systemd 单元把
// StandardError 接到 logs/quality-service.error.log、StandardOutput 接到
// quality-service.log。但本服务此前**从未调用** slog.SetDefault —— Go 的
// slog 默认处理器把所有级别都写 stderr。于是：
//
//   - quality-service.log  自 7-19 起恒为 0 字节；
//   - quality-service.error.log 累积到 681MB / 3,593,726 行，
//     其中约 76% 是 INFO 的 "availability score calculated"，
//     而同期**真实错误只有 74 条**（"connection refused"）。
//
// 后果不是磁盘（实测约 6.6MB/天，18G 可用空间够数年），而是**告警信号
// 被淹没**：任何按 `*.error.log` 抓错误的规则/人工排查，都要先翻过 358 万行
// INFO 才能看到那 74 条错。
//
// 修法：Info/Debug → stdout，Warn/Error → stderr，并且统一为 JSON
// （此前是 Go log 的纯文本 `2026/10/17 10:46:23 INFO ...` 形态，与本仓
// 其余二进制（cmd/gateway 用 JSONHandler）不一致，无法直接用 jq 查）。
func configureLogging(levelStr string) {
	opts := &slog.HandlerOptions{Level: parseLogLevel(levelStr)}
	slog.SetDefault(slog.New(&levelSplitHandler{
		info:  slog.NewJSONHandler(os.Stdout, opts),
		error: slog.NewJSONHandler(os.Stderr, opts),
	}))
}

// parseLogLevel 解析 -log-level。未知值落回 info：
// 落 info 是「该记的都记到」，落 error 则会静默关掉全部流水，
// 对一个后台调度服务来说那是更难发现的失明。
func parseLogLevel(levelStr string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// levelSplitHandler 把 Debug/Info 送 stdout、其余送 stderr。
//
// 分界取 Warn：Warn 已经是「需要注意」，留在错误流里才不丢告警；
// Info 是「发生了什么」，属于正常流水。
type levelSplitHandler struct {
	info  slog.Handler
	error slog.Handler
}

func (h *levelSplitHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level >= slog.LevelWarn {
		return h.error.Enabled(ctx, level)
	}
	return h.info.Enabled(ctx, level)
}

func (h *levelSplitHandler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		return h.error.Handle(ctx, rec)
	}
	return h.info.Handle(ctx, rec)
}

func (h *levelSplitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelSplitHandler{info: h.info.WithAttrs(attrs), error: h.error.WithAttrs(attrs)}
}

func (h *levelSplitHandler) WithGroup(name string) slog.Handler {
	return &levelSplitHandler{info: h.info.WithGroup(name), error: h.error.WithGroup(name)}
}
