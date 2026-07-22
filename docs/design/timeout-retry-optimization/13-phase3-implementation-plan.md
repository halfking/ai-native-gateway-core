# Phase 3 实施计划 - Keepalive & 节点切换

## 📋 Phase 3 目标

实现前端Keepalive心跳和节点切换通知机制，保持长时间流式请求的连接活跃。

---

## 🎯 核心功能

### 功能1: Keepalive心跳发送

**目标**: 每N秒向客户端发送一个keepalive事件，防止代理超时

**实现**:
- SSE格式：`event: keepalive\ndata: {...}\n\n`
- 间隔：可配置，默认15秒
- 自动启停：请求开始时启动，结束时停止

### 功能2: 节点切换通知

**目标**: 当切换LLM节点（凭据）时，通知前端

**实现**:
- SSE格式：`event: node_switch\ndata: {...}\n\n`
- 包含：from_node、to_node、attempt、reason
- 触发时机：重试切换节点时

---

## 📦 实施步骤

### 步骤1: 创建KeepaliveSender（30分钟）

**文件**: `domains/streaming/keepalive_sender.go`

```go
package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// KeepaliveEvent represents a keepalive event sent to client
type KeepaliveEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
}

// NodeSwitchEvent represents a node switch notification
type NodeSwitchEvent struct {
	Type      string `json:"type"`
	FromNode  string `json:"from_node"`
	ToNode    string `json:"to_node"`
	Attempt   int    `json:"attempt"`
	Reason    string `json:"reason,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// KeepaliveSender manages keepalive heartbeats for streaming requests
type KeepaliveSender struct {
	writer      http.ResponseWriter
	flusher     http.Flusher
	interval    time.Duration
	stopChan    chan struct{}
	stoppedChan chan struct{}
	logger      *slog.Logger
}

// NewKeepaliveSender creates a new KeepaliveSender
func NewKeepaliveSender(w http.ResponseWriter, intervalSeconds int, logger *slog.Logger) *KeepaliveSender {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Warn("response writer does not support flushing, keepalive disabled")
		return nil
	}

	if intervalSeconds <= 0 {
		intervalSeconds = 15 // default
	}

	return &KeepaliveSender{
		writer:      w,
		flusher:     flusher,
		interval:    time.Duration(intervalSeconds) * time.Second,
		stopChan:    make(chan struct{}),
		stoppedChan: make(chan struct{}),
		logger:      logger,
	}
}

// Start begins sending keepalive events
func (k *KeepaliveSender) Start(ctx context.Context) {
	if k == nil {
		return
	}

	go func() {
		defer close(k.stoppedChan)
		
		ticker := time.NewTicker(k.interval)
		defer ticker.Stop()

		k.logger.Debug("keepalive sender started", "interval", k.interval)

		for {
			select {
			case <-ctx.Done():
				k.logger.Debug("keepalive sender stopped (context done)")
				return
			case <-k.stopChan:
				k.logger.Debug("keepalive sender stopped (explicit stop)")
				return
			case <-ticker.C:
				if err := k.sendKeepalive(); err != nil {
					k.logger.Warn("failed to send keepalive", "error", err)
				}
			}
		}
	}()
}

// Stop stops the keepalive sender
func (k *KeepaliveSender) Stop() {
	if k == nil {
		return
	}
	
	close(k.stopChan)
	<-k.stoppedChan // wait for goroutine to exit
}

// SendKeepalive sends a keepalive event immediately
func (k *KeepaliveSender) sendKeepalive() error {
	if k == nil {
		return nil
	}

	event := KeepaliveEvent{
		Type:      "keepalive",
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal keepalive event: %w", err)
	}

	// SSE format
	fmt.Fprintf(k.writer, "event: keepalive\n")
	fmt.Fprintf(k.writer, "data: %s\n\n", data)
	k.flusher.Flush()

	k.logger.Debug("keepalive sent", "timestamp", event.Timestamp)
	return nil
}

