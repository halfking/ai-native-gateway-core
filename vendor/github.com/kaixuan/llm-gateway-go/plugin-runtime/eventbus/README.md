# Plugin Runtime EventBus

事件总线（EventBus）实现插件间的松耦合异步通信，采用发布/订阅模式。

## 功能特性

- ✅ **发布/订阅模式**：解耦事件生产者和消费者
- ✅ **通配符订阅**：支持 `plugin.*` 匹配所有插件事件
- ✅ **非阻塞发送**：慢订阅者不阻塞发布者
- ✅ **多订阅者**：同一事件可被多个订阅者接收
- ✅ **并发安全**：支持并发发布和订阅
- ✅ **标准事件类型**：预定义 20+ 种事件类型

## 架构设计

```
┌─────────────┐
│  Publisher  │────┐
└─────────────┘    │
                   │ Publish
┌─────────────┐    │         ┌──────────────┐
│  Publisher  │────┼────────▶│   EventBus   │
└─────────────┘    │         └──────────────┘
                   │                │
┌─────────────┐    │                │ Dispatch
│  Publisher  │────┘                │
└─────────────┘                     ▼
                          ┌──────────────────┐
                          │  Subscriptions   │
                          └──────────────────┘
                               │    │    │
                               ▼    ▼    ▼
                           Sub1  Sub2  Sub3
```

## 使用示例

### 基本用法

```go
import (
    "context"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
)

// 创建事件总线
bus := eventbus.NewMemoryEventBus(100) // 缓冲区大小 100
defer bus.Close()

// 订阅事件
handler := func(ctx context.Context, event *eventbus.Event) error {
    log.Printf("Received: %s from %s", event.Type, event.Source)
    return nil
}

sub, err := bus.Subscribe("plugin.started", handler)
if err != nil {
    log.Fatalf("Subscribe failed: %v", err)
}
defer sub.Unsubscribe()

// 发布事件
ctx := context.Background()
event := &eventbus.Event{
    ID:        "evt-123",
    Type:      "plugin.started",
    Source:    "supervisor",
    Timestamp: time.Now(),
    Payload:   []byte(`{"plugin_id":"my-plugin"}`),
}

if err := bus.Publish(ctx, event); err != nil {
    log.Fatalf("Publish failed: %v", err)
}
```

### 通配符订阅

```go
// 订阅所有插件事件
sub, err := bus.Subscribe("plugin.*", handler)

// 订阅所有 MaaS 库存事件
sub, err := bus.Subscribe("maas.inventory.*", handler)

// 订阅所有事件
sub, err := bus.Subscribe("*", handler)
```

### 使用辅助函数创建事件

```go
// 创建插件事件
payload := map[string]string{
    "plugin_id": "my-plugin",
    "version":   "1.0.0",
}
event, err := eventbus.NewPluginEvent(
    eventbus.EventTypePluginStarted,
    "my-plugin",
    payload,
)

// 创建 MaaS 事件
payload := map[string]interface{}{
    "resource_id": "res-123",
    "quantity":    100,
}
event, err := eventbus.NewMaaSEvent(
    eventbus.EventTypeMaaSInventorySellableChanged,
    "inventory-service",
    payload,
)
```

## 标准事件类型

### 插件生命周期事件

| 事件类型 | 说明 |
|---------|------|
| `plugin.started` | 插件启动 |
| `plugin.stopped` | 插件停止 |
| `plugin.crashed` | 插件崩溃 |
| `plugin.upgraded` | 插件升级 |
| `plugin.enabled` | 插件启用 |
| `plugin.disabled` | 插件禁用 |

### MaaS 库存事件

| 事件类型 | 说明 |
|---------|------|
| `maas.inventory.sellable_changed` | 可售库存变化 |
| `maas.inventory.reservation_prepared` | 库存预留准备 |
| `maas.inventory.reservation_committed` | 库存预留提交 |
| `maas.inventory.reservation_released` | 库存预留释放 |

### MaaS 商务事件

| 事件类型 | 说明 |
|---------|------|
| `maas.commercial.quote_issued` | 报价发出 |
| `maas.commercial.quote_accepted` | 报价接受 |
| `maas.commercial.quote_rejected` | 报价拒绝 |

### MaaS 交易事件

| 事件类型 | 说明 |
|---------|------|
| `maas.trading.contract_confirmed` | 合约确认 |
| `maas.trading.contract_cancelled` | 合约取消 |
| `maas.trading.order_placed` | 订单下达 |
| `maas.trading.order_fulfilled` | 订单履约 |

### MaaS 履约事件

| 事件类型 | 说明 |
|---------|------|
| `maas.fulfillment.plan_published` | 履约计划发布 |
| `maas.fulfillment.task_assigned` | 任务分配 |
| `maas.fulfillment.task_completed` | 任务完成 |

### MaaS 账本事件

| 事件类型 | 说明 |
|---------|------|
| `maas.ledger.usage_recorded` | 用量记录 |
| `maas.ledger.usage_settled` | 用量结算 |
| `maas.ledger.invoice_issued` | 账单发出 |

