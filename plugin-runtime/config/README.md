# Plugin Runtime Config

配置热重载（Hot Reload）实现，监听配置文件变化并自动重载，支持配置验证和原子替换。

## 功能特性

- ✅ **热重载**：监听配置文件变化，自动重载
- ✅ **配置验证**：重载前验证配置，无效配置不生效
- ✅ **原子替换**：使用 atomic.Value 实现无锁读取
- ✅ **防抖动**：100ms 防抖，避免频繁重载
- ✅ **回调机制**：支持注册重载回调函数
- ✅ **并发安全**：支持多 goroutine 并发读取配置

## 架构设计

```
┌──────────────┐
│ Config File  │
│ (gateway.yaml)│
└──────┬───────┘
       │ fsnotify
       ▼
┌──────────────┐     Validate      ┌────────────┐
│   Watcher    │─────────────────▶│ Validator  │
└──────┬───────┘                   └────────────┘
       │ Reload                           │
       ▼                                  ▼ OK
┌──────────────┐     atomic.Store  ┌────────────┐
│  Reloader    │──────────────────▶│ConfigHolder│
└──────┬───────┘                   └─────┬──────┘
       │ Callbacks                       │ atomic.Load
       ▼                                  ▼
┌──────────────┐                   ┌────────────┐
│   Handlers   │                   │  Readers   │
└──────────────┘                   └────────────┘
```

## 使用示例

### 基本用法

```go
import (
    "context"
    "github.com/kaixuan/llm-gateway-go/plugin-runtime/config"
)

// 初始配置
initialConfig := &config.Config{
    Server: config.ServerConfig{
        Host: "localhost",
        Port: 8080,
    },
    Plugins: config.PluginsConfig{
        Directory: "/etc/llm-gateway/plugins",
        Enabled:   []string{"auth", "rate-limit"},
    },
}

// 创建重载器
reloader, err := config.NewConfigReloader(initialConfig, config.NewValidator())
if err != nil {
    log.Fatalf("Failed to create reloader: %v", err)
}
defer reloader.Stop()

// 开始监听配置文件
ctx := context.Background()
if err := reloader.Watch(ctx, "/etc/llm-gateway/gateway.yaml"); err != nil {
    log.Fatalf("Failed to watch config: %v", err)
}

// 获取当前配置
config := reloader.GetConfig()
log.Printf("Server running on %s:%d", config.Server.Host, config.Server.Port)
```

### 注册重载回调

```go
// 注册回调函数，在配置重载成功后执行
reloader.OnReload(func(ctx context.Context, oldConfig, newConfig *config.Config) error {
    log.Printf("Config reloaded: port changed from %d to %d", 
        oldConfig.Server.Port, newConfig.Server.Port)
    
    // 执行重载逻辑（如重启服务、更新缓存等）
    return restartServer(newConfig)
})
```

### 手动触发重载

```go
// 不等待文件变化，立即重载配置
ctx := context.Background()
if err := reloader.Reload(ctx); err != nil {
    log.Printf("Manual reload failed: %v", err)
}
```

### 线程安全读取配置

```go
// 在任意 goroutine 中读取配置（非阻塞）
go func() {
    for {
        config := reloader.GetConfig()
        // 使用配置
        processRequest(config)
    }
}()
```

## 配置文件格式

`gateway.yaml` 示例：

```yaml
server:
  host: 0.0.0.0
  port: 8080

plugins:
  directory: /etc/llm-gateway/plugins
  enabled:
    - auth
    - rate-limit
    - logging

security:
  tls_enabled: true
  cert_file: /etc/llm-gateway/certs/server.crt
  key_file: /etc/llm-gateway/certs/server.key
```

## 配置验证规则

### Server 配置

- `host`：必填，服务器监听地址
- `port`：必填，范围 1-65535

### Plugins 配置

- `directory`：必填，插件目录路径
- 目录必须存在且可访问
- `enabled`：可选，启用的插件列表

### Security 配置

- `tls_enabled`：可选，是否启用 TLS
- 如果启用 TLS：
  - `cert_file`：必填，证书文件路径
  - `key_file`：必填，密钥文件路径
  - 文件必须存在且可读

## 重载流程

```
文件变化 → 防抖动(100ms) → 加载配置 → 验证配置 → 原子替换 → 执行回调
                                  ↓ 失败
                           保留旧配置 + 记录错误
```

### 1. 文件监听

使用 `fsnotify` 监听配置文件的 Write 和 Create 事件。

### 2. 防抖动

文件变化后等待 100ms，避免编辑器多次保存导致的频繁重载。

### 3. 加载配置

读取并解析 YAML 配置文件。

### 4. 验证配置