// SendNodeSwitch sends a node switch notification
func (k *KeepaliveSender) SendNodeSwitch(fromNode, toNode string, attempt int, reason string) error {
	if k == nil {
		return nil
	}

	event := NodeSwitchEvent{
		Type:      "node_switch",
		FromNode:  fromNode,
		ToNode:    toNode,
		Attempt:   attempt,
		Reason:    reason,
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal node switch event: %w", err)
	}

	// SSE format
	fmt.Fprintf(k.writer, "event: node_switch\n")
	fmt.Fprintf(k.writer, "data: %s\n\n", data)
	k.flusher.Flush()

	k.logger.Info("node switch notification sent",
		"from", fromNode,
		"to", toNode,
		"attempt", attempt,
		"reason", reason)
	return nil
}
```

### 步骤2: 集成到Executor（30分钟）

**文件**: `domains/streaming/executors/executor.go`

在Executor结构体中添加：

```go
type Executor struct {
	// ... existing fields
	
	// KeepaliveInterval (Phase 3, 2026-07-23): Keepalive heartbeat interval in seconds
	// Loaded from system_settings.retry.keepalive_interval_seconds
	KeepaliveInterval int
}
```

### 步骤3: 在executor_chat.go中使用（30分钟）

**文件**: `domains/streaming/executors/executor_chat.go`

找到流式处理的位置，添加KeepaliveSender：

```go
func (e *Executor) executeStreamingRequest(ctx context.Context, ...) error {
	// ... existing code
	
	// Phase 3: Start keepalive sender
	var keepaliveSender *streaming.KeepaliveSender
	if e.KeepaliveInterval > 0 {
		keepaliveSender = streaming.NewKeepaliveSender(w, e.KeepaliveInterval, logger)
		if keepaliveSender != nil {
			keepaliveSender.Start(ctx)
			defer keepaliveSender.Stop()
		}
	}
	
	// ... existing streaming code
	
	// When switching nodes (in retry logic):
	if keepaliveSender != nil && switchedNode {
		_ = keepaliveSender.SendNodeSwitch(oldNodeID, newNodeID, attemptNum, "retry_fallback")
	}
	
	return nil
}
```

### 步骤4: 从TimeoutConfig获取间隔（15分钟）

**文件**: `cmd/gateway/main.go`

在wiring Executor时添加：

```go
if timeoutConfig != nil {
	routingExec.KeepaliveInterval = timeoutConfig.GetKeepaliveInterval()
	slog.Info("keepalive interval configured", "seconds", routingExec.KeepaliveInterval)
}
```

---

## 🧪 测试计划

### 单元测试

**文件**: `domains/streaming/keepalive_sender_test.go`

```go
package streaming

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestKeepaliveSender_SendKeepalive(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	
	recorder := httptest.NewRecorder()
	sender := NewKeepaliveSender(recorder, 1, logger)
	
	if sender == nil {
		t.Fatal("expected non-nil sender")
	}
	
	err := sender.sendKeepalive()
	if err != nil {
		t.Fatalf("sendKeepalive failed: %v", err)
	}
	
	output := recorder.Body.String()
	if !strings.Contains(output, "event: keepalive") {
		t.Errorf("expected 'event: keepalive', got: %s", output)
	}
	if !strings.Contains(output, "data:") {
		t.Errorf("expected 'data:', got: %s", output)
	}
	if !strings.Contains(output, "\"type\":\"keepalive\"") {
		t.Errorf("expected keepalive type in data, got: %s", output)
	}
}

func TestKeepaliveSender_SendNodeSwitch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	
	recorder := httptest.NewRecorder()
	sender := NewKeepaliveSender(recorder, 1, logger)
	
	err := sender.SendNodeSwitch("node-1", "node-2", 2, "timeout")
	if err != nil {
		t.Fatalf("SendNodeSwitch failed: %v", err)
	}
	
	output := recorder.Body.String()
	if !strings.Contains(output, "event: node_switch") {
		t.Errorf("expected 'event: node_switch', got: %s", output)
	}
	if !strings.Contains(output, "node-1") {
		t.Errorf("expected 'node-1', got: %s", output)
	}
	if !strings.Contains(output, "node-2") {
		t.Errorf("expected 'node-2', got: %s", output)
	}
}

func TestKeepaliveSender_AutoSend(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	
	recorder := httptest.NewRecorder()
	sender := NewKeepaliveSender(recorder, 1, logger) // 1 second interval
	
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	
	sender.Start(ctx)
	defer sender.Stop()
	
	// Wait for at least 2 keepalives
	time.Sleep(2200 * time.Millisecond)
	
	output := recorder.Body.String()
	count := strings.Count(output, "event: keepalive")
	if count < 2 {
		t.Errorf("expected at least 2 keepalives, got %d. Output: %s", count, output)
	}
}
```

---

## 📊 验证标准

### 功能验证

1. **Keepalive发送** ✅
   - 每15秒收到一个keepalive事件
   - 事件格式正确（SSE）
   - 包含timestamp字段

2. **节点切换通知** ✅
   - 重试时收到node_switch事件
   - 包含from_node、to_node、attempt
   - 事件格式正确

3. **优雅启停** ✅
   - 请求开始时自动启动
   - 请求结束时自动停止
   - 无goroutine泄漏

### 性能验证

- Keepalive不影响正常流式响应
- CPU开销 < 0.1%
- 内存开销 < 1KB per request

---

## 📝 实施检查清单

- [ ] 创建keepalive_sender.go
- [ ] 创建keepalive_sender_test.go
- [ ] 在Executor添加KeepaliveInterval字段
- [ ] 在executor_chat.go集成KeepaliveSender
- [ ] 从TimeoutConfig获取间隔
- [ ] 运行单元测试
- [ ] 编译测试
- [ ] 本地功能测试
- [ ] 提交代码

---

## ⏱️ 预计时间

- 步骤1（KeepaliveSender）: 30分钟
- 步骤2（Executor集成）: 30分钟
- 步骤3（executor_chat集成）: 30分钟
- 步骤4（配置wiring）: 15分钟
- 测试与验证: 30分钟

**总计**: 约2小时15分钟

---

## 🎯 Phase 3 成功标准

- [ ] 所有单元测试通过
- [ ] 编译成功
- [ ] 客户端能收到keepalive事件
- [ ] 客户端能收到node_switch事件
- [ ] 无goroutine泄漏
- [ ] 代码已提交并推送

---

**准备开始Phase 3实施！** 🚀
