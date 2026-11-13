# Plugin Runtime IPC

进程间通信（IPC）层实现，基于 Unix Socket 和 JSON 协议，用于插件与核心之间的高性能通信。

## 功能特性

- ✅ **Unix Socket 通信**：高性能本地进程通信
- ✅ **JSON 协议**：简单、可读、易于调试
- ✅ **扩展点调用**：支持插件扩展点的请求/响应
- ✅ **健康检查**：插件健康状态检测
- ✅ **超时控制**：可配置的读写超时
- ✅ **并发安全**：支持多 goroutine 并发通信

## 架构设计

```
┌──────────────┐                    ┌──────────────┐
│   Gateway    │                    │    Plugin    │
│   (Client)   │                    │   (Server)   │
└──────┬───────┘                    └──────┬───────┘
       │                                   │
       │   IPC Request (JSON)              │
       ├──────────────────────────────────▶│
       │                                   │
       │   IPC Response (JSON)             │
       │◀──────────────────────────────────┤
       │                                   │
     Unix Socket                     Unix Socket
  (/tmp/plugin.sock)              (/tmp/plugin.sock)
```

## 使用示例

### 服务端（插件侧）

```go
import (
    "context"
    "encoding/json"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/ipc"
)

// 创建 IPC 服务端
server := ipc.NewServer("/tmp/my-plugin.sock")

// 注册扩展点处理器
server.RegisterHandler(ipc.MessageTypeExtensionPoint, 
    func(ctx context.Context, request []byte) ([]byte, error) {
        var req ipc.ExtensionPointRequest
        if err := json.Unmarshal(request, &req); err != nil {
            return nil, err
        }
        
        // 处理扩展点逻辑
        result := processExtensionPoint(req.Phase, req.Context)
        
        // 构造响应
        resp := ipc.ExtensionPointResponse{
            Type:    ipc.MessageTypeExtensionPoint,
            ID:      req.ID,
            Success: true,
            Result:  result,
        }
        
        return json.Marshal(resp)
    })

// 注册健康检查处理器
server.RegisterHandler(ipc.MessageTypeHealthCheck,
    func(ctx context.Context, request []byte) ([]byte, error) {
        var req ipc.HealthCheckRequest
        json.Unmarshal(request, &req)
        
        resp := ipc.HealthCheckResponse{
            Type:    ipc.MessageTypeHealthCheck,
            ID:      req.ID,
            Healthy: true,
            Message: "OK",
        }
        
        return json.Marshal(resp)
    })

// 启动服务端
ctx := context.Background()
if err := server.Start(ctx); err != nil {
    log.Fatalf("Start server failed: %v", err)
}

// 优雅关闭
defer server.Stop()
```

### 客户端（网关侧）

```go
import (
    "context"
    "encoding/json"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/ipc"
)

// 创建 IPC 客户端
client := ipc.NewClient("/tmp/my-plugin.sock")
defer client.Close()

// 发送扩展点请求
req := ipc.ExtensionPointRequest{
    Type:    ipc.MessageTypeExtensionPoint,
    ID:      "req-123",
    Phase:   "governance",
    Plugin:  "my-plugin",
    Context: json.RawMessage(`{"user_id":"user123"}`),
}

reqData, _ := json.Marshal(req)

ctx := context.Background()
respData, err := client.Send(ctx, reqData)
if err != nil {
    log.Printf("Send failed: %v", err)
    return
}

// 解析响应
var resp ipc.ExtensionPointResponse
if err := json.Unmarshal(respData, &resp); err != nil {
    log.Printf("Unmarshal failed: %v", err)
    return
}

if resp.Success {
    log.Printf("Extension point executed successfully")
} else {
    log.Printf("Extension point failed: %s", resp.Error)
}
```

### 带超时的请求

```go
// 使用 context 设置超时
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

respData, err := client.Send(ctx, reqData)
if err != nil {
    log.Printf("Request timeout or failed: %v", err)
}
```

## 消息协议

### 扩展点请求

```json
{
  "type": "extension_point",
  "id": "req-123",
  "phase": "governance",
  "plugin": "my-plugin",
  "context": {
    "user_id": "user123",
    "action": "create_resource"
  }
}
```

### 扩展点响应

```json
{
  "type": "extension_point",
  "id": "req-123",
  "success": true,
  "result": {
    "allowed": true,
    "reason": "User has permission"
  },
  "error": ""
}
```

### 健康检查请求

