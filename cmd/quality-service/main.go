package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
	)
	flag.Parse()

	// 初始化日志
	log := logger.New("quality-service")
	log.Info("starting quality service",
		"http_addr", *httpAddr,
		"scheduler_enabled", *schedulerOn,
		"scheduler_interval", *scheduleEvery,
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
