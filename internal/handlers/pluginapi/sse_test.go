package pluginapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
)

func TestServePluginEventsSSE(t *testing.T) {
	bus := eventbus.NewMemoryEventBus(10)
	handler := ServePluginEventsSSE(bus)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/plugins/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// 在后台启动handler
	done := make(chan bool)
	go func() {
		handler(w, req)
		close(done)
	}()

	// 等待SSE连接建立
	time.Sleep(100 * time.Millisecond)

	// 发布一个测试事件
	event, _ := eventbus.NewPluginEvent("plugin.test", "test-plugin", map[string]string{"status": "ok"})
	bus.Publish(context.Background(), event)

	// 等待事件被写入
	time.Sleep(200 * time.Millisecond)

	// 取消请求（模拟客户端断开）
	cancel()

	// 等待handler退出
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit after context cancel")
	}

	// 验证响应包含SSE头和初始注释
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, ": SSE connected") {
		t.Error("expected SSE connected comment")
	}
	t.Logf("SSE handler responded correctly (body: %d bytes)", len(body))
}