```json
{
  "type": "health_check",
  "id": "health-1"
}
```

### 健康检查响应

```json
{
  "type": "health_check",
  "id": "health-1",
  "healthy": true,
  "message": "OK"
}
```

## 消息类型

| 常量 | 值 | 说明 |
|------|---|------|
| `MessageTypeExtensionPoint` | `extension_point` | 扩展点调用 |
| `MessageTypeHealthCheck` | `health_check` | 健康检查 |

## 超时配置

### 默认超时

- **读超时**：10 秒
- **写超时**：10 秒
- **连接超时**：2 秒

### 自定义超时

通过 context 控制：

```go
// 1 秒超时
ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
defer cancel()

respData, err := client.Send(ctx, reqData)
```

## 错误处理

### 客户端错误

```go
respData, err := client.Send(ctx, reqData)
if err != nil {
    // 可能的错误：
    // - 连接失败：插件未启动或 socket 文件不存在
    // - 写入失败：网络问题或插件崩溃
    // - 读取失败：超时或连接中断
    // - 超时：context 超时
    log.Printf("IPC failed: %v", err)
    return
}
```

### 服务端错误

```go
server.RegisterHandler(messageType, func(ctx context.Context, request []byte) ([]byte, error) {
    // 返回错误会自动生成错误响应
    if err := validate(request); err != nil {
        return nil, fmt.Errorf("validation failed: %w", err)
    }
    
    // 正常处理
    return processRequest(request)
})
```

错误响应格式：

```json
{
  "type": "error",
  "success": false,
  "error": "validation failed: invalid user_id"
}
```

## 性能特征

### 基准测试结果

```
BenchmarkSingleRequest-16         10000 ops
BenchmarkConcurrentRequests-16    支持并发
BenchmarkJSONEncoding-16          1600485 ops  747.7 ns/op
```

### 延迟

- **小消息（< 1KB）**：< 1ms
- **中等消息（1-10KB）**：1-5ms
- **并发请求**：支持 100+ 并发连接

### 吞吐量

- 单连接：~1000 req/s
- 多连接：取决于并发数

## 测试

运行单元测试：

```bash
go test -v -cover
```

当前测试覆盖率：**70.2%**

运行性能测试：

```bash
go test -bench=. -benchmem
```

测试用例包括：
- 基本客户端-服务端通信
- 健康检查
- 并发请求（100 并发）
- 超时处理
- 未知消息类型处理
- 服务端启停

## 使用场景

### 1. 扩展点调用

网关调用插件的扩展点：

```go
// 治理扩展点
req := ipc.ExtensionPointRequest{
    Phase:  "governance",
    Plugin: "auth-plugin",
    Context: json.RawMessage(`{"user":"alice","action":"read"}`),
}
```

### 2. 健康检查

定期检查插件健康状态：

```go
req := ipc.HealthCheckRequest{
    Type: ipc.MessageTypeHealthCheck,
    ID:   "health-check-1",
}

// 发送健康检查
resp, err := client.Send(ctx, reqData)
// 如果失败或超时，标记插件不健康
```

### 3. 插件间通信

通过网关中转的插件间通信：

```
Plugin A → Gateway (Client) → Gateway (Router) → Plugin B
```

## 注意事项

### Socket 文件路径

- 使用绝对路径
- 确保目录存在且可写
- 服务端启动时会删除旧的 socket 文件

### 并发安全

- 客户端可以安全地被多个 goroutine 使用
- 服务端的处理器应该是线程安全的

### 消息大小限制

- 当前缓冲区：1MB
- 超过限制的消息可能被截断
- 建议单个消息 < 100KB

### 连接管理

- 当前实现：每次请求创建新连接
- 未来优化：连接池复用
- 性能影响：连接开销 ~100μs

## 扩展

### 添加自定义消息类型

```go
const MessageTypeCustom = "custom_type"

// 注册处理器
server.RegisterHandler(MessageTypeCustom, customHandler)

// 发送自定义消息
req := map[string]interface{}{
    "type": MessageTypeCustom,
    "id":   "custom-1",
    "data": "custom payload",
}
```

### 集成 Protobuf

未来可以替换 JSON 为 Protobuf 以提升性能：

```protobuf
message ExtensionPointRequest {
  string id = 1;
  string phase = 2;
  string plugin = 3;
  bytes context = 4;
}
```

## 依赖

无外部依赖，仅使用 Go 标准库。

## License

与主项目相同
