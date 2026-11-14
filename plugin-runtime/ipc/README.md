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

每条连接传输一个完整 JSON 请求和一个完整 JSON 响应；对象保持原有 JSON wire 格式，无长度前缀。`ReadMessage` 通过有界 JSON 解码处理流分片，`WriteMessage` 校验合法 JSON 并处理短写；标量 JSON 以换行分隔。请求和响应分别受 `MaxMessageSize = 16 MiB` 限制，超限返回 `ErrMessageTooLarge`，不会把部分消息当作成功结果。

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

- **客户端默认请求超时**：10 秒，覆盖连接、写入与读取；已有 context deadline 时使用该 deadline
- **服务端读/写超时**：各 10 秒
- **连接超时**：最多 2 秒，context 可提前终止
- **主动取消**：立即中断连接或进行中的读写；`errors.Is` 可识别 `context.Canceled` / `context.DeadlineExceeded`

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

## 验证与基准

运行当前模块的测试与静态检查：

```bash
go test -race -count=1 -cover ./...
go vet ./...
```

测试覆盖合法 10 MiB JSON 完整往返、双向分片、16 MiB 消息边界、取消阻塞读写、Close 与并发请求、Stop 取消并等待 handler、重复启停和 100 并发请求。覆盖率以当前命令输出为准。

运行有限次数的真实通信基准：

```bash
go test -run '^$' -bench . -benchtime=3x -benchmem -count=1 ./...
```

载荷为合法 JSON，分别包含 1 KiB、100 KiB 和 10 MiB 的 JSON context（总请求还包含信封字段）。每次 Marshal、Send、解码和完整 echo 都检查结果。并发基准启动 100 个 worker，每个 `op` 为每个 worker 各一次请求，因此显示 `100 requests/op`，其 `ns/op` 是每组 100 个请求的耗时，不是单请求延迟。每个请求仍使用独立连接。历史未检查错误的测量结果不可用作性能依据。

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

- 单条 JSON 请求或响应上限为 16 MiB，包含 JSON 字符及空白（不含标量传输分隔符）
- 解码器最多读取上限加一个字节，防止消息读取无界增长
- 10 MiB 合法 JSON 已纳入完整字节往返回归测试

### 连接管理

- 每次请求创建新连接，客户端统一管理连接的生命周期
- `Client.Close` 拒绝后续请求、取消进行中的 dial 并关闭活跃连接，返回 `ErrClientClosed`
- `Server.Stop` 取消 handler context、关闭监听和活跃连接，并等待 handler 退出；handler 必须响应取消，不要在 handler 内同步调用 `Stop`
- `Stop` 可重复或并发调用；`Start` 只允许一次，停止后需创建新的 Server
- 取消传入 `Start` 的父 context 会触发相同的停止流程

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
