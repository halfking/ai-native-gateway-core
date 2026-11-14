package pluginapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
)

// PluginEventBus abstracts event subscription for SSE
type PluginEventBus interface {
	Subscribe(pattern string, handler eventbus.EventHandler) (eventbus.Subscription, error)
}

// ServePluginEventsSSE serves plugin events as Server-Sent Events (text/event-stream).
// Clients receive plugin.* events as JSON in SSE data field.
func ServePluginEventsSSE(bus PluginEventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		// 发送初始注释保持连接
		fmt.Fprintf(w, ": SSE connected\n\n")
		flusher.Flush()

		// 订阅plugin.*事件
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		eventChan := make(chan *eventbus.Event, 16)
		sub, err := bus.Subscribe("plugin.*", func(ctx context.Context, event *eventbus.Event) error {
			select {
			case eventChan <- event:
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				// 客户端消费慢，丢弃（避免阻塞eventbus分发）
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: {\"error\":\"%s\"}\n\n", err.Error())
			flusher.Flush()
			return
		}
		defer sub.Unsubscribe()

		// 推送事件到客户端
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 心跳注释
				fmt.Fprintf(w, ": heartbeat\n\n")
				flusher.Flush()
			case event := <-eventChan:
				// 前端期望data为JSON对象，至少含type字段
				frontendEvent := map[string]interface{}{
					"type":      event.Type,
					"source":    event.Source,
					"timestamp": event.Timestamp.Unix(),
				}
				// 若Payload非空，尝试解析为JSON并合并
				if len(event.Payload) > 0 {
					var payload map[string]interface{}
					if json.Unmarshal(event.Payload, &payload) == nil {
						frontendEvent["payload"] = payload
					} else {
						// Payload不是JSON对象，作为字符串
						frontendEvent["payload"] = string(event.Payload)
					}
				}
				if len(event.Metadata) > 0 {
					frontendEvent["metadata"] = event.Metadata
				}

				data, _ := json.Marshal(frontendEvent)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
