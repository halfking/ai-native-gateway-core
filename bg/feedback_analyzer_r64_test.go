package bg

// feedback_analyzer_r64_test.go — AnalyzeOnce 非阻塞互斥的单测（R64 P2）。
//
// 钉住的行为：已有一轮分析在跑（mu 占用态）时，AnalyzeOnce 必须立即返回
// ErrAnalyzeInProgress 哨兵——admin handleAnalyze 据此回 409，而不是排队
// 阻塞整个默认 5 分钟超时窗。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAnalyzeOnceBusyReturnsSentinel(t *testing.T) {
	a := NewFeedbackAnalyzer(nil)
	a.mu.Lock() // 预置占用态，模拟在跑的一轮分析
	defer a.mu.Unlock()

	// db 为 nil：若互斥失效继续往下跑，首个 DB 调用即 panic——测试同样红。
	// 用带超时的 select 证明「非阻塞」本身，而不只验证返回值。
	done := make(chan error, 1)
	go func() {
		done <- a.AnalyzeOnce(context.Background())
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAnalyzeInProgress) {
			t.Fatalf("err = %v, want ErrAnalyzeInProgress", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AnalyzeOnce blocked while another analysis cycle holds the mutex")
	}
}