## 模式匹配规则

| 模式 | 匹配示例 | 不匹配示例 |
|------|---------|-----------|
| `plugin.started` | `plugin.started` | `plugin.stopped` |
| `plugin.*` | `plugin.started`, `plugin.stopped` | `maas.inventory.changed` |
| `maas.inventory.*` | `maas.inventory.sellable_changed` | `maas.commercial.quote_issued` |
| `*` | 所有事件 | - |

## 非阻塞发送机制

EventBus 使用带缓冲的 channel 和非阻塞 send，确保慢订阅者不会阻塞发布者：

```go
select {
case sub.ch <- event:
    // 事件发送成功
default:
    // Channel 满，丢弃事件并记录警告
    log.Printf("WARN: Event dropped for subscription %s", sub.id)
}
```

**配置建议**：
- 默认缓冲区大小：100
- 高负载场景：500-1000
- 低延迟场景：10-50

## 并发安全

所有公开方法都是并发安全的：

- **Publish**：使用 `RLock`，允许并发发布
- **Subscribe/Unsubscribe**：使用 `Lock`，独占修改
- **订阅处理**：每个订阅独立 goroutine，并发处理

## 性能特征

### 发布性能

```
BenchmarkPublish-8   	 1000000	      1047 ns/op
```

- 单次发布延迟：~1 μs
- 支持并发发布
- 非阻塞（即使订阅者慢）

### 内存占用

- 每个订阅：~1 KB（不含缓冲区）
- 缓冲区：`bufferSize * sizeof(Event pointer)` ≈ `bufferSize * 8 bytes`
- 100 订阅 + 默认缓冲：~100 KB

## 测试

运行单元测试：

```bash
go test -v -cover
```

当前测试覆盖率：**85.9%**

测试用例包括：
- 基本发布订阅
- 通配符订阅
- 多订阅者
- 非阻塞发送
- 取消订阅
- 事件总线关闭
- 模式匹配
- 并发发布

## 使用场景

### 1. 插件生命周期通知

Supervisor 发布插件状态变化事件，其他组件订阅：

```go
// Supervisor 发布
event, _ := eventbus.NewPluginEvent(
    eventbus.EventTypePluginCrashed,
    "my-plugin",
    map[string]string{"reason": "oom"},
)
bus.Publish(ctx, event)

// Reconciler 订阅
bus.Subscribe("plugin.*", func(ctx context.Context, event *Event) error {
    // 处理插件事件
    return nil
})
```

### 2. MaaS 经营闭环

各服务通过事件协调：

```go
// 库存服务：库存变化
event, _ := eventbus.NewMaaSEvent(
    eventbus.EventTypeMaaSInventorySellableChanged,
    "inventory-service",
    map[string]interface{}{"resource_id": "gpu-001", "quantity": 10},
)
bus.Publish(ctx, event)

// 商务服务：订阅库存变化，自动报价
bus.Subscribe("maas.inventory.*", func(ctx context.Context, event *Event) error {
    // 生成报价
    return nil
})
```

### 3. 跨插件通信

插件间解耦通信：

```go
// 插件 A 发布
event := &eventbus.Event{
    Type:   "custom.data.processed",
    Source: "plugin-a",
    Payload: data,
}
bus.Publish(ctx, event)

// 插件 B 订阅
bus.Subscribe("custom.*", handler)
```

## 注意事项

### 事件顺序

- **同一订阅者**：保证顺序（channel FIFO）
- **不同订阅者**：不保证顺序（并发处理）
- **全局顺序**：不保证（需要持久化版本）

### 事件持久化

当前内存版本不持久化事件：

- 进程重启：事件丢失
- 事件重放：不支持
- 消费者组：不支持

如需持久化，可实现 Redis Streams 版本。

### 慢订阅者

订阅者应快速返回，长时间操作应异步处理：

```go
// ❌ 不好：阻塞 handler
handler := func(ctx context.Context, event *Event) error {
    time.Sleep(10 * time.Second) // 阻塞！
    return nil
}

// ✅ 好：异步处理
handler := func(ctx context.Context, event *Event) error {
    go processEventAsync(event)
    return nil
}
```

## 扩展

### 自定义事件类型

```go
const (
    EventTypeCustomMyEvent = "custom.my_event"
)

event := &eventbus.Event{
    ID:        eventbus.generateEventID(), // 需要导出
    Type:      EventTypeCustomMyEvent,
    Source:    "my-service",
    Timestamp: time.Now(),
    Payload:   payload,
}
```

### 事件过滤

在 handler 中自定义过滤逻辑：

```go
handler := func(ctx context.Context, event *Event) error {
    // 只处理特定插件的事件
    if event.Metadata["plugin_id"] != "my-plugin" {
        return nil
    }
    // 处理事件
    return nil
}
```

## 依赖

无外部依赖，仅使用 Go 标准库。

## License

与主项目相同