执行完整的配置验证：
- Schema 验证（必填字段、类型检查）
- 业务规则验证（端口范围、文件存在性等）

### 5. 原子替换

**验证成功**：使用 `atomic.Value.Store()` 原子替换配置
**验证失败**：保留旧配置，记录错误日志

### 6. 执行回调

调用所有注册的 `ReloadHandler`，传递旧配置和新配置。

## 原子替换机制

使用 Go 的 `atomic.Value` 实现无锁读取：

```go
type ConfigHolder struct {
    config atomic.Value // stores *Config
}

// 读取：非阻塞，无锁
func (h *ConfigHolder) Get() *Config {
    return h.config.Load().(*Config)
}

// 写入：原子操作
func (h *ConfigHolder) Set(newConfig *Config) {
    h.config.Store(newConfig)
}
```

**优势**：
- 读取操作零开销（无锁）
- 写入操作原子性
- 读写不互相阻塞

## 错误处理

### 无效配置

无效配置不会中断服务，旧配置继续生效：

```
2026/11/13 16:19:28 ERROR: Config reload failed: validate config: 
    invalid server config: server port must be between 1 and 65535, got 99999
```

### 文件读取失败

临时文件读取失败不影响现有配置：

```
2026/11/13 16:19:28 ERROR: Config reload failed: load config: 
    read file: open /path/to/config: no such file or directory
```

### 回调执行失败

回调失败不影响配置更新和其他回调：

```
2026/11/13 16:19:28 ERROR: Reload handler 2 failed: restart server: 
    address already in use
```

## 测试

运行单元测试：

```bash
go test -v -cover
```

当前测试覆盖率：**83.2%**

测试用例包括：
- 原子配置访问
- 并发读写测试
- 配置验证（有效/无效）
- 热重载功能
- 无效配置处理
- 手动重载
- 回调机制
- 停止功能

## 性能特征

### 读取性能

- **延迟**：~10 ns（atomic.Load）
- **吞吐量**：无限制（无锁读取）
- **并发**：支持任意数量并发读取

### 重载性能

- **防抖间隔**：100 ms
- **重载延迟**：< 500 ms（读取 + 解析 + 验证 + 替换）
- **不阻塞读取**：重载时读取操作不受影响

### 内存占用

- **ConfigHolder**：~8 bytes（指针）
- **Config**：取决于配置大小，通常 < 1 KB
- **Watcher**：~100 KB（fsnotify 开销）

## 使用场景

### 1. 动态调整服务端口

```yaml
# 修改 gateway.yaml
server:
  port: 9090  # 从 8080 改为 9090
```

无需重启，配置自动生效（需要业务代码支持）。

### 2. 启用/禁用插件

```yaml
plugins:
  enabled:
    - auth
    - rate-limit
    # - logging  # 注释掉禁用 logging 插件
```

修改后自动重载插件配置。

### 3. 更新 TLS 证书

```yaml
security:
  cert_file: /etc/llm-gateway/certs/new-server.crt
  key_file: /etc/llm-gateway/certs/new-server.key
```

证书更新后自动重载。

### 4. 紧急降级

创建降级配置文件，快速覆盖生产配置：

```bash
cp gateway-degraded.yaml gateway.yaml
# 自动触发重载，进入降级模式
```

## 注意事项

### 配置文件编辑

某些编辑器（如 vim）保存文件时会先删除再创建，确保：
- 监听 Create 事件
- 防抖动机制处理多次事件

### 回调执行

重载回调应该：
- **快速返回**：避免阻塞重载流程
- **幂等性**：可能被多次调用
- **错误处理**：返回错误不影响配置更新

### 配置一致性

重载期间：
- 读取操作：读到旧配置或新配置（原子替换）
- 不会读到部分更新的配置
- 不会因为重载而阻塞

### 文件权限

确保进程有权限：
- 读取配置文件
- 读取 TLS 证书/密钥文件
- 访问插件目录

## 扩展

### 自定义验证规则

```go
type customValidator struct {
    baseValidator config.Validator
}

func (v *customValidator) Validate(config *config.Config) error {
    // 先执行基础验证
    if err := v.baseValidator.Validate(config); err != nil {
        return err
    }
    
    // 添加自定义规则
    if config.Server.Port == 8080 {
        return fmt.Errorf("port 8080 is reserved")
    }
    
    return nil
}
```

### 添加配置字段

```go
// 扩展 Config 结构
type ExtendedConfig struct {
    config.Config
    
    // 自定义字段
    Database DatabaseConfig `yaml:"database"`
}
```

## 依赖

```
github.com/fsnotify/fsnotify v1.10.1
gopkg.in/yaml.v3 v3.0.1
```

## License

与主项目相同
