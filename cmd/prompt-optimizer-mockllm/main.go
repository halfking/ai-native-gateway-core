// Command prompt-optimizer-mockllm 提供一个 OpenAI 兼容的 mock LLM，
// 供提示词优化模块的本地 Docker 测试使用。
//
// 为什么需要它：/api/v1/compare 的 mode=output 与
// /api/v1/optimize/evaluate 都要真的跑一次模型才能打分。
// 没有模型通道时这两条路径只能返回"未配置"错误，端到端就测不到。
//
// 复用仓库既有的 internal/providers/mock（它已实现 OpenAI Chat Completions
// 协议），不另写一份协议实现。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
)

func main() {
	var (
		listen = flag.String("listen", ":8099", "HTTP listen address")
		delay  = flag.Duration("delay", 20*time.Millisecond, "模拟推理延迟")
	)
	flag.Parse()

	mux := http.NewServeMux()
	// OpenAI 兼容的 chat completions（流式/非流式由请求体决定）。
	mux.Handle("/v1/chat/completions", mock.ChatCompletions("fast", func() time.Duration { return *delay }))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"healthy"}`)
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("mock llm listening on %s", *listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("mock llm failed: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Printf("mock llm stopped")
}
